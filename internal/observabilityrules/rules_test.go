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
	if len(bundle.Rules) != 11 {
		t.Fatalf("rules = %d, want 11", len(bundle.Rules))
	}
}

func TestRepositoryRulesUseCurrentMetricSemantics(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]string{
		"memory_psi_slow_burn": `window_seconds="10"`,
		"queue_wait_slow_burn": `gha_fleet_queue_intent_oldest_state_age_seconds{state="queued"}`,
	}
	seen := make(map[string]bool, len(wanted))
	for _, rule := range bundle.Rules {
		fragment, exists := wanted[rule.ID]
		if exists && !strings.Contains(rule.Query, fragment) {
			t.Errorf("rule %s query %q does not contain current metric contract %q", rule.ID, rule.Query, fragment)
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
