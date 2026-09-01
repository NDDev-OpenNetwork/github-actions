package observabilitydashboards

import (
	"encoding/json"
	"strings"
	"testing"
)

// A SQL panel charts a logs stream: the language selects the dialect, the
// stream identity is declared rather than derived, and the promql-only
// invariants keep holding for everything else.
func TestSQLPanelRendersAgainstItsDeclaredLogsStream(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := RenderOpenObserve(bundle)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dashboard := range rendered {
		encoded, err := json.Marshal(dashboard)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(encoded), "gha_fleet_job_lifecycle") {
			continue
		}
		found = true
		if !strings.Contains(string(encoded), `"queryType":"sql"`) ||
			!strings.Contains(string(encoded), `"stream_type":"logs"`) {
			t.Fatalf("lifecycle dashboard did not render as sql over logs: %s", encoded[:400])
		}
	}
	if !found {
		t.Fatal("job lifecycle dashboard missing from the rendered set")
	}
}

func TestSQLPanelValidationRefusesUnsafeShapes(t *testing.T) {
	panel := Panel{
		ID: "sample_sql", Title: "t", Kind: "timeseries", Unit: "count", Description: "d",
		QueryLanguage: "sql", StreamName: "gha_fleet_job_lifecycle",
		Query: `SELECT count(*) FROM "gha_fleet_job_lifecycle"`,
	}
	if err := panel.Validate(); err != nil {
		t.Fatal(err)
	}
	compound := panel
	compound.Query = "SELECT 1; DROP TABLE x"
	if err := compound.Validate(); err == nil {
		t.Fatal("compound sql accepted")
	}
	unnamed := panel
	unnamed.StreamName = ""
	if err := unnamed.Validate(); err == nil {
		t.Fatal("sql panel without a stream accepted")
	}
	foreign := panel
	foreign.Query = `SELECT count(*) FROM "other_stream"`
	if err := foreign.Validate(); err == nil {
		t.Fatal("sql panel reading a foreign stream accepted")
	}
	promqlWithStream := Panel{ID: "sample_promql", Title: "t", Kind: "stat", Unit: "count", Description: "d",
		Query: "max(gha_fleet_platform_healthy)", StreamName: "gha_fleet_platform_healthy"}
	if err := promqlWithStream.Validate(); err == nil {
		t.Fatal("promql panel carrying stream_name accepted")
	}
}
