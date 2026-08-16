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
	for _, expected := range []Limit{
		// 6 CPU units / 2 vCPU = 3, and 12288 MiB / 3072 = 4, so the instance
		// ceiling of 3 is what binds.
		{Pool: "nddev-linux-fast", Workers: 3, Binding: BindingInstances},
		// 6 / 2 = 3 and 12288 / 4096 = 3, so the instance ceiling binds here
		// too. Not a pilot decision -- arithmetic, on a class sized from a
		// measured 536 MiB guest baseline rather than from the host's shape.
		{Pool: "nddev-linux-standard", Workers: 3, Binding: BindingInstances},
		{Pool: "nddev-linux-integration", Workers: 1, Binding: BindingCPU},
		{Pool: "nddev-linux-release", Workers: 1, Binding: BindingCPU},
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

// #257 asks for more integration throughput. This is what would have to change,
// and the answer is not the ceiling: raising the CPU cap to the host's full
// eight units still leaves memory admitting one, because two integration
// workers want 20480 MiB and the host has 15991.
//
// So the second integration worker needs a second host. Recording it as a test
// because the intuitive fix -- raise the number -- would have looked correct and
// produced a host that admits work it cannot run.
func TestASecondIntegrationWorkerDoesNotFitOnOneHost(t *testing.T) {
	t.Parallel()
	cfg := load(t, "server-gha-runner-2.yaml")

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
			"expected memory to be the binding constraint at one worker", limit)
	}

	// And the memory a second worker would need, against what the host has.
	const observedHostMiB = 15991 // measured on gha-runner-1..4, 2026-08-15
	if want := 2 * integration.Resources.MemoryMiB; want <= observedHostMiB {
		t.Fatalf("two integration workers want %d MiB and the host has %d MiB; "+
			"this test's premise no longer holds and #257 may be solvable on one host", want, observedHostMiB)
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
