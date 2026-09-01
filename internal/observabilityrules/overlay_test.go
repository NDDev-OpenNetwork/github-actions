package observabilityrules

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tenantSQLRule is the overlay shape the estate ships: a tenant-supplied SQL
// alert over a logs stream, with the condition in the statement and the
// trigger gating result rows.
func tenantSQLRule(id string) Rule {
	return Rule{
		ID:             id,
		Severity:       "ticket",
		QueryLanguage:  "sql",
		StreamName:     "anton_logs",
		Expression:     `SELECT 'server-anton-kz' AS subject, count(*) AS value FROM "anton_logs" HAVING count(*) < 1`,
		Operator:       ">=",
		Threshold:      1,
		EvaluationSecs: 300,
		HoldSecs:       1800,
		DestinationRef: "fleet_oncall",
		Enabled:        true,
		Owner:          "server-anton-kz",
		Runbook:        "https://github.com/NDDev-OpenNetwork/github-actions/blob/main/docs/tenant-observability-overlay.md",
		Summary:        "tenant telemetry stream is silent",
		Action:         "check the tenant host's collector and network path",
		Recovery:       "records arriving again clears the condition",
	}
}

func overlayBundle(rules ...Rule) Bundle {
	return Bundle{SchemaVersion: SchemaVersion, Backend: "openobserve", Organization: "default", Rules: rules}
}

func TestMergeUnionsSortedAndRefusesCollisions(t *testing.T) {
	base, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	merged, err := base.Merge(overlayBundle(tenantSQLRule("anton_telemetry_stale")))
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rules) != len(base.Rules)+1 {
		t.Fatalf("merged rules = %d, want %d", len(merged.Rules), len(base.Rules)+1)
	}
	if err := merged.Validate(); err != nil {
		t.Fatalf("merged bundle does not validate: %v", err)
	}

	// An overlay adds, it never redefines: colliding with any base id refuses.
	collision := overlayBundle(tenantSQLRule(base.Rules[0].ID))
	collision.Rules[0].StreamName = "anton_logs"
	if _, err := base.Merge(collision); err == nil {
		t.Fatal("an overlay redefining a base rule id was accepted")
	}

	// A bundle with a different identity is not an overlay of this one.
	foreign := overlayBundle(tenantSQLRule("anton_telemetry_stale"))
	foreign.Organization = "tenant"
	if _, err := base.Merge(foreign); err == nil {
		t.Fatal("an overlay with a foreign organization was accepted")
	}
}

