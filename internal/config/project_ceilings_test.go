package config

import "testing"

// The project ceilings are bootstrap limits, not the real one: admission
// enforces the real limit against the host it observes. They still have to be
// wide enough for the shape the fleet actually runs and narrow enough that a
// typo cannot ask for a machine nobody owns.
func TestProjectCeilingsAdmitTheDedicatedShapeAndRefuseBeyondIt(t *testing.T) {
	dedicated, err := Load("../../config/server-gha-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if dedicated.HostReserve.MinimumCPUUnits != 2 {
		t.Fatalf("a dedicated host reserves %d CPU units; the ceilings below assume two",
			dedicated.HostReserve.MinimumCPUUnits)
	}
	if dedicated.Incus.ProjectMaxCPUUnits != 6 || dedicated.Incus.ProjectMaxInstances != 3 {
		t.Fatalf("dedicated project is %d CPU units over %d instances, want 6 over 3",
			dedicated.Incus.ProjectMaxCPUUnits, dedicated.Incus.ProjectMaxInstances)
	}
	// Two reserved plus six given away is exactly the eight cores these hosts
	// have. Anything more would be overcommit, which the guardrails forbid.
	if reserved := dedicated.HostReserve.MinimumCPUUnits + dedicated.Incus.ProjectMaxCPUUnits; reserved != 8 {
		t.Fatalf("reserve plus project is %d CPU units, want exactly the 8 the host has", reserved)
	}

	for name, mutate := range map[string]func(*Config){
		"more CPU units than a host has beside its reserve": func(c *Config) { c.Incus.ProjectMaxCPUUnits = 7 },
		"no CPU units at all":                               func(c *Config) { c.Incus.ProjectMaxCPUUnits = 0 },
		"more instances than CPU units can back":            func(c *Config) { c.Incus.ProjectMaxInstances = 5 },
		"no instances at all":                               func(c *Config) { c.Incus.ProjectMaxInstances = 0 },
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
		"../../config/server-gha-runner-1.yaml",
		"../../config/server-gha-runner-2.yaml",
		"../../config/server-gha-runner-1.yaml",
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
