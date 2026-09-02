package observabilityrules

import (
	"fmt"
	"regexp"
	"sort"
)

type OpenObserveBundle struct {
	SchemaVersion int                `json:"schema_version"`
	Organization  string             `json:"organization"`
	Destination   string             `json:"destination"`
	Notification  *Notification      `json:"notification,omitempty"`
	Alerts        []OpenObserveAlert `json:"alerts"`
}

type OpenObserveAlert struct {
	Name              string             `json:"name"`
	OrgID             string             `json:"org_id"`
	StreamType        string             `json:"stream_type"`
	StreamName        string             `json:"stream_name"`
	IsRealTime        bool               `json:"is_real_time"`
	QueryCondition    OpenObserveQuery   `json:"query_condition"`
	TriggerCondition  OpenObserveTrigger `json:"trigger_condition"`
	Destinations      []string           `json:"destinations"`
	ContextAttributes map[string]string  `json:"context_attributes"`
	Description       string             `json:"description"`
	Enabled           bool               `json:"enabled"`
	TZOffset          int                `json:"tz_offset"`
	CreatesIncident   bool               `json:"creates_incident"`
	Priority          int                `json:"priority"`
	Tags              []string           `json:"tags"`
}

type OpenObserveQuery struct {
	Type             string                     `json:"type"`
	SQL              string                     `json:"sql,omitempty"`
	PromQL           string                     `json:"promql"`
	PromQLCondition  *OpenObserveValueCondition `json:"promql_condition,omitempty"`
	PromQLMultiAlert bool                       `json:"promql_multi_alert"`
}

type OpenObserveValueCondition struct {
	Column     string  `json:"column"`
	Operator   string  `json:"operator"`
	Value      float64 `json:"value"`
	IgnoreCase bool    `json:"ignore_case"`
}

type OpenObserveTrigger struct {
	Period          int    `json:"period"`
	Operator        string `json:"operator"`
	Threshold       int    `json:"threshold"`
	Frequency       int    `json:"frequency"`
	FrequencyType   string `json:"frequency_type"`
	Silence         int    `json:"silence"`
	Timezone        string `json:"timezone"`
	ToleranceInSecs int    `json:"tolerance_in_secs"`
	AlignTime       bool   `json:"align_time"`
}

func RenderOpenObserve(bundle Bundle, destination string, enable bool) (OpenObserveBundle, error) {
	if err := bundle.Validate(); err != nil {
		return OpenObserveBundle{}, err
	}
	if !idPattern.MatchString(destination) {
		return OpenObserveBundle{}, fmt.Errorf("OpenObserve destination identity is invalid")
	}
	result := OpenObserveBundle{
		SchemaVersion: 1,
		Organization:  bundle.Organization,
		Destination:   destination,
		Notification:  bundle.Notification,
		Alerts:        make([]OpenObserveAlert, 0, len(bundle.Rules)),
	}
	for _, rule := range bundle.Rules {
		priority := 3
		// OpenObserve v0.92 pauses outcome evaluation during silence rather
		// than suppressing notification delivery alone. Keep ticket recovery
		// fresh enough to trust while still spacing repeat notifications.
		silence := 15
		if rule.Severity == "page" {
			priority = 1
			silence = 10
		}
		// A rule that states its own repeat cadence overrides the default. See
		// Rule.RepeatSecs: two constants cannot be right for both a queue that
		// drains by itself and a host that needs patching.
		if rule.RepeatSecs > 0 {
			silence = rule.RepeatSecs / 60
		}
		frequencyMinutes := (rule.EvaluationSecs + 59) / 60
		periodMinutes := max((rule.HoldSecs+59)/60, frequencyMinutes)
		streamType := "metrics"
		var query OpenObserveQuery
		// AlignTime false: every alert keeps its own schedule instead of being
		// snapped to the wall clock.
		//
		// OpenObserve aligns an alert's next run to the previous interval
		// boundary for its frequency (TriggerCondition::get_aligned_next_trigger_time),
		// so with alignment on, every one-minute rule evaluates at :00 and every
		// ten-minute rule at :00, :10, :20 -- thirty-four alerts issuing their
		// searches and persisting their group states in the same millisecond.
		// Measured on 2026-09-02: the searches queued behind each other until
		// some exceeded the PromQL load-data timeout (that is what raised
		// alert_evaluation_failed), and the state writes queued behind the
		// single SQLite writer until they were refused --
		// "could not persist group states ...: database is locked", 2386 times
		// in one day. A scheduler that cannot persist that it already notified
		// notifies again on the next tick, which is why the channel repeated
		// the same page every ten minutes for hours.
		//
		// Nothing in these rules depends on clock alignment: every expression
		// takes its own range relative to evaluation time.
		trigger := OpenObserveTrigger{
			Period: periodMinutes, Frequency: frequencyMinutes, FrequencyType: "minutes",
			Silence: silence, Timezone: "UTC", AlignTime: false,
		}
		switch rule.QueryLanguage {
		case "sql":
			// A SQL rule reads a logs stream over the alert period; the
			// condition lives in the statement (HAVING), and the trigger gates
			// on how many result rows came back. An absence rule is therefore
			// expressible: a global aggregate always returns its one row, and
			// HAVING keeps it exactly when the window was empty.
			streamType = "logs"
			query = OpenObserveQuery{Type: "sql", SQL: rule.Expression}
			trigger.Operator = rule.Operator
			trigger.Threshold = int(rule.Threshold)
		default:
			promQL, err := sustainedPromQL(rule)
			if err != nil {
				return OpenObserveBundle{}, err
			}
			query = OpenObserveQuery{
				Type:   "promql",
				PromQL: withSubjectLabel(promQL),
				PromQLCondition: &OpenObserveValueCondition{
					Column: "value", Operator: rule.Operator, Value: rule.Threshold,
				},
				// Multi-alert turns each returned series into its own
				// notification carrying that series' labels. Without it the
				// backend renders one message from the first row, which is
				// worse than naming nobody: it names one host arbitrarily.
				//
				// Derived from the expression rather than declared beside it.
				// A second field would be one more thing to keep in sync, and
				// the expression already says whether a subject survives.
				PromQLMultiAlert: namesASubject(promQL),
			}
			// OpenObserve defines threshold as a PromQL series-coverage gate,
			// not a consecutive-evaluation counter. Sustained time semantics
			// live in the range-subquery expression above.
			trigger.Operator = ">="
			trigger.Threshold = 1
		}
		result.Alerts = append(result.Alerts, OpenObserveAlert{
			Name:             rule.ID,
			OrgID:            bundle.Organization,
			StreamType:       streamType,
			StreamName:       rule.StreamName,
			QueryCondition:   query,
			TriggerCondition: trigger,
			Destinations:     []string{destination},
			ContextAttributes: map[string]string{
				"action": rule.Action, "owner": rule.Owner, "recovery": rule.Recovery,
				"runbook": rule.Runbook, "severity": rule.Severity,
			},
			Description: rule.Summary,
			// Both, not either. The flag is the operator's kill switch for a
			// whole reconcile; the field is this rule's own intent, reviewed in
			// the file with the expression it belongs to.
			//
			// Taking only the flag made the field decorative: every rule in the
			// bundle read `enabled: false` while all 28 were live and ten of them
			// were paging. A reader checking whether an alert was armed got the
			// wrong answer from the only document that looked like the authority.
			Enabled:  enable && rule.Enabled,
			Priority: priority,
			Tags:     []string{"managed-by:gds", "severity:" + rule.Severity},
		})
	}
	sort.Slice(result.Alerts, func(i, j int) bool { return result.Alerts[i].Name < result.Alerts[j].Name })
	return result, nil
}

