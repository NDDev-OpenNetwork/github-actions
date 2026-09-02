package observabilityrules

import (
	"strconv"
	"strings"
	"testing"
)

func TestRepositoryBundleIsValid(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Rules) != 31 {
		t.Fatalf("rules = %d, want 31", len(bundle.Rules))
	}
}

func TestRepositoryRulesUseCurrentMetricSemantics(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]string{
		"github_correlation_persistent":     "gha_fleet_queue_missing_workflow_run_id_beyond_grace",
		"lifecycle_queued_delivery_stall":   "gha_fleet_queue_oldest_queued_wait_seconds_by_scale_set",
		"memory_psi_slow_burn":              `window_seconds="10"`,
		"queue_wait_slow_burn":              "gha_fleet_queue_oldest_queued_wait_seconds_by_scale_set",
		"provider_retry_error_persistent":   `gha_fleet_provider_retry_deferred_records_by_error_class{error_class=~"identity|intent|provider|timeout|unknown"}`,
		"compute_pressure_observer_missing": `count(up{service_name="pressure-state"} == 1)`,
		"compute_pressure_state_stale":      "gha_fleet_pressure_observer_up",
		"compute_root_disk_low":             "system_filesystem_usage",
		"kernel_slab_unreclaimable":         "gha_fleet_host_slab_unreclaimable_attributed_bytes",
		"audit_suppression_burst":           `signal_class="audit_suppressed"`,
		"kernel_workqueue_hog":              `signal_class="kernel_workqueue_hog"`,
		"host_compliance_observer_missing":  "gha_fleet_host_compliance_observer_up",
		"host_package_inventory_stale":      "gha_fleet_host_package_inventory_age_seconds",
		"host_reboot_required":              "gha_fleet_host_reboot_required",
		"host_standard_updates_available":   "gha_fleet_host_standard_updates_available",
		"host_swap_high":                    `state="used"`,
		"host_swap_thrash":                  `type="major"`,
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

func TestDiagnosticExporterPageRequiresSustainedFailure(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range bundle.Rules {
		if rule.ID == "diagnostic_export_failure" {
			rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
			if err != nil {
				t.Fatal(err)
			}
			for _, alert := range rendered.Alerts {
				if alert.Name == rule.ID && (rule.HoldSecs != 180 || alert.TriggerCondition.Threshold != 1 ||
					!strings.HasPrefix(withoutSubject(alert.QueryCondition.PromQL), "min_over_time(")) {
					t.Fatalf("diagnostic rule=%#v alert=%#v", rule, alert)
				}
			}
			return
		}
	}
	t.Fatal("diagnostic_export_failure rule is missing")
}

// The host-signal slow burns must keep a per-host vector all the way through
// the sustained subquery: collapsing them with max() before the subquery is
// what produced OpenObserve error 20008, "the return value should have been a
// matrix but got scalar".
//
// This test used to also require `or vector(0)` on both, as a fallback for a
// window with no events. That requirement was the defect: `or` evaluates to an
// empty result on this backend regardless of its left side, so the fallback
// blinded both alerts on every window, and a test asserting the form could not
// tell the difference. It now asserts the behaviour that matters and forbids
// the operator that broke it.
func TestHostSignalSlowBurnsRemainVectorsForOpenObserveSubqueries(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"audit_suppression_burst", "kernel_workqueue_hog"} {
		found := false
		for _, alert := range rendered.Alerts {
			if alert.Name != id {
				continue
			}
			found = true
			if strings.Contains(alert.QueryCondition.PromQL, "((max(max_over_time(") {
				t.Fatalf("%s collapses its host series to a scalar before the sustained subquery: %s", id, alert.QueryCondition.PromQL)
			}
			if strings.Contains(alert.QueryCondition.PromQL, " or ") {
				t.Fatalf("%s cannot fire: the backend evaluates `or` to an empty result: %s", id, alert.QueryCondition.PromQL)
			}
			// No outer subquery at all: an empty inner vector inside one is
			// error 20008, which records outcome 3 and notifies nobody. The
			// windowed delta is already the sustained statement.
			if strings.Contains(alert.QueryCondition.PromQL, "min_over_time((max_over_time(") {
				t.Fatalf("%s still wraps its windowed delta in a subquery a quiet window cannot satisfy: %s", id, alert.QueryCondition.PromQL)
			}
			// The windowed delta stays the whole statement, and it is summed
			// by host_name: the signal-event streams carry a start_time label
			// that changes with each counter run, so an unaggregated delta
			// measures one run instead of the host and splits the alert into
			// one dispatch per run (2026-09-02: sixteen failures arrived as
			// three series of 4, 4 and 3). Summing by host keeps the subject
			// the notification prints and cannot collapse it to a scalar.
			body := withoutSubject(alert.QueryCondition.PromQL)
			if !strings.HasPrefix(body, "sum by (host_name) (max_over_time(") {
				t.Fatalf("%s is not the windowed delta summed by host: %s", id, alert.QueryCondition.PromQL)
			}
			if !strings.Contains(body, "- min_over_time(") {
				t.Fatalf("%s lost the min_over_time half of its delta: %s", id, alert.QueryCondition.PromQL)
			}
		}
		if !found {
			t.Fatalf("%s alert is missing", id)
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
		"slow page":        func(r *Rule) { r.Severity, r.HoldSecs = "page", 3600 },
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
		if alert.Name != rule.ID || alert.StreamName != rule.StreamName ||
			alert.QueryCondition.PromQLCondition.Operator != rule.Operator ||
			alert.QueryCondition.PromQLCondition.Value != rule.Threshold || !alert.Enabled {
			t.Fatalf("alert %s does not preserve rule semantics: %#v", rule.ID, alert)
		}
		if alert.TriggerCondition.Threshold != 1 {
			t.Fatalf("alert %s coverage threshold = %d, want 1", rule.ID, alert.TriggerCondition.Threshold)
		}
		if rule.HoldSecs > rule.EvaluationSecs && !strings.Contains(alert.QueryCondition.PromQL, "_over_time(") {
			t.Fatalf("alert %s lacks sustained range query: %q", rule.ID, alert.QueryCondition.PromQL)
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
		coverage         int
	}{
		{name: "thirty seconds", severity: "page", evaluationSecs: 30, holdSecs: 120, frequencyMinutes: 1, periodMinutes: 2, coverage: 1},
		{name: "sixty seconds", severity: "page", evaluationSecs: 60, holdSecs: 60, frequencyMinutes: 1, periodMinutes: 1, coverage: 1},
		{name: "five minutes", severity: "ticket", evaluationSecs: 300, holdSecs: 900, frequencyMinutes: 5, periodMinutes: 15, coverage: 1},
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
				trigger.Period != test.periodMinutes || trigger.Threshold != test.coverage {
				t.Fatalf("trigger = %#v", trigger)
			}
		})
	}
}

