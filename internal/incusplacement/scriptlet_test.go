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
		"MINIMUM_MEMORY_PERCENT = 10", "MAX_WORKER_MEMORY_BYTES = 8192 * 1024 * 1024",
		"MAX_WORKER_CPU_UNITS = 4", "CPU_ALLOWANCE_BY_LIMIT_MIB = {2048: 2, 3072: 2, 4096: 2, 6144: 4, 8192: 4}",
		"2048: 2048", "3072: 3072", "4096: 4096", "6144: 6144", "8192: 8192",
		"LOAD_TIE_EPSILON = 0.05", "def load_per_core(state, resources)",
		`getattr(sysinfo, "load_averages", None)`, `getattr(cpu, "total", 0)`,
		"def committed_cpu_units(instances, pending_count)",
		"projected_cpu = float(committed_cpu + want_cpu_units) / float(cores)",
		"member_score < chosen_score - LOAD_TIE_EPSILON",
		"remaining > chosen_remaining", "pending_count < chosen_count",
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

func TestRenderRejectsAmbiguousCPUAllowanceForOneMemoryClass(t *testing.T) {
	cfg, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Pools[1].Resources.MemoryMiB = cfg.Pools[0].Resources.MemoryMiB
	cfg.Pools[1].Resources.VCPU = cfg.Pools[0].Resources.VCPU + 1
	if _, err := Render(cfg); err == nil || !strings.Contains(err.Error(), "one CPU allowance") {
		t.Fatalf("ambiguous memory-to-CPU class was accepted: %v", err)
	}
}
