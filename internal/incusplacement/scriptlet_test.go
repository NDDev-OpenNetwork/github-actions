package incusplacement

import (
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func TestRenderCPUAwarePlacementFromPortablePolicy(t *testing.T) {
	cfg, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	script, err := Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`PROJECT = "gha-fleet"`, `POOL = "gha-lvm"`,
		"MINIMUM_MEMORY_BYTES = 1024 * 1024 * 1024",
		"MINIMUM_MEMORY_PERCENT = 10", "MAX_WORKER_MEMORY_BYTES = 2560 * 1024 * 1024",
		"2048: 512", "3072: 512", "4096: 2560", "6144: 2048",
		"LOAD_TIE_EPSILON = 0.05", "def load_per_core(state, resources)",
		`getattr(sysinfo, "load_averages", None)`, `getattr(cpu, "total", 0)`,
		"member_load < chosen_load - LOAD_TIE_EPSILON",
		"remaining < chosen_remaining", "pending_count > chosen_count",
		"user.gha_pressure.state", "get_instances_count(PROJECT, name, True)",
		"insufficient-memory: no fleet member has room for this worker",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("placement script omits %q", required)
		}
	}
	for _, forbidden := range []string{"NDDev-it-com", "My-Attention", "almaty", "10.110."} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("portable placement contains private identity %q", forbidden)
		}
	}
}

func TestRenderRejectsInvalidPolicy(t *testing.T) {
	cfg, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.HostReserve.MinimumPercent = 0
	if _, err := Render(cfg); err == nil {
		t.Fatal("invalid platform policy rendered a placement script")
	}
}
