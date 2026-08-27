package observabilityrules

import (
	"fmt"
	"sort"
)

type OpenObserveBundle struct {
	SchemaVersion int                `json:"schema_version"`
	Organization  string             `json:"organization"`
	Destination   string             `json:"destination"`
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
	Type             string                    `json:"type"`
	PromQL           string                    `json:"promql"`
	PromQLCondition  OpenObserveValueCondition `json:"promql_condition"`
	PromQLMultiAlert bool                      `json:"promql_multi_alert"`
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
		frequencyMinutes := (rule.EvaluationSecs + 59) / 60
		periodMinutes := max((rule.HoldSecs+59)/60, frequencyMinutes)
		promQL, err := sustainedPromQL(rule)
		if err != nil {
			return OpenObserveBundle{}, err
		}
		result.Alerts = append(result.Alerts, OpenObserveAlert{
			Name:       rule.ID,
			OrgID:      bundle.Organization,
			StreamType: "metrics",
			StreamName: rule.StreamName,
			QueryCondition: OpenObserveQuery{
				Type:   "promql",
				PromQL: promQL,
				PromQLCondition: OpenObserveValueCondition{
					Column: "value", Operator: rule.Operator, Value: rule.Threshold,
				},
			},
			TriggerCondition: OpenObserveTrigger{
				// OpenObserve defines threshold as a PromQL series-coverage gate,
				// not a consecutive-evaluation counter. Sustained time semantics
				// live in the range-subquery expression above.
				Period: periodMinutes, Operator: ">=", Threshold: 1,
				Frequency: frequencyMinutes, FrequencyType: "minutes", Silence: silence,
				Timezone: "UTC", AlignTime: true,
			},
			Destinations: []string{destination},
			ContextAttributes: map[string]string{
				"action": rule.Action, "owner": rule.Owner, "recovery": rule.Recovery,
				"runbook": rule.Runbook, "severity": rule.Severity,
			},
			Description: rule.Summary,
			Enabled:     enable,
			Priority:    priority,
			Tags:        []string{"managed-by:gds", "severity:" + rule.Severity},
		})
	}
	sort.Slice(result.Alerts, func(i, j int) bool { return result.Alerts[i].Name < result.Alerts[j].Name })
	return result, nil
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