// Recovery is observed at most one silence window late, because OpenObserve
// pauses outcome evaluation while silenced. This asserts that bound, which is
// what the name promises -- not the two constants that used to be the whole
// policy. Asserting the constants would have made every deliberate cadence a
// test failure and left the flood in place.
//
// It also matches by name rather than by index: RenderOpenObserve sorts its
// output, so the positional comparison this replaces was only ever correct by
// accident of the bundle already being alphabetical.
func TestRenderOpenObserveBoundsStaleRecoveryDuringSilence(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Rule, len(bundle.Rules))
	for _, rule := range bundle.Rules {
		byID[rule.ID] = rule
	}
	if len(rendered.Alerts) != len(byID) {
		t.Fatalf("rendered %d alerts from %d rules", len(rendered.Alerts), len(byID))
	}
	for _, alert := range rendered.Alerts {
		rule, exists := byID[alert.Name]
		if !exists {
			t.Fatalf("rendered alert %s has no rule", alert.Name)
		}
		want := repeatOrDefault(rule) / 60
		if alert.TriggerCondition.Silence != want {
			t.Errorf("alert %s silence=%d minutes, want %d from its declared cadence", alert.Name, alert.TriggerCondition.Silence, want)
		}
		// The bound itself: a page is re-stated within the hour, and nothing
		// goes quiet for more than a day.
		if rule.Severity == "page" && alert.TriggerCondition.Silence > 60 {
			t.Errorf("page %s would observe recovery up to %d minutes late", alert.Name, alert.TriggerCondition.Silence)
		}
		if alert.TriggerCondition.Silence > 1440 {
			t.Errorf("alert %s would observe recovery up to %d minutes late", alert.Name, alert.TriggerCondition.Silence)
		}
	}
}

