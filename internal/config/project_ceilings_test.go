package config

import "testing"

// The project ceilings are bootstrap limits, not the real one: admission
// enforces the real limit against the host it observes. They still have to be
// wide enough for the shape the fleet actually runs and narrow enough that a
// typo cannot ask for a machine nobody owns.
func TestProjectCeilingsAdmitTheDedicatedShapeAndRefuseBeyondIt(t *testing.T) {
	dedicated, err := Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if dedicated.HostReserve.MinimumCPUUnits != 2 {
		t.Fatalf("a dedicated host reserves %d CPU units; the ceilings below assume two",
			dedicated.HostReserve.MinimumCPUUnits)
	}
	if dedicated.Incus.ProjectMaxCPUUnits != 10 || dedicated.Incus.ProjectMaxMemoryMiB != 14*1024 || dedicated.Incus.ProjectMaxInstances != 8 {
		t.Fatalf("dedicated project is %d logical CPU units, %d MiB over %d instances, want 10 units, 14336 MiB over 8",
			dedicated.Incus.ProjectMaxCPUUnits, dedicated.Incus.ProjectMaxMemoryMiB,
			dedicated.Incus.ProjectMaxInstances)
	}
	// The field is a logical admission budget, not an Incus cpuset limit. Ten
	// weighted units allow one opportunistic two-unit worker while placement
	// still closes under the measured pressure policy and the physical-RAM
	// commitment envelope. Emergency swap is not schedulable capacity.

	for name, mutate := range map[string]func(*Config){
		"more logical CPU units than the bounded burst": func(c *Config) { c.Incus.ProjectMaxCPUUnits = 11 },
		"no CPU units at all":                           func(c *Config) { c.Incus.ProjectMaxCPUUnits = 0 },
		"unbounded instance fanout":                     func(c *Config) { c.Incus.ProjectMaxInstances = 9 },
		"no instances at all":                           func(c *Config) { c.Incus.ProjectMaxInstances = 0 },
		"more memory than a member can safely expose":   func(c *Config) { c.Incus.ProjectMaxMemoryMiB = 14337 },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := dedicated
			mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("the ceiling accepted a project it should refuse")
			}
		})
	}
}

// The pool this exists for runs lint, format, typecheck and secret scans. Job
// trees of that kind were observed between 292 and 607 MiB resident, and a
// live worker reported a 536 MiB guest baseline, so three gibibytes leaves
// about four times the observed peak for the job itself.
//
// The exact number is not the invariant; being strictly cheaper than standard
// is. This pool only earns its own image, profile and scale set if a fast job
// costs the fleet less than the same job on standard would.
func TestFastPoolIsSizedForWhatItRuns(t *testing.T) {
	for _, path := range []string{
		"../../config/example-runner-1.yaml",
		"../../config/example-runner-2.yaml",
		"../../config/example-runner-1.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			loaded, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			var fast *Pool
			for index, pool := range loaded.Pools {
				if pool.Name == "nddev-linux-fast" {
					fast = &loaded.Pools[index]
				}
			}
			if fast == nil {
				t.Fatal("no fast pool declared")
			}
			var standard *Pool
			for index, pool := range loaded.Pools {
				if pool.Name == "nddev-linux-standard" {
					standard = &loaded.Pools[index]
				}
			}
			if standard == nil {
				t.Fatal("no standard pool declared")
			}
			// 607 MiB observed peak plus the 536 MiB guest baseline, with room
			// to spare. Below this the pool would start failing the work it
			// exists for, which is worse than not having the pool.
			const observedPeakWithBaselineMiB = 607 + 536
			if fast.Resources.MemoryMiB < 2*observedPeakWithBaselineMiB {
				t.Fatalf("fast worker holds %d MiB, which is less than twice the %d MiB a measured fast job needs",
					fast.Resources.MemoryMiB, observedPeakWithBaselineMiB)
			}
			if fast.Resources.MemoryMiB >= standard.Resources.MemoryMiB {
				t.Fatal("the fast worker is no smaller than a standard one, which defeats the point of the pool")
			}
			if fast.Resources.VCPU > standard.Resources.VCPU {
				t.Fatal("the fast worker costs more CPU than a standard one, which defeats the point of the pool")
			}
		})
	}
}
