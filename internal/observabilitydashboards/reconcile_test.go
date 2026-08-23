package observabilitydashboards

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
)

type fakeDashboardBackend struct {
	mu         sync.Mutex
	next       int
	dashboards map[string]OpenObserveDashboard
	hashes     map[string]string
}

func (f *fakeDashboardBackend) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json")
	base := "/api/default/dashboards"
	if request.URL.Path == base && request.Method == http.MethodGet {
		list := dashboardList{Dashboards: []dashboardSummary{}}
		for id, dashboard := range f.dashboards {
			list.Dashboards = append(list.Dashboards, dashboardSummary{Version: 8, Hash: f.hashes[id], FolderID: "default", DashboardID: id, Title: dashboard.Title, Description: dashboard.Description})
		}
		sort.Slice(list.Dashboards, func(i, j int) bool { return list.Dashboards[i].Title < list.Dashboards[j].Title })
		_ = json.NewEncoder(writer).Encode(list)
		return
	}
	if request.URL.Path == base && request.Method == http.MethodPost {
		var dashboard OpenObserveDashboard
		if err := json.NewDecoder(request.Body).Decode(&dashboard); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		f.next++
		id := fmt.Sprintf("dashboard-%d", f.next)
		dashboard.DashboardID = id
		f.dashboards[id], f.hashes[id] = dashboard, fmt.Sprintf("hash-%d", f.next)
		writer.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(writer).Encode(dashboard)
		return
	}
	id := strings.TrimPrefix(request.URL.Path, base+"/")
	dashboard, exists := f.dashboards[id]
	if !exists {
		http.NotFound(writer, request)
		return
	}
	switch request.Method {
	case http.MethodGet:
		_ = json.NewEncoder(writer).Encode(dashboardEnvelope{Version: 8, Hash: f.hashes[id], V8: &dashboard})
	case http.MethodPut:
		if request.URL.Query().Get("hash") != f.hashes[id] {
			http.Error(writer, "hash conflict", http.StatusConflict)
			return
		}
		var desired OpenObserveDashboard
		if err := json.NewDecoder(request.Body).Decode(&desired); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		desired.DashboardID = id
		f.dashboards[id], f.hashes[id] = desired, f.hashes[id]+"-next"
		_ = json.NewEncoder(writer).Encode(desired)
	case http.MethodDelete:
		delete(f.dashboards, id)
		delete(f.hashes, id)
		writer.WriteHeader(http.StatusNoContent)
	default:
		http.Error(writer, "unsupported", http.StatusMethodNotAllowed)
	}
}

func TestReconcileOpenObserveDashboardsPreservesManualAndConverges(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	desired, err := RenderOpenObserve(bundle)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeDashboardBackend{
		next: 1,
		dashboards: map[string]OpenObserveDashboard{
			"manual": {Version: 8, DashboardID: "manual", Title: "Operator dashboard", Description: "not managed"},
			"stale":  {Version: 8, DashboardID: "stale", Title: "Retired fleet view", Description: managedDescriptionPrefix + "id:retired"},
		},
		hashes: map[string]string{"manual": "manual-hash", "stale": "stale-hash"},
	}
	server := httptest.NewServer(backend)
	defer server.Close()
	client, err := NewOpenObserveClient(server.URL, "operator", "secret")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := client.Plan(t.Context(), "default", "default", desired)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != "drifted" || len(plan.Actions) != len(desired)+1 {
		t.Fatalf("unexpected plan %#v", plan)
	}
	verified, err := client.Apply(t.Context(), "default", "default", desired)
	if err != nil {
		t.Fatal(err)
	}
	if verified.State != "managed" || len(verified.Actions) != 0 {
		t.Fatalf("unexpected verified plan %#v", verified)
	}
	if _, exists := backend.dashboards["manual"]; !exists {
		t.Fatal("manual dashboard was removed")
	}
	if _, exists := backend.dashboards["stale"]; exists {
		t.Fatal("stale managed dashboard was preserved")
	}

	for id, dashboard := range backend.dashboards {
		if dashboard.Title == desired[0].Title {
			dashboard.Tabs[0].Panels[0].Title = "drifted"
			backend.dashboards[id] = dashboard
			break
		}
	}
	plan, err = client.Plan(t.Context(), "default", "default", desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "update" || plan.Actions[0].Hash == "" {
		t.Fatalf("drift update is not hash-bound: %#v", plan)
	}
}
