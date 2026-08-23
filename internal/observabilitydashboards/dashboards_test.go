package observabilitydashboards

import (
	"encoding/json"
	"path/filepath"
	"runtime"
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
	if len(bundle.Dashboards) != 6 {
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
