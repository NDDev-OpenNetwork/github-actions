// Package hostcapacity derives how many workers of a class a host can run at
// once, from what the host configuration already declares.
//
// The fleet asserts one job per host in nine places across two components, and
// that number was read as a pilot constant to be raised later. It is not. For
// the heavy classes it is what the declared Incus project caps already imply:
// a four-vCPU worker against project_max_cpu_units: 6 admits one, and a
// 10240 MiB worker against project_max_memory_mib: 12288 admits one. The fast
// class, at two vCPU and 4096 MiB, admits three -- which is exactly the
// max_running: 3 gha-runner-3 already declares.
//
// So the constant is not wrong, it is merely unexplained, and unexplained is
// what makes it look raiseable. Deriving it says what would actually have to
// change: on the current hardware, a second concurrent integration worker needs
// 20480 MiB where the host has 15991, so it needs another host rather than a
// larger number.
package hostcapacity

import (
	"fmt"

	fleetconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

// Limit is the concurrency a pool can reach and the constraint that decides it.
type Limit struct {
	Pool string
	// Workers is the number of workers of this pool that fit at once.
	Workers int
	// Binding names the cap that produced Workers. It is reported because "one"
	// is a very different fact depending on whether the instance ceiling, the
	// CPU units or the memory decided it.
	Binding string
}

// Bindings, in the order they are tested.
const (
	BindingInstances = "incus.project_max_instances"
	BindingCPU       = "incus.project_max_cpu_units"
	BindingMemory    = "incus.project_max_memory_mib"
)

// ForPool derives the concurrency one pool can reach on the host the
// configuration describes.
func ForPool(cfg fleetconfig.Config, pool string) (Limit, error) {
	declared, exists := cfg.Pool(pool)
	if !exists {
		return Limit{}, fmt.Errorf("host declares no pool %q", pool)
	}
	if declared.Resources.VCPU <= 0 || declared.Resources.MemoryMiB <= 0 {
		return Limit{}, fmt.Errorf("pool %q declares no resources to divide the host caps by", pool)
	}

	limit := Limit{Pool: pool, Workers: cfg.Incus.ProjectMaxInstances, Binding: BindingInstances}
	if byCPU := cfg.Incus.ProjectMaxCPUUnits / declared.Resources.VCPU; byCPU < limit.Workers {
		limit.Workers, limit.Binding = byCPU, BindingCPU
	}
	if byMemory := cfg.Incus.ProjectMaxMemoryMiB / declared.Resources.MemoryMiB; byMemory < limit.Workers {
		limit.Workers, limit.Binding = byMemory, BindingMemory
	}
	if limit.Workers < 0 {
		limit.Workers = 0
	}
	return limit, nil
}

// ForHost derives every declared pool's concurrency.
func ForHost(cfg fleetconfig.Config) ([]Limit, error) {
	limits := make([]Limit, 0, len(cfg.Pools))
	for _, pool := range cfg.Pools {
		limit, err := ForPool(cfg, pool.Name)
		if err != nil {
			return nil, err
		}
		limits = append(limits, limit)
	}
	return limits, nil
}
