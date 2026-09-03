package hostcapacity

import (
	"path/filepath"
	"testing"

	fleetconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func load(t *testing.T, name string) fleetconfig.Config {
	t.Helper()
	cfg, err := fleetconfig.Load(filepath.Join("..", "..", "config", name))
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return cfg
}

// The ceiling is what the declared caps imply, never a constant. Stating it as
// a derivation is the whole point: change a class's shape and the ceiling moves
// with it, which is exactly what happened when standard went from 4 vCPU /
// 10240 MiB to 2 / 4096 and its ceiling went from one worker to three.
func TestDeclaredCapsAlreadyImplyTheCeiling(t *testing.T) {
	t.Parallel()
	cfg := load(t, "example-runner-1.yaml")
	for _, expected := range []Limit{
		{Pool: "nddev-linux-fast", Workers: 4, Binding: BindingMemory},
		{Pool: "nddev-linux-standard", Workers: 3, Binding: BindingMemory},
		{Pool: "nddev-linux-integration", Workers: 1, Binding: BindingMemory},
		{Pool: "nddev-linux-untrusted", Workers: 2, Binding: BindingCPU},
		{Pool: "nddev-linux-release", Workers: 1, Binding: BindingMemory},
	} {
		got, err := ForPool(cfg, expected.Pool)
		if err != nil {
			t.Fatalf("%s: %v", expected.Pool, err)
		}
		if got != expected {
			t.Errorf("%s: derived %+v, want %+v", expected.Pool, got, expected)
		}
	}
}

// The traced C/C++ workload establishes the 8-GiB hard class. One heavy worker
// plus the host reserve fits every 15991-MiB member; two do not. Concurrency
// comes from four failure-domain members rather than by overcommitting the hard
// memory promise on one member.
func TestOneIntegrationContainerFitsAndASecondDoesNot(t *testing.T) {
	t.Parallel()
	cfg := load(t, "example-runner-2.yaml")

	integration, exists := cfg.Pool("nddev-linux-integration")
	if !exists {
		t.Fatal("the integration host declares no integration pool")
	}
	// Give the project every CPU unit the host has and see what memory says.
	generous := cfg
	generous.Incus.ProjectMaxCPUUnits = 8
	limit, err := ForPool(generous, "nddev-linux-integration")
	if err != nil {
		t.Fatal(err)
	}
	if limit.Workers != 1 || limit.Binding != BindingMemory {
		t.Fatalf("with the CPU cap opened to the whole host, integration derives %+v; "+
			"expected one memory-bounded container", limit)
	}

	// And the memory a second worker would need, against what the host has.
	const observedHostMiB = 15991 // measured on gha-runner-1..4, 2026-08-15
	if want := integration.Resources.MemoryMiB; want+cfg.HostReserve.MinimumMemoryMiB > observedHostMiB {
		t.Fatalf("one integration container plus reserve wants %d MiB and the host has %d MiB", want+cfg.HostReserve.MinimumMemoryMiB, observedHostMiB)
	}
	if want := 2 * integration.Resources.MemoryMiB; want <= observedHostMiB {
		t.Fatalf("two integration containers unexpectedly fit: want %d MiB host %d MiB", want, observedHostMiB)
	}
}

// Every declared host must derive a limit for every pool it declares, or the
// derivation has a hole somewhere the configuration does not.
func TestEveryDeclaredHostDerivesEveryPool(t *testing.T) {
	t.Parallel()
	matches, err := filepath.Glob(filepath.Join("..", "..", "config", "example-*.yaml"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("glob platform configs: %v", err)
	}
	for _, path := range matches {
		cfg, loadErr := fleetconfig.Load(path)
		if loadErr != nil {
			t.Fatalf("load %s: %v", filepath.Base(path), loadErr)
		}
		limits, deriveErr := ForHost(cfg)
		if deriveErr != nil {
			t.Fatalf("%s: %v", filepath.Base(path), deriveErr)
		}
		if len(limits) != len(cfg.Pools) {
			t.Errorf("%s declares %d pools and derived %d limits", filepath.Base(path), len(cfg.Pools), len(limits))
		}
		for _, limit := range limits {
			if limit.Workers < 1 {
				t.Errorf("%s pool %q derives %d workers, so the host declares a pool it cannot run one of",
					filepath.Base(path), limit.Pool, limit.Workers)
			}
		}
	}
}