// PromQL aggregation operators, which drop every label unless the expression
// keeps some with `by`. Range functions such as `max_over_time` are not in this
// set: they are applied per series and preserve labels.
var aggregationOperator = regexp.MustCompile(
	`\b(sum|min|max|avg|count|count_values|group|stddev|stdvar|topk|bottomk|quantile)\s*\(`,
)

var keepsLabels = regexp.MustCompile(`\bby\s*\(\s*([a-zA-Z_][a-zA-Z0-9_]*)`)

// namesASubject reports whether the expression returns one series per subject
// rather than a single collapsed number. An aggregation with no `by` clause
// collapses the fleet into one value, so there is nothing to name and one
// notification is correct. Anything else -- an aggregation that keeps a label,
// or a selector that was never aggregated -- carries the labels that say which
// host, scale set or error class the alert is about.
func namesASubject(promQL string) bool {
	if keepsLabels.MatchString(promQL) {
		return true
	}
	return !aggregationOperator.MatchString(promQL)
}

// withSubjectLabel gives every PromQL alert a `subject` label for the message
// to print. A rule that keeps a label -- host_name, scale_set, error_class --
// names its subject with that label, and the template reads exactly one name,
// so the identifying label is copied into `subject` with label_join. A rule
// that aggregates the fleet into one number names the fleet.
//
// label_join rather than label_replace with a capture group: measured on the
// live engine on 2026-09-01, `label_replace(v, "subject", "$1", "host_name",
// "(.+)")` returned the series without the new label, while label_join and a
// literal label_replace value both took. The first message rendered from the
// template read "host: {host_name}" -- for a rule whose result carried no
// host_name at all -- and "observed {alert_agg_value}", a variable this
// backend version does not define. The subject is now a label the rule
// guarantees, and the observed value is the row's `value` column.
func withSubjectLabel(promQL string) string {
	if match := keepsLabels.FindStringSubmatch(promQL); match != nil {
		return fmt.Sprintf(`label_join(%s, "subject", "", %q)`, promQL, match[1])
	}
	if namesASubject(promQL) {
		// No aggregation at all: the raw series keep every label the
		// collector stamped, and host_name is the one every fleet series has.
		return fmt.Sprintf(`label_join(%s, "subject", "", "host_name")`, promQL)
	}
	return fmt.Sprintf(`label_replace(%s, "subject", "fleet", "", "")`, promQL)
}

func sustainedPromQL(rule Rule) (string, error) {
	if rule.HoldSecs <= rule.EvaluationSecs {
		return rule.Expression, nil
	}
	function := ""
	switch rule.Operator {
	case ">", ">=":
		function = "min_over_time"
	case "<", "<=":
		function = "max_over_time"
	default:
		return "", fmt.Errorf("rule %q cannot express sustained operator %q", rule.ID, rule.Operator)
	}
	return fmt.Sprintf(
		"%s((%s)[%s:%s])",
		function,
		rule.Expression,
		promQLDuration(rule.HoldSecs),
		promQLDuration(rule.EvaluationSecs),
	), nil
}

func promQLDuration(seconds int) string {
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}
