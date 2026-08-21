package observabilityrules

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = 2

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{2,63}$`)
var metricPattern = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]{0,254}$`)

type Bundle struct {
	SchemaVersion int    `json:"schema_version" yaml:"schema_version"`
	Backend       string `json:"backend" yaml:"backend"`
	Organization  string `json:"organization" yaml:"organization"`
	Rules         []Rule `json:"rules" yaml:"rules"`
}

type Rule struct {
	ID             string  `json:"id" yaml:"id"`
	Severity       string  `json:"severity" yaml:"severity"`
	QueryLanguage  string  `json:"query_language" yaml:"query_language"`
	StreamName     string  `json:"stream_name" yaml:"stream_name"`
	Expression     string  `json:"expression" yaml:"expression"`
	Operator       string  `json:"operator" yaml:"operator"`
	Threshold      float64 `json:"threshold" yaml:"threshold"`
	EvaluationSecs int     `json:"evaluation_seconds" yaml:"evaluation_seconds"`
	HoldSecs       int     `json:"hold_seconds" yaml:"hold_seconds"`
	DestinationRef string  `json:"destination_ref" yaml:"destination_ref"`
	Enabled        bool    `json:"enabled" yaml:"enabled"`
	Owner          string  `json:"owner" yaml:"owner"`
	Runbook        string  `json:"runbook" yaml:"runbook"`
	Summary        string  `json:"summary" yaml:"summary"`
	Action         string  `json:"action" yaml:"action"`
	Recovery       string  `json:"recovery" yaml:"recovery"`
}

func Load(path string) (Bundle, error) {
	file, err := os.Open(path)
	if err != nil {
		return Bundle{}, fmt.Errorf("open observability rules: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(io.LimitReader(file, 256*1024+1))
	decoder.KnownFields(true)
	var bundle Bundle
	if err := decoder.Decode(&bundle); err != nil {
		return Bundle{}, fmt.Errorf("decode observability rules: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Bundle{}, fmt.Errorf("observability rules contain multiple YAML documents")
	}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func (b Bundle) Validate() error {
	if b.SchemaVersion != SchemaVersion || b.Backend != "openobserve" || b.Organization != "default" {
		return fmt.Errorf("observability bundle identity is invalid")
	}
	if len(b.Rules) < 1 || len(b.Rules) > 64 {
		return fmt.Errorf("observability bundle rule count is invalid")
	}
	ids := make([]string, 0, len(b.Rules))
	seen := make(map[string]struct{}, len(b.Rules))
	for _, rule := range b.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("rule %q: %w", rule.ID, err)
		}
		if _, duplicate := seen[rule.ID]; duplicate {
			return fmt.Errorf("rule %q is duplicated", rule.ID)
		}
		seen[rule.ID] = struct{}{}
		ids = append(ids, rule.ID)
	}
	if !sort.StringsAreSorted(ids) {
		return fmt.Errorf("observability rules must be sorted by id")
	}
	return nil
}

func (r Rule) Validate() error {
	if !idPattern.MatchString(r.ID) || !metricPattern.MatchString(r.StreamName) || (r.Severity != "page" && r.Severity != "ticket") || r.QueryLanguage != "promql" {
		return fmt.Errorf("identity, severity or query language is invalid")
	}
	if len(r.Expression) < 3 || len(r.Expression) > 4096 || strings.ContainsAny(r.Expression, "\r\x00") {
		return fmt.Errorf("expression is invalid")
	}
	if _, valid := map[string]struct{}{">": {}, ">=": {}, "<": {}, "<=": {}, "=": {}, "!=": {}}[r.Operator]; !valid || math.IsNaN(r.Threshold) || math.IsInf(r.Threshold, 0) {
		return fmt.Errorf("operator or threshold is invalid")
	}
	if r.EvaluationSecs < 15 || r.EvaluationSecs > 900 || r.HoldSecs < r.EvaluationSecs || r.HoldSecs > 86400 {
		return fmt.Errorf("evaluation or hold duration is invalid")
	}
	if r.Severity == "page" && (r.EvaluationSecs > 60 || r.HoldSecs > 900) {
		return fmt.Errorf("page is too slow to be actionable")
	}
	if r.Severity == "ticket" && r.HoldSecs < 600 {
		return fmt.Errorf("ticket rule must represent sustained slow burn")
	}
	if !idPattern.MatchString(r.DestinationRef) || r.Owner == "" || r.Summary == "" || r.Action == "" || r.Recovery == "" {
		return fmt.Errorf("ownership or response contract is incomplete")
	}
	if !strings.HasPrefix(r.Runbook, "https://github.com/NDDev-OpenNetwork/github-actions/blob/") {
		return fmt.Errorf("runbook must be public and source-bound")
	}
	return nil
}

func (r Rule) RequiredEvaluations() int {
	return (r.HoldSecs + r.EvaluationSecs - 1) / r.EvaluationSecs
}