// queue_wait_slow_burn and lifecycle_queued_delivery_stall read the identical
// series. Before this bound, any queued age past 300 seconds satisfied both --
// a ticket at > 120 sustained fifteen minutes and a page at > 300 sustained
// five -- so one stall produced two messages on two cadences. Over seven days
// the pair accounted for 43 of 115 firing transitions.
//
// The two rules must now partition the range rather than overlap on it.
func TestQueueWaitRulesPartitionTheSameSeries(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var burn, page *Rule
	for i := range bundle.Rules {
		switch bundle.Rules[i].ID {
		case "queue_wait_slow_burn":
			burn = &bundle.Rules[i]
		case "lifecycle_queued_delivery_stall":
			page = &bundle.Rules[i]
		}
	}
	if burn == nil || page == nil {
		t.Fatal("both queued-age rules must exist; they are the pair being partitioned")
	}
	if page.Severity != "page" || burn.Severity != "ticket" {
		t.Fatalf("severities changed: burn=%q page=%q", burn.Severity, page.Severity)
	}
	// The bound must name the page's own threshold, so moving one moves both
	// or fails here rather than silently reopening the overlap.
	bound := "< bool " + strconv.FormatFloat(page.Threshold, 'f', -1, 64)
	if !strings.Contains(burn.Expression, bound) {
		t.Errorf("the ticket bound %q does not track the page threshold %v", burn.Expression, page.Threshold)
	}
	// The gate has to be the bool-modified product, not the obvious filter.
	// Measured against the live backend, `vector(200) < 300` returns an empty
	// result and `(vector(200) < 300) or vector(0)` returns empty too, so a
	// filtered expression would make this rule report an evaluation error --
	// which notifies nobody -- instead of "not firing".
	if strings.Contains(burn.Expression, "or vector(") {
		t.Errorf("the ticket relies on an or-fallthrough this backend does not evaluate: %q", burn.Expression)
	}
	// The gate is a bool-modified product of the rule's own series with itself,
	// so above the page threshold the ticket evaluates to zero rather than to
	// an empty result. Checked structurally rather than by pinning the literal
	// "* (max(": the aggregation now keeps `by (scale_set)` so the message can
	// say which scale set is waiting, and a textual pin turned that into a
	// failure about a form instead of about the invariant.
	factor, gate, split := strings.Cut(burn.Expression, " * (")
	if !split || !strings.HasSuffix(strings.TrimSpace(gate), ")") {
		t.Fatalf("the ticket is not a bool-modified product: %q", burn.Expression)
	}
	masked, _, hasBound := strings.Cut(gate, " < bool ")
	if !hasBound {
		t.Fatalf("the ticket product has no bool bound: %q", burn.Expression)
	}
	if strings.TrimSpace(factor) != strings.TrimSpace(masked) {
		t.Errorf("the ticket gates a different series than it reports: %q against %q", factor, masked)
	}
}

