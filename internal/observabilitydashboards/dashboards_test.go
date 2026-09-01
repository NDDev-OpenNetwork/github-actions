package observabilitydashboards

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func configPath(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(current), "..", "..", "config", "observability-dashboards.yaml")
}

func TestPublishedDashboardBundleIsValidAndRenderable(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Dashboards) != 11 {
		t.Fatalf("dashboards=%d", len(bundle.Dashboards))
	}
	rendered, err := Render(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Bundle
	if err := json.Unmarshal(rendered, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestDashboardValidationRejectsUnownedMetric(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dashboards[0].Panels[0].Query = "max(process_cpu_seconds_total)"
	if err := bundle.Validate(); err == nil {
		t.Fatal("unowned metric query was accepted")
	}
}

func TestDashboardValidationRejectsOrderingDrift(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Dashboards[0], bundle.Dashboards[1] = bundle.Dashboards[1], bundle.Dashboards[0]
	if err := bundle.Validate(); err == nil {
		t.Fatal("unsorted dashboards were accepted")
	}
}

// A panel query carrying a set operator renders an empty series on this
// backend, whatever its operands hold -- the same defect that left four alerts
// unable to fire. A blank panel is quieter than a blind alert but reads the
// same way: nothing wrong here.
//
// Measured live: max(A) -> 1, max(B) -> 0, and max(A) or max(B) -> empty.
// Arithmetic works, so non-negative counters compared against zero say the
// same thing with `+`.
func TestNoPanelDependsOnASetOperatorTheBackendDiscards(t *testing.T) {
	bundle, err := Load(configPath(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, dashboard := range bundle.Dashboards {
		for _, panel := range dashboard.Panels {
			for _, operator := range []string{" or ", " and ", " unless "} {
				if strings.Contains(panel.Query, operator) {
					t.Errorf("panel %s/%s renders empty: its query uses%qwhich this backend evaluates to an empty result: %q",
						dashboard.ID, panel.ID, operator, panel.Query)
				}
			}
		}
	}
}