func TestLoadWithOverlaysMergesTheEstateFile(t *testing.T) {
	dir := t.TempDir()
	overlayPath := filepath.Join(dir, "estate-overlay.yaml")
	overlay := `schema_version: 2
backend: openobserve
organization: default
rules:
  - id: anton_telemetry_stale
    severity: ticket
    query_language: sql
    stream_name: anton_logs
    expression: SELECT 'server-anton-kz' AS subject, count(*) AS value FROM "anton_logs" HAVING count(*) < 1
    operator: ">="
    threshold: 1
    evaluation_seconds: 300
    hold_seconds: 1800
    destination_ref: fleet_oncall
    enabled: true
    owner: server-anton-kz
    runbook: https://github.com/NDDev-OpenNetwork/github-actions/blob/main/docs/tenant-observability-overlay.md
    summary: tenant telemetry stream is silent
    action: check the tenant host's collector and network path
    recovery: records arriving again clears the condition
`
	if err := os.WriteFile(overlayPath, []byte(overlay), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := LoadWithOverlays("../../config/observability-rules.yaml", []string{overlayPath})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, rule := range merged.Rules {
		if rule.ID == "anton_telemetry_stale" {
			found = true
		}
	}
	if !found {
		t.Fatal("overlay rule missing from the merged bundle")
	}
	if _, err := LoadWithOverlays("../../config/observability-rules.yaml", nil); err != nil {
		t.Fatalf("no-overlay load must behave exactly like Load: %v", err)
	}
}

func TestSQLRuleRendersAsLogsAlert(t *testing.T) {
	bundle := overlayBundle(tenantSQLRule("anton_telemetry_stale"))
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	alert := rendered.Alerts[0]
	if alert.StreamType != "logs" {
		t.Fatalf("stream type = %q, want logs", alert.StreamType)
	}
	if alert.QueryCondition.Type != "sql" || !strings.Contains(alert.QueryCondition.SQL, "HAVING") {
		t.Fatalf("query condition does not carry the sql statement: %#v", alert.QueryCondition)
	}
	if alert.QueryCondition.PromQL != "" {
		t.Fatalf("sql alert must not carry promql: %q", alert.QueryCondition.PromQL)
	}
	// For sql the trigger gates result rows with the rule's own operator and
	// threshold; the promql fixed coverage gate does not apply.
	if alert.TriggerCondition.Operator != ">=" || alert.TriggerCondition.Threshold != 1 {
		t.Fatalf("trigger = %q %d, want >= 1", alert.TriggerCondition.Operator, alert.TriggerCondition.Threshold)
	}
	if alert.TriggerCondition.Period != 30 || alert.TriggerCondition.Frequency != 5 {
		t.Fatalf("schedule = period %d frequency %d, want 30/5", alert.TriggerCondition.Period, alert.TriggerCondition.Frequency)
	}
}

func TestSQLRuleValidationRejectsUnsafeShapes(t *testing.T) {
	for name, mutate := range map[string]func(*Rule){
		"compound statement":  func(r *Rule) { r.Expression = "SELECT 1; DROP TABLE x" },
		"fractional row gate": func(r *Rule) { r.Threshold = 1.5 },
		"negative row gate":   func(r *Rule) { r.Threshold = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			rule := tenantSQLRule("anton_telemetry_stale")
			mutate(&rule)
			if err := rule.Validate(); err == nil {
				t.Fatal("unsafe sql rule was accepted")
			}
		})
	}
}

func TestReconcilePlanChecksLogsStreamsForSQLAlerts(t *testing.T) {
	bundle := overlayBundle(tenantSQLRule("anton_telemetry_stale"))
	desired, err := RenderOpenObserve(bundle, "fleet_oncall", false)
	if err != nil {
		t.Fatal(err)
	}

	// The stream exists as a logs stream and only there: a type-blind check
	// against the metrics namespace would block this plan forever.
	fake := &fakeOpenObserve{
		destination:   true,
		streamsByType: map[string][]string{"logs": {"anton_logs"}},
		alerts:        map[string]OpenObserveAlert{},
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
	if plan.State == "blocked" || len(plan.MissingStreams) != 0 {
		t.Fatalf("plan blocked: state %q missing %v", plan.State, plan.MissingStreams)
	}
	if len(plan.Actions) != 1 || plan.Actions[0].Kind != "create" {
		t.Fatalf("actions = %#v, want one create", plan.Actions)
	}

	// The same stream absent from the logs namespace blocks the plan even if a
	// metrics stream shares the name.
	fake.streamsByType = map[string][]string{"metrics": {"anton_logs"}}
	blocked, err := client.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if blocked.State != "blocked" || len(blocked.MissingStreams) != 1 {
		t.Fatalf("plan = %q missing %v, want blocked on the logs stream", blocked.State, blocked.MissingStreams)
	}
}

func TestReconcileRequestsOnlyNeededStreamTypes(t *testing.T) {
	// A purely promql bundle keeps asking for metrics alone; the fake with a
	// typed map returns nothing for other types, so a stray logs request would
	// block the plan and fail this test.
	desired := desiredFixture(t)
	fake := &fakeOpenObserve{
		destination:   true,
		streamsByType: map[string][]string{"metrics": {desired.Alerts[0].StreamName}},
		alerts:        map[string]OpenObserveAlert{},
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
	if plan.State == "blocked" {
		t.Fatalf("promql-only plan blocked on %v: the metrics namespace was not consulted", plan.MissingStreams)
	}
}
