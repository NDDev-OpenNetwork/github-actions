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
	for _, wanted := range []string{"gha_fleet_pressure_observer_up 1\n", "gha_fleet_host_oom_kills_total 23\n", "gha_fleet_host_pressure_open 1\n"} {
		if !strings.Contains(response.Body.String(), wanted) {
			t.Fatalf("metrics missing %q\n%s", wanted, response.Body.String())
		}
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
