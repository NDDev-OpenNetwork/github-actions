package observabilityrules

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeOpenObserve struct {
	destination bool
	streams     []string
	// streamsByType, when set, answers the streams listing per requested type
	// the way the real API does; the flat streams list answers every type.
	streamsByType map[string][]string
	alerts        map[string]OpenObserveAlert
}

func (f *fakeOpenObserve) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if username, password, ok := request.BasicAuth(); !ok || username != "operator" || password != "secret" {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/default/alerts/destinations":
		if f.destination {
			_ = json.NewEncoder(writer).Encode([]destinationSummary{{Name: "fleet_oncall"}})
		} else {
			_ = json.NewEncoder(writer).Encode([]destinationSummary{})
		}
	case request.Method == http.MethodGet && request.URL.Path == "/api/default/streams":
		names := f.streams
		if f.streamsByType != nil {
			names = f.streamsByType[request.URL.Query().Get("type")]
		}
		response := streamList{}
		for _, name := range names {
			response.List = append(response.List, struct {
				Name string `json:"name"`
			}{Name: name})
		}
		_ = json.NewEncoder(writer).Encode(response)
	case request.URL.Path == "/api/v2/default/alerts" && request.Method == http.MethodGet:
		response := alertList{}
		for id, alert := range f.alerts {
			response.List = append(response.List, struct {
				AlertID string   `json:"alert_id"`
				Name    string   `json:"name"`
				Tags    []string `json:"tags"`
			}{AlertID: id, Name: alert.Name, Tags: alert.Tags})
		}
		_ = json.NewEncoder(writer).Encode(response)
	case request.URL.Path == "/api/v2/default/alerts" && request.Method == http.MethodPost:
		var alert OpenObserveAlert
		if json.NewDecoder(request.Body).Decode(&alert) != nil {
			http.Error(writer, "bad alert", http.StatusBadRequest)
			return
		}
		f.alerts["created-"+alert.Name] = alert
		_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
	case strings.HasPrefix(request.URL.Path, "/api/v2/default/alerts/"):
		id := strings.TrimPrefix(request.URL.Path, "/api/v2/default/alerts/")
		alert, exists := f.alerts[id]
		if !exists {
			http.Error(writer, "missing", http.StatusNotFound)
			return
		}
		switch request.Method {
		case http.MethodGet:
			_ = json.NewEncoder(writer).Encode(alert)
		case http.MethodPut:
			// OpenObserve accepts the whole document and keeps the stream it
			// already has. Modelled here because the difference is invisible
			// otherwise: the PUT succeeds, everything but the stream changes,
			// and the reconcile reports drift forever.
			previousStream := alert.StreamName
			if json.NewDecoder(request.Body).Decode(&alert) != nil {
				http.Error(writer, "bad alert", http.StatusBadRequest)
				return
			}
			alert.StreamName = previousStream
			f.alerts[id] = alert
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		case http.MethodDelete:
			delete(f.alerts, id)
			_ = json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		default:
			http.Error(writer, "method", http.StatusMethodNotAllowed)
		}
	default:
		http.Error(writer, "not found", http.StatusNotFound)
	}
}

func desiredFixture(t *testing.T) OpenObserveBundle {
	t.Helper()
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	bundle.Rules = bundle.Rules[:1]
	desired, err := RenderOpenObserve(bundle, "fleet_oncall", false)
	if err != nil {
		t.Fatal(err)
	}
	return desired
}

func TestReconcileBlocksWithoutDestinationOrMetricStream(t *testing.T) {
	desired := desiredFixture(t)
	fake := &fakeOpenObserve{alerts: map[string]OpenObserveAlert{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewOpenObserveClient(server.URL, "operator", "secret")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != "blocked" || plan.DestinationPresent || len(plan.MissingStreams) != 1 || len(plan.Actions) != 0 {
		t.Fatalf("blocked plan = %#v", plan)
	}
}

func TestReconcileCreateUpdateDeleteAndReadBack(t *testing.T) {
	desired := desiredFixture(t)
	fake := &fakeOpenObserve{
		destination: true,
		streams:     []string{desired.Alerts[0].StreamName},
		alerts: map[string]OpenObserveAlert{
			"obsolete":  {Name: "obsolete_rule", Tags: []string{managedTag}},
			"unmanaged": {Name: "operator_rule", Tags: []string{"owner:operator"}},
		},
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewOpenObserveClient(server.URL, "operator", "secret")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != "drifted" || len(plan.Actions) != 2 || plan.Actions[0].Kind != "create" || plan.Actions[1].Kind != "delete" {
		t.Fatalf("initial plan = %#v", plan)
	}
	verified, err := client.Apply(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if verified.State != "managed" || len(verified.Actions) != 0 {
		t.Fatalf("verified plan = %#v", verified)
	}
	if _, exists := fake.alerts["unmanaged"]; !exists {
		t.Fatal("unmanaged alert was deleted")
	}
	for id, alert := range fake.alerts {
		if alert.Name == desired.Alerts[0].Name {
			alert.Description = "drifted"
			fake.alerts[id] = alert
		}
	}
	drift, err := client.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift.Actions) != 1 || drift.Actions[0].Kind != "update" {
		t.Fatalf("update plan = %#v", drift)
	}
}

// TestMovingAStreamReplacesTheAlertRatherThanEditingIt covers the case that
// could not converge. OpenObserve keeps an existing alert's stream_name through
// a PUT, so planning an update for a rule whose metric moved changes the
// expression and the threshold, leaves the stream behind, and reports the same
// drift on every subsequent run.
//
// Observed live on lifecycle_queued_delivery_stall and queue_wait_slow_burn when
// they moved off the state clock: apply returned "read-back did not converge"
// and replanning produced the identical two updates.
func TestMovingAStreamReplacesTheAlertRatherThanEditingIt(t *testing.T) {
	desired := desiredFixture(t)
	wanted := desired.Alerts[0]
	fake := &fakeOpenObserve{
		destination: true,
		streams:     []string{wanted.StreamName},
		alerts: map[string]OpenObserveAlert{
			"moved": {Name: wanted.Name, StreamName: "gha_fleet_some_previous_series", Tags: []string{managedTag}},
		},
	}
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := NewOpenObserveClient(server.URL, "operator", "secret")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, action := range plan.Actions {
		kinds[action.Kind]++
	}
	if kinds["update"] != 0 || kinds["delete"] != 1 || kinds["create"] != 1 {
		t.Fatalf("a moved stream must be replaced, not edited: %#v", plan.Actions)
	}
	applied, err := client.Apply(context.Background(), desired)
	if err != nil {
		t.Fatalf("apply must converge after a replacement: %v", err)
	}
	if applied.State != "managed" {
		t.Fatalf("state = %q, want managed", applied.State)
	}
}