// Every PromQL set operator returns an empty result on the backend these rules
// run against, whether or not its operands have data. Measured against the
// live engine with two series that plainly read 1 and 0:
//
//	max(gha_fleet_platform_healthy)                     -> 1
//	max(gha_fleet_incus_orphan_instances)               -> 0
//	max(A) or  max(B)                                   -> empty
//	max(A) and max(B)                                   -> empty
//	max(A) unless max(B)                                -> empty
//	max(A) and on() max(B)                              -> empty
//	max(gha_fleet_platform_healthy) or vector(0)        -> empty
//
// They do not fail to default an absence; they annihilate the expression. An
// empty result satisfies no threshold, so an expression containing any of them
// cannot fire -- and it looks exactly like an alert that is simply not
// breaching, which is why this went unnoticed for two days.
//
// Arithmetic does work, including the bool modifier, so a conjunction is
// expressible without a set operator: `(a > bool 300) + (b < bool 1)` returns
// a correct vector on the same build. Four rules carried one -- audit_suppression_burst,
// kernel_workqueue_hog, github_correlation_persistent and the
// lifecycle_inventory_gap page -- and none of them had fired since the
// 2026-08-27 22:09 rewrite that introduced the last two of them, while the
// rollout that shipped it recorded `evaluation_errors_after: 0` and
// `all_alerts_normal: true`, which is precisely what a blinded alert looks
// like.
//
// Non-negative counters compared against zero say the same thing with `+`, and
// a window with no events says it with an empty result, which is honest and
// does not fire.
func TestNoRuleDependsOnASetOperatorTheBackendDiscards(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range bundle.Rules {
		for _, operator := range []string{" or ", " and ", " unless "} {
			if strings.Contains(rule.Expression, operator) {
				t.Errorf("rule %s cannot fire: its expression uses%qwhich this backend evaluates to an empty result: %q", rule.ID, operator, rule.Expression)
			}
		}
	}
}

// The whole repeat policy used to be two constants: ten minutes for a page,
// fifteen for a ticket. Measured over 103 hours of recorded transitions, that
// sent 352 messages from 116 episodes -- 82 a day -- and half of them were two
// conditions that cannot clear without a person. kernel_slab_unreclaimable held
// a single alerting episode for 23.6 hours and announced it 95 times.
//
// A rule that says how often its own standing fact should be repeated is the
// fix; these assert the bound rather than the value, so retuning one is not a
// test change but removing the policy is.
func TestStandingConditionsDoNotRepeatAtPageCadence(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	// Conditions that end only when a person patches, reboots or reruns
	// something. Nothing about the fleet's own behaviour clears them.
	humanCleared := map[string]bool{
		"kernel_slab_unreclaimable":       true,
		"host_standard_updates_available": true,
		"host_reboot_required":            true,
		"host_package_inventory_stale":    true,
	}
	seen := 0
	for _, rule := range bundle.Rules {
		if !humanCleared[rule.ID] {
			continue
		}
		seen++
		if rule.RepeatSecs < 3600 {
			t.Errorf("%s repeats every %ds; it cannot clear without maintenance, so that is a message every few minutes for a fact that will not change",
				rule.ID, repeatOrDefault(rule))
		}
		if rule.Severity != "ticket" {
			t.Errorf("%s is severity %q; a condition only a person can clear is not a page", rule.ID, rule.Severity)
		}
	}
	if seen != len(humanCleared) {
		t.Fatalf("expected %d human-cleared rules, found %d", len(humanCleared), seen)
	}
}

// The counter-check: every other rule keeps the short default, so widening the
// cadence stays a deliberate per-rule statement rather than a global retreat
// from alerting.
func TestSelfClearingRulesKeepTheShortDefault(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	widened := 0
	for _, rule := range bundle.Rules {
		if rule.RepeatSecs > 0 {
			widened++
		}
		if rule.Severity == "page" && rule.RepeatSecs > 3600 {
			t.Errorf("page %s stays quiet for %ds", rule.ID, rule.RepeatSecs)
		}
	}
	if widened > 6 {
		t.Errorf("%d rules have widened their repeat cadence; that is a policy change, not four exceptions", widened)
	}
}

func repeatOrDefault(rule Rule) int {
	if rule.RepeatSecs > 0 {
		return rule.RepeatSecs
	}
	if rule.Severity == "page" {
		return 600
	}
	return 900
}

