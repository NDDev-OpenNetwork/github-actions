package pressureobserve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHandlerExportsOOMCounterAndFreshPressureState(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "pressure.json")
	content := `{"schema_version":1,"state":"open","reason":"healthy","state_since":"2026-08-26T23:59:00Z","observed_at":"2026-08-26T23:59:30Z","oom_kills_total":23}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Handler{StatePath: path, MaxStaleness: time.Minute, Now: func() time.Time { return now }}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	for _, wanted := range []string{"gha_fleet_pressure_observer_up 1\n", "gha_fleet_host_oom_kills_total 23\n", "gha_fleet_host_pressure_open 1\n", "gha_fleet_host_root_free_percent "} {
		if !strings.Contains(response.Body.String(), wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, response.Body.String())
		}
	}
}

func TestRenderRootDiskFailsClosedWhenRootCannotBeStat(t *testing.T) {
	got := RenderRootDisk(filepath.Join(t.TempDir(), "missing"))
	if !strings.Contains(got, "gha_fleet_host_root_free_percent 0\n") {
		t.Fatalf("missing root did not fail closed\n%s", got)
	}
}

func TestHandlerFailsClosedForStaleOrInvalidState(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 2, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "pressure.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"state":"closed","reason":"cpu-pressure","state_since":"2026-08-26T23:58:00Z","observed_at":"2026-08-26T23:59:00Z","oom_kills_total":8}`), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Handler{StatePath: path, MaxStaleness: time.Minute, Now: func() time.Time { return now }}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stale health status=%d", response.Code)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"state":"invented"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(response.Body.String(), "gha_fleet_pressure_observer_up 0\n") || !strings.Contains(response.Body.String(), "gha_fleet_host_pressure_open 0\n") {
		t.Fatalf("invalid state did not fail closed\n%s", response.Body.String())
	}
}

// The compute members published a boolean derived from PSI and never PSI
// itself, so the values the admission gate decides on were readable nowhere.
// This drives the whole path: a real /proc/pressure layout under HostRoot
// through to the same series the services observer publishes.
func TestHandlerPublishesPressureStallInformationForThisHost(t *testing.T) {
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "pressure.json")
	state := `{"schema_version":1,"state":"closed","reason":"cpu-pressure","state_since":"2026-08-28T23:59:00Z","observed_at":"2026-08-28T23:59:30Z","oom_kills_total":0}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	pressureDir := filepath.Join(root, "proc", "pressure")
	if err := os.MkdirAll(pressureDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"cpu":    "some avg10=21.50 avg60=12.00 avg300=6.24 total=8085030582\n",
		"memory": "some avg10=0.00 avg60=0.00 avg300=0.00 total=569253221\nfull avg10=0.00 avg60=0.00 avg300=0.00 total=483753520\n",
		"io":     "some avg10=0.36 avg60=0.70 avg300=2.20 total=4540149284\nfull avg10=0.24 avg60=0.63 avg300=1.69 total=3214123562\n",
	} {
		if err := os.WriteFile(filepath.Join(pressureDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler{StatePath: statePath, HostRoot: root, MaxStaleness: time.Minute, Now: func() time.Time { return now }}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d", response.Code)
	}
	body := response.Body.String()
	for _, wanted := range []string{
		"gha_fleet_host_psi_available 1\n",
		// The value that closed the gate is now a series, in the same family
		// and with the same labels the services host already publishes.
		`gha_fleet_host_psi_stall_percent{mode="some",resource="cpu",window_seconds="10"} 21.5` + "\n",
		`gha_fleet_host_psi_stall_percent{mode="full",resource="io",window_seconds="300"} 1.69` + "\n",
		`gha_fleet_host_psi_stall_seconds_total{mode="some",resource="cpu"} 8085.030582` + "\n",
		// And the gate's own reason, which previously required opening a file
		// on the host to read.
		`gha_fleet_host_pressure_state{reason="cpu-pressure"} 1` + "\n",
		"gha_fleet_host_pressure_open 0\n",
	} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, body)
		}
	}
}

// A host whose pressure cannot be read must say so. Dropping the series would
// be indistinguishable from a host under no pressure at all, which is the
// failure this whole change exists to remove.
func TestHandlerReportsPressureUnavailableRatherThanOmittingIt(t *testing.T) {
	now := time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	statePath := filepath.Join(dir, "pressure.json")
	state := `{"schema_version":1,"state":"open","reason":"healthy","state_since":"2026-08-28T23:59:00Z","observed_at":"2026-08-28T23:59:30Z","oom_kills_total":0}`
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := Handler{StatePath: statePath, HostRoot: filepath.Join(dir, "absent"), MaxStaleness: time.Minute, Now: func() time.Time { return now }}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := response.Body.String()
	if !strings.Contains(body, "gha_fleet_host_psi_available 0\n") {
		t.Fatalf("an unreadable pressure source must publish availability 0\n%s", body)
	}
	if !strings.Contains(body, `gha_fleet_host_psi_stall_percent{mode="some",resource="cpu",window_seconds="10"} 0`+"\n") {
		t.Fatalf("the families must still be present so absence is not silence\n%s", body)
	}
}
