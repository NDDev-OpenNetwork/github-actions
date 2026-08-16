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
	cfg := load(t, "server-gha-runner-1.yaml")
	// Four members carry the per-member caps, so the fleet has 12 instances,
	// 24 CPU units and 49152 MiB to divide -- the same totals the live Incus
	// project reports. The ceiling is fleet-wide because max_running is:
	// one queue drives every member and the provider counts running workers
	// across the cluster.
	for _, expected := range []Limit{
		// 24 / 2 = 12 and 49152 / 3072 = 16, so the 12-instance ceiling binds.
		{Pool: "nddev-linux-fast", Workers: 12, Binding: BindingInstances},
		// 24 / 2 = 12 and 49152 / 4096 = 12, so instances bind here too. Not a
		// pilot decision -- arithmetic, on a class sized from a measured
		// 536 MiB guest baseline rather than from the host's shape.
		{Pool: "nddev-linux-standard", Workers: 12, Binding: BindingInstances},
		// 49152 / 6144 = 8 is below both the 12-instance and 12-CPU ceilings,
		// so memory binds. The class declares max_running 3 against it.
		{Pool: "nddev-linux-integration", Workers: 8, Binding: BindingMemory},
		{Pool: "nddev-linux-release", Workers: 6, Binding: BindingCPU},
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

// Every declared host must derive a limit for every pool it declares, or the
// derivation has a hole somewhere the configuration does not.
func TestEveryDeclaredHostDerivesEveryPool(t *testing.T) {
	t.Parallel()
	matches, err := filepath.Glob(filepath.Join("..", "..", "config", "server-*.yaml"))
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

// #257 asked for more integration throughput, and the answer this file used to
// record was "not by raising the number": a second 4 vCPU / 8192 MiB worker did
// not fit beside the first on one 15991 MiB host, so it needed a second host.
//
// Both halves of that stopped being true. Clustering made the ceiling
// fleet-wide, and the class was resized from measurement -- a live integration
// worker peaked at 1179 MiB of its 8192 and held 1.1 of 4 vCPU -- to
// 2 vCPU / 6144 MiB. What replaces the old assertion is the invariant that
// actually protects the fleet: a class may declare no more concurrency than the
// caps can carry, whatever its shape.
func TestNoClassDeclaresMoreConcurrencyThanTheCapsCarry(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"server-gha-runner-1.yaml", "server-gha-runner-2.yaml",
		"server-gha-runner-3.yaml", "server-gha-runner-4.yaml",
	} {
		cfg := load(t, host)
		for _, pool := range cfg.Pools {
			limit, err := ForPool(cfg, pool.Name)
			if err != nil {
				t.Fatalf("%s: %s: %v", host, pool.Name, err)
			}
			if pool.MaxRunning > limit.Workers {
				t.Errorf(
					"%s: %s declares max_running %d but the caps carry %d (%s binds)",
					host, pool.Name, pool.MaxRunning, limit.Workers, limit.Binding,
				)
			}
		}
	}
}
