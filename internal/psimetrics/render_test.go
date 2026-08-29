package psimetrics

import (
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
)

func sample() hostprobe.Pressure {
	return hostprobe.Pressure{
		Available: true,
		CPU: hostprobe.PressureResource{
			Some: hostprobe.PressureWindow{Avg10: 21.5, Avg60: 12, Avg300: 6.24, TotalMicros: 8085030582},
		},
		Memory: hostprobe.PressureResource{
			Full: hostprobe.PressureWindow{Avg10: 1.25, TotalMicros: 2_000_000},
		},
		IO: hostprobe.PressureResource{
			Full: hostprobe.PressureWindow{Avg300: 1.69, TotalMicros: 3214123562},
		},
	}
}

// Both observers publish these families and they must be the same series, or a
// fleet-wide query silently answers for a subset of hosts.
func TestRenderEmitsEveryResourceModeAndWindow(t *testing.T) {
	body := Render(sample())
	for _, wanted := range []string{
		"gha_fleet_host_psi_available 1\n",
		`gha_fleet_host_psi_stall_percent{mode="some",resource="cpu",window_seconds="10"} 21.5` + "\n",
		`gha_fleet_host_psi_stall_percent{mode="some",resource="cpu",window_seconds="300"} 6.24` + "\n",
		`gha_fleet_host_psi_stall_percent{mode="full",resource="memory",window_seconds="10"} 1.25` + "\n",
		`gha_fleet_host_psi_stall_percent{mode="full",resource="io",window_seconds="300"} 1.69` + "\n",
		`gha_fleet_host_psi_stall_seconds_total{mode="some",resource="cpu"} 8085.030582` + "\n",
		`gha_fleet_host_psi_stall_seconds_total{mode="full",resource="memory"} 2` + "\n",
	} {
		if !strings.Contains(body, wanted) {
			t.Fatalf("missing %q\n%s", wanted, body)
		}
	}
	// three resources x two modes x three windows, plus one total per mode
	if got := strings.Count(body, "gha_fleet_host_psi_stall_percent{"); got != 18 {
		t.Fatalf("stall percent series = %d, want 18", got)
	}
	if got := strings.Count(body, "gha_fleet_host_psi_stall_seconds_total{"); got != 6 {
		t.Fatalf("stall seconds series = %d, want 6", got)
	}
}

// An unavailable source publishes zeroes with availability 0 rather than
// disappearing: a missing series and an idle host look identical in a query,
// and telling them apart is the entire point of collecting this.
func TestRenderKeepsSeriesWhenPressureIsUnavailable(t *testing.T) {
	body := Render(hostprobe.Pressure{})
	if !strings.Contains(body, "gha_fleet_host_psi_available 0\n") {
		t.Fatalf("availability must be reported as 0\n%s", body)
	}
	if got := strings.Count(body, "gha_fleet_host_psi_stall_percent{"); got != 18 {
		t.Fatalf("stall percent series = %d, want 18 even when unavailable", got)
	}
}
