package observabilityrules

import (
	"strings"
	"testing"
)

func TestRepositoryBundleIsValid(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Rules) != 18 {
		t.Fatalf("rules = %d, want 18", len(bundle.Rules))
	}
}

func TestRepositoryRulesUseCurrentMetricSemantics(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]string{
		"github_correlation_persistent":     "gha_fleet_queue_missing_workflow_run_id_beyond_grace",
		"lifecycle_queued_delivery_stall":   `gha_fleet_queue_intent_oldest_state_age_seconds{state="queued"}`,
		"memory_psi_slow_burn":              `window_seconds="10"`,
		"queue_wait_slow_burn":              `gha_fleet_queue_intent_oldest_state_age_seconds{state="queued"}`,
		"provider_retry_error_persistent":   `gha_fleet_provider_retry_deferred_records_by_error_class{error_class=~"identity|intent|provider|timeout|unknown"}`,
		"compute_pressure_observer_missing": `count(up{service_name="pressure-state"} == 1)`,
		"compute_pressure_state_stale":      "gha_fleet_pressure_observer_up",
		"compute_root_disk_low":             "system_filesystem_usage",
	}
	seen := make(map[string]bool, len(wanted))
	for _, rule := range bundle.Rules {
		fragment, exists := wanted[rule.ID]
		if exists && !strings.Contains(rule.Expression, fragment) {
			t.Errorf("rule %s expression %q does not contain current metric contract %q", rule.ID, rule.Expression, fragment)
		}
		seen[rule.ID] = exists
	}
	for id := range wanted {
		if !seen[id] {
			t.Errorf("required semantic rule %s is missing", id)
		}
	}
}

func TestRulesRejectUnsafeOrUnactionableChanges(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Rule){
		"unknown severity": func(r *Rule) { r.Severity = "noise" },
		"slow page":        func(r *Rule) { r.HoldSecs = 3600 },
		"fast ticket":      func(r *Rule) { r.Severity, r.HoldSecs = "ticket", 60 },
		"private runbook":  func(r *Rule) { r.Runbook = "https://example.invalid/private" },
		"unknown operator": func(r *Rule) { r.Operator = "contains" },
	} {
		t.Run(name, func(t *testing.T) {
			mutated := bundle
			mutated.Rules = append([]Rule(nil), bundle.Rules...)
			mutate(&mutated.Rules[0])
			if err := mutated.Validate(); err == nil {
				t.Fatal("unsafe rule was accepted")
			}
		})
	}
}

func TestRenderOpenObserveSeparatesExpressionAndThreshold(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(rendered.Alerts) != len(bundle.Rules) {
		t.Fatalf("alerts = %d, want %d", len(rendered.Alerts), len(bundle.Rules))
	}
	for index, alert := range rendered.Alerts {
		rule := bundle.Rules[index]
		if alert.Name != rule.ID || alert.StreamName != rule.StreamName || alert.QueryCondition.PromQL != rule.Expression ||
			alert.QueryCondition.PromQLCondition.Operator != rule.Operator ||
			alert.QueryCondition.PromQLCondition.Value != rule.Threshold || !alert.Enabled {
			t.Fatalf("alert %s does not preserve rule semantics: %#v", rule.ID, alert)
		}
		if alert.TriggerCondition.Threshold != rule.RequiredEvaluations() {
			t.Fatalf("alert %s evaluations = %d, want %d", rule.ID, alert.TriggerCondition.Threshold, rule.RequiredEvaluations())
		}
	}
}

func TestRenderOpenObserveConvertsSecondsToMinuteSchedule(t *testing.T) {
	for _, test := range []struct {
		name             string
		severity         string
		evaluationSecs   int
		holdSecs         int
		frequencyMinutes int
		periodMinutes    int
		evaluations      int
	}{
		{name: "thirty seconds", severity: "page", evaluationSecs: 30, holdSecs: 120, frequencyMinutes: 1, periodMinutes: 2, evaluations: 2},
		{name: "sixty seconds", severity: "page", evaluationSecs: 60, holdSecs: 60, frequencyMinutes: 1, periodMinutes: 1, evaluations: 1},
		{name: "five minutes", severity: "ticket", evaluationSecs: 300, holdSecs: 900, frequencyMinutes: 5, periodMinutes: 15, evaluations: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle := Bundle{SchemaVersion: SchemaVersion, Backend: "openobserve", Organization: "default", Rules: []Rule{{
				ID: "example_rule", Severity: test.severity, QueryLanguage: "promql", StreamName: "example_metric",
				Expression: "max(example_metric)", Operator: ">", Threshold: 0,
				EvaluationSecs: test.evaluationSecs, HoldSecs: test.holdSecs, DestinationRef: "fleet_oncall",
				Owner: "fleet-operations", Runbook: "https://github.com/NDDev-OpenNetwork/github-actions/blob/main/docs/runbooks/fleet-alerts.md",
				Summary: "Example summary.", Action: "Example action.", Recovery: "Example recovery.",
			}}}
			rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
			if err != nil {
				t.Fatal(err)
			}
			trigger := rendered.Alerts[0].TriggerCondition
			if trigger.FrequencyType != "minutes" || trigger.Frequency != test.frequencyMinutes ||
				trigger.Period != test.periodMinutes || trigger.Threshold != test.evaluations {
				t.Fatalf("trigger = %#v", trigger)
			}
		})
	}
}
