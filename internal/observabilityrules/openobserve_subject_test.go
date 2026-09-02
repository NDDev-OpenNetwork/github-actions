package observabilityrules

import (
	"encoding/json"
	"strings"
	"testing"
)

// A collapsed aggregation has no subject to name; anything that keeps labels
// does. This is the whole difference between a message that says which host is
// out of disk and one that says "Condition: 1 >= 1".
func TestNamesASubject(t *testing.T) {
	for expression, want := range map[string]bool{
		"max(gha_fleet_host_reboot_required)":                 false,
		"max by (host_name) (gha_fleet_host_reboot_required)": true,
		"max by(host_name)(gha_fleet_host_reboot_required)":   true,
		"count(up{service_name=\"pressure-state\"} == 1)":     false,
		"max(a) + max(b)": false,
		"max_over_time(gha_fleet_host_signal_events{signal_class=\"x\"}[15m])": true,
		"sum_over_time(gha_fleet_observability_signal_events{a=\"b\"}[1h])":    true,
		"sum by (error_class) (gha_fleet_provider_retry_deferred_records)":     true,
		"sum by (host_name) (free) / sum by (host_name) (total)":               true,
		"min(sum by (host_name) (free))":                                       true,
	} {
		if got := namesASubject(expression); got != want {
			t.Errorf("namesASubject(%q)=%v, want %v", expression, got, want)
		}
	}
}

// The shipped bundle is the thing that matters: a rule whose expression keeps a
// label must reach the backend with multi-alert on, or the label is carried and
// never rendered.
func TestShippedRulesCarryTheirSubject(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	subjects := 0
	for _, alert := range rendered.Alerts {
		if alert.QueryCondition.Type != "promql" {
			continue
		}
		want := namesASubject(alert.QueryCondition.PromQL)
		if alert.QueryCondition.PromQLMultiAlert != want {
			t.Errorf("%s multi_alert=%v, want %v for %q",
				alert.Name, alert.QueryCondition.PromQLMultiAlert, want, alert.QueryCondition.PromQL)
		}
		if want {
			subjects++
		}
	}
	// Before this, three of twenty-nine rules kept a label. A regression that
	// collapses them again must fail here rather than in a chat message that
	// says nothing.
	if subjects < 20 {
		t.Errorf("only %d shipped rules name a subject; the fleet has more than that", subjects)
	}
}

func TestNotificationValidate(t *testing.T) {
	good := Notification{
		Template: "fleet_oncall_telegram",
		Body:     `{"chat_id":"1","text":"alert {alert_name} value {alert_agg_value}"}`,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid notification rejected: %v", err)
	}
	for name, broken := range map[string]Notification{
		"bad id":        {Template: "Fleet Oncall", Body: good.Body},
		"short body":    {Template: good.Template, Body: `{"a":"{alert_name}"}`},
		"invalid json":  {Template: good.Template, Body: `{"chat_id":"1","text":"alert {alert_name} value {alert_agg_value}"`},
		"unnamed alert": {Template: good.Template, Body: `{"chat_id":"1","text":"something happened somewhere at some point"}`},
	} {
		if err := broken.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// An overlay adds, it never redefines -- the same rule the ids already follow.
func TestOverlayNotificationDoesNotRedefine(t *testing.T) {
	base := Bundle{
		SchemaVersion: SchemaVersion, Backend: "openobserve", Organization: "default",
		Notification: &Notification{Template: "a_template", Body: `{"text":"{alert_name} fired somewhere"}`},
		Rules:        []Rule{sampleRule("aaa_rule")},
	}
	overlay := Bundle{
		SchemaVersion: SchemaVersion, Backend: "openobserve", Organization: "default",
		Notification: &Notification{Template: "b_template", Body: `{"text":"{alert_name} fired somewhere"}`},
		Rules:        []Rule{sampleRule("bbb_rule")},
	}
	if _, err := base.Merge(overlay); err == nil {
		t.Fatal("colliding notification was accepted")
	}
	base.Notification = nil
	merged, err := base.Merge(overlay)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Notification == nil || merged.Notification.Template != "b_template" {
		t.Fatalf("overlay notification was not adopted: %#v", merged.Notification)
	}
}

func sampleRule(id string) Rule {
	return Rule{
		ID: id, Severity: "ticket", QueryLanguage: "promql", StreamName: "some_metric",
		Expression: "max by (host_name) (some_metric)", Operator: ">", Threshold: 0,
		EvaluationSecs: 300, HoldSecs: 900, DestinationRef: "fleet_oncall", Enabled: true,
		Owner: "fleet-operations", Summary: "s", Action: "a", Recovery: "r",
		Runbook: "https://github.com/NDDev-OpenNetwork/github-actions/blob/main/docs/runbooks/fleet-alerts.md",
	}
}

// The body reaches the destination verbatim, so it has to survive a JSON round
// trip with the placeholders intact.
func TestNotificationBodySurvivesEncoding(t *testing.T) {
	body := `{"chat_id":"1","text":"{alert_name}\n{alert_agg_value} {alert_operator} {alert_threshold}"}`
	encoded, err := json.Marshal(Notification{Template: "t_template", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "{alert_agg_value}") {
		t.Fatalf("placeholders did not survive encoding: %s", encoded)
	}
}

// Thirty-four alerts must not evaluate in the same millisecond. OpenObserve
// snaps an aligned alert's next run to its frequency boundary, so alignment
// puts every rule of a given frequency on the same tick: on 2026-09-02 the
// searches queued until some exceeded the PromQL load-data timeout and the
// group-state writes queued behind the single SQLite writer until they were
// refused 2386 times, and a scheduler that cannot persist that it notified
// notifies again.
func TestRenderedAlertsDoNotShareOneSchedulingTick(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Alerts) < 20 {
		t.Fatalf("only %d alerts rendered; the check proves nothing", len(rendered.Alerts))
	}
	for _, alert := range rendered.Alerts {
		if alert.TriggerCondition.AlignTime {
			t.Fatalf("alert %q is aligned to the wall clock and will evaluate in the herd", alert.Name)
		}
	}
}