// TestDisabledRuleStaysDisabledWhateverTheFlag pins the half that was missing.
// The reconcile flag used to decide Enabled alone, which made the per-rule field
// decorative: every rule in the bundle read `enabled: false` while all 28 were
// live in OpenObserve and ten of them were paging. A reader checking whether an
// alert was armed got the wrong answer from the document that looked like the
// authority.
func TestDisabledRuleStaysDisabledWhateverTheFlag(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Rules) == 0 {
		t.Fatal("bundle has no rules; this test would be vacuous")
	}
	bundle.Rules[0].Enabled = false
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	target := bundle.Rules[0].ID
	found := false
	for _, alert := range rendered.Alerts {
		if alert.Name != target {
			continue
		}
		found = true
		if alert.Enabled {
			t.Fatalf("rule %q declares enabled: false and rendered enabled anyway", target)
		}
	}
	if !found {
		t.Fatalf("rule %q was not rendered at all", target)
	}
}

// TestFlagDisablesEveryRule keeps the flag a kill switch rather than a no-op:
// an operator reconciling without --enable must be able to arm nothing, however
// the file reads.
func TestFlagDisablesEveryRule(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, alert := range rendered.Alerts {
		if alert.Enabled {
			t.Fatalf("alert %q is enabled with the flag off", alert.Name)
		}
	}
}

// withoutSubject strips the subject label the renderer adds around every
// PromQL alert, so tests about an expression's shape read the expression.
func withoutSubject(promQL string) string {
	for _, prefix := range []string{"label_join(", "label_replace("} {
		if strings.HasPrefix(promQL, prefix) {
			inner := strings.TrimPrefix(promQL, prefix)
			if index := strings.LastIndex(inner, `, "subject"`); index >= 0 {
				return inner[:index]
			}
		}
	}
	return promQL
}

// Every rendered PromQL alert carries a `subject` label: the label the rule
// keeps when it names one, the literal fleet when it aggregates the fleet.
// The message template prints exactly that label, so an alert without it
// would print the placeholder, which is what happened before this existed.
func TestEveryPromQLAlertCarriesASubjectLabel(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle, "fleet_oncall", true)
	if err != nil {
		t.Fatal(err)
	}
	grouped, scalar := 0, 0
	for _, alert := range rendered.Alerts {
		if alert.QueryCondition.Type != "promql" {
			continue
		}
		promQL := alert.QueryCondition.PromQL
		switch {
		case strings.HasPrefix(promQL, "label_join(") && strings.HasSuffix(promQL, `, "subject", "", "host_name")`),
			strings.HasPrefix(promQL, "label_join(") && strings.HasSuffix(promQL, `, "subject", "", "scale_set")`),
			strings.HasPrefix(promQL, "label_join(") && strings.HasSuffix(promQL, `, "subject", "", "error_class")`):
			grouped++
			if !alert.QueryCondition.PromQLMultiAlert {
				t.Errorf("%s keeps a label but is not a multi-alert", alert.Name)
			}
		case strings.HasPrefix(promQL, "label_replace(") && strings.HasSuffix(promQL, `, "subject", "fleet", "", "")`):
			scalar++
			if alert.QueryCondition.PromQLMultiAlert {
				t.Errorf("%s names the fleet but is a multi-alert", alert.Name)
			}
		default:
			t.Errorf("%s carries no subject label: %s", alert.Name, promQL)
		}
	}
	if grouped == 0 || scalar == 0 {
		t.Fatalf("expected both grouped and fleet-wide alerts, got %d grouped and %d fleet", grouped, scalar)
	}
}

// Every rule that reads an OTel signal-event stream must aggregate by
// host_name. Those streams carry start_time, flag and instrumentation
// labels that change with each counter run, so an unaggregated expression
// returns one series per run: the count is wrong and the alert dispatches
// once per series. Found on 2026-09-02 when one incident of sixteen failed
// evaluations arrived as three identical pages.
func TestSignalEventRulesAggregateThePipelineLabelsAway(t *testing.T) {
	bundle, err := Load("../../config/observability-rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, rule := range bundle.Rules {
		if !strings.Contains(rule.Expression, "signal_events") {
			continue
		}
		checked++
		if !strings.Contains(rule.Expression, "by (host_name)") {
			t.Fatalf("rule %q reads a signal-event stream without aggregating by host_name: %s", rule.ID, rule.Expression)
		}
	}
	if checked < 3 {
		t.Fatalf("only %d signal-event rules were checked; the walk proves nothing", checked)
	}
}
