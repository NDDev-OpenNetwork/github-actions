package admission

import "fmt"

type Reason string

const (
	ReasonAdmitted           Reason = "admitted"
	ReasonHostUnhealthy      Reason = "host-unhealthy"
	ReasonDiskPressure       Reason = "disk-pressure"
	ReasonPoolSaturated      Reason = "pool-saturated"
	ReasonQueueIntent        Reason = "queue-intent"
	ReasonInsufficientCPU    Reason = "insufficient-cpu"
	ReasonInsufficientMemory Reason = "insufficient-memory"
)

type HostSnapshot struct {
	Healthy            bool `json:"healthy" yaml:"healthy"`
	TotalCPUUnits      int  `json:"total_cpu_units" yaml:"total_cpu_units"`
	TotalMemoryMiB     int  `json:"total_memory_mib" yaml:"total_memory_mib"`
	AvailableMemoryMiB int  `json:"available_memory_mib" yaml:"available_memory_mib"`
	AllocatedCPUUnits  int  `json:"allocated_cpu_units" yaml:"allocated_cpu_units"`
	AllocatedMemoryMiB int  `json:"allocated_memory_mib" yaml:"allocated_memory_mib"`
	FreeDiskPercent    int  `json:"free_disk_percent" yaml:"free_disk_percent"`
	PoolRunning        int  `json:"pool_running" yaml:"pool_running"`
}

type ReservePolicy struct {
	MinimumCPUUnits        int
	MinimumMemoryMiB       int
	MinimumPercent         int
	MinimumFreeDiskPercent int
}

type Request struct {
	PoolName   string
	VCPU       int
	MemoryMiB  int
	MaxRunning int
}

type Decision struct {
	Admitted              bool   `json:"admitted"`
	Reason                Reason `json:"reason"`
	Pool                  string `json:"pool"`
	RequiredCPUReserve    int    `json:"required_cpu_reserve"`
	RequiredMemoryReserve int    `json:"required_memory_reserve_mib"`
	RemainingCPUUnits     int    `json:"remaining_cpu_units"`
	RemainingMemoryMiB    int    `json:"remaining_memory_mib"`
	RemainingAvailableMiB int    `json:"remaining_available_memory_mib"`
}

// Evaluate uses one schedulable CPU unit for every requested vCPU. A CPU unit
// is a physical core on dedicated hardware or one provider-guaranteed vCPU on
// the current virtual host. No SMT or local overcommit multiplier is assumed.
func Evaluate(snapshot HostSnapshot, policy ReservePolicy, request Request) (Decision, error) {
	if err := validate(snapshot, policy, request); err != nil {
		return Decision{}, err
	}

	cpuReserve := max(policy.MinimumCPUUnits, percentCeiling(snapshot.TotalCPUUnits, policy.MinimumPercent))
	memoryReserve := max(policy.MinimumMemoryMiB, percentCeiling(snapshot.TotalMemoryMiB, policy.MinimumPercent))
	remainingCPU := snapshot.TotalCPUUnits - snapshot.AllocatedCPUUnits - request.VCPU
	remainingMemory := snapshot.TotalMemoryMiB - snapshot.AllocatedMemoryMiB - request.MemoryMiB
	remainingAvailableMemory := snapshot.AvailableMemoryMiB - request.MemoryMiB

	decision := Decision{
		Pool:                  request.PoolName,
		RequiredCPUReserve:    cpuReserve,
		RequiredMemoryReserve: memoryReserve,
		RemainingCPUUnits:     remainingCPU,
		RemainingMemoryMiB:    remainingMemory,
		RemainingAvailableMiB: remainingAvailableMemory,
	}

	switch {
	case !snapshot.Healthy:
		decision.Reason = ReasonHostUnhealthy
	case snapshot.FreeDiskPercent < policy.MinimumFreeDiskPercent:
		decision.Reason = ReasonDiskPressure
	case snapshot.PoolRunning >= request.MaxRunning:
		decision.Reason = ReasonPoolSaturated
	case remainingCPU < cpuReserve:
		decision.Reason = ReasonInsufficientCPU
	case remainingMemory < memoryReserve || remainingAvailableMemory < memoryReserve:
		decision.Reason = ReasonInsufficientMemory
	default:
		decision.Admitted = true
		decision.Reason = ReasonAdmitted
	}
	return decision, nil
}

func validate(snapshot HostSnapshot, policy ReservePolicy, request Request) error {
	if snapshot.TotalCPUUnits <= 0 || snapshot.TotalMemoryMiB <= 0 || snapshot.AvailableMemoryMiB <= 0 {
		return fmt.Errorf("host totals must be positive")
	}
	if snapshot.AvailableMemoryMiB > snapshot.TotalMemoryMiB {
		return fmt.Errorf("available memory exceeds host capacity")
	}
	if snapshot.AllocatedCPUUnits < 0 || snapshot.AllocatedCPUUnits > snapshot.TotalCPUUnits {
		return fmt.Errorf("allocated CPU units are outside host capacity")
	}
	if snapshot.AllocatedMemoryMiB < 0 || snapshot.AllocatedMemoryMiB > snapshot.TotalMemoryMiB {
		return fmt.Errorf("allocated memory is outside host capacity")
	}
	if snapshot.FreeDiskPercent < 0 || snapshot.FreeDiskPercent > 100 {
		return fmt.Errorf("free disk percent must be between 0 and 100")
	}
	if snapshot.PoolRunning < 0 {
		return fmt.Errorf("pool running count cannot be negative")
	}
	if policy.MinimumCPUUnits < 0 || policy.MinimumMemoryMiB < 0 || policy.MinimumPercent < 0 || policy.MinimumPercent > 100 {
		return fmt.Errorf("reserve policy is invalid")
	}
	if policy.MinimumFreeDiskPercent < 0 || policy.MinimumFreeDiskPercent > 100 {
		return fmt.Errorf("disk reserve policy is invalid")
	}
	if request.PoolName == "" || request.VCPU <= 0 || request.MemoryMiB <= 0 || request.MaxRunning <= 0 {
		return fmt.Errorf("admission request is invalid")
	}
	return nil
}

func percentCeiling(total, percent int) int {
	return (total*percent + 99) / 100
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}
