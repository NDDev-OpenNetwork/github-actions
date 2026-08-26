package observabilityrules

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestOpenObserveQueryContractIsPortableAndComplete(t *testing.T) {
	raw, err := os.ReadFile("../../config/openobserve-queries.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		SchemaVersion int `yaml:"schema_version"`
		Backend       string
		Organization  string
		Queries       []struct {
			ID         string   `yaml:"id"`
			Stream     string   `yaml:"stream"`
			SearchType string   `yaml:"search_type"`
			SQL        string   `yaml:"sql"`
			Parameters []string `yaml:"parameters"`
		}
		Dashboards []struct {
			ID     string `yaml:"id"`
			Panels []struct {
				Query string `yaml:"query"`
			} `yaml:"panels"`
		}
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != 1 || document.Backend != "openobserve" || document.Organization != "default" {
		t.Fatalf("query contract identity = %#v", document)
	}
	queries := make(map[string]struct{}, len(document.Queries))
	for _, query := range document.Queries {
		if query.ID == "" || (query.SearchType != "logs" && query.SearchType != "traces") ||
			!strings.Contains(query.SQL, `FROM "`+query.Stream+`"`) {
			t.Fatalf("invalid query = %#v", query)
		}
		if _, exists := queries[query.ID]; exists {
			t.Fatalf("duplicate query %q", query.ID)
		}
		queries[query.ID] = struct{}{}
	}
	for _, dashboard := range document.Dashboards {
		if dashboard.ID == "" || len(dashboard.Panels) == 0 {
			t.Fatalf("invalid dashboard = %#v", dashboard)
		}
		for _, panel := range dashboard.Panels {
			if _, exists := queries[panel.Query]; !exists {
				t.Fatalf("dashboard %q references unknown query %q", dashboard.ID, panel.Query)
			}
		}
	}
	text := string(raw)
	for _, required := range []string{
		"eligibility-to-start-by-job",
		"SUM(duration) AS eligibility_to_start_us",
		"github_workflow_run_id", "github_job_name",
		"'queue.queued'", "'queue.assigned'", "'queue.acquiring'", "'queue.acquired'",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("eligibility-to-start query lacks %q", required)
		}
	}
	if strings.Contains(text, "'queue.running'") {
		t.Fatal("eligibility-to-start query includes execution time")
	}
	for _, privateIdentity := range []string{"NDDev-it-com", "My-Attention-AI-Inc", "10.110.", "209.38."} {
		if strings.Contains(text, privateIdentity) {
			t.Fatalf("public query contract contains private identity %q", privateIdentity)
		}
	}
}
