package admission

import "fmt"

type Reason string

const (
	ReasonAdmitted            Reason = "admitted"
	ReasonHostUnhealthy       Reason = "host-unhealthy"
	ReasonDiskPressure        Reason = "disk-pressure"
	ReasonPoolSaturated       Reason = "pool-saturated"
	ReasonQueueIntent         Reason = "queue-intent"
	ReasonInsufficientCPU     Reason = "insufficient-cpu"
	ReasonInsufficientMemory  Reason = "insufficient-memory"
	ReasonPressureUnavailable Reason = "pressure-unavailable"
	ReasonCPUPressure         Reason = "cpu-pressure"
	ReasonMemoryPressure      Reason = "memory-pressure"
	ReasonRecentOOM           Reason = "recent-oom"
	ReasonDiagnosticWAL       Reason = "storage-high-watermark"
)

type HostSnapshot struct {
	Healthy            bool    `json:"healthy" yaml:"healthy"`
	TotalCPUUnits      int     `json:"total_cpu_units" yaml:"total_cpu_units"`
	TotalMemoryMiB     int     `json:"total_memory_mib" yaml:"total_memory_mib"`
	AvailableMemoryMiB int     `json:"available_memory_mib" yaml:"available_memory_mib"`
	AllocatedCPUUnits  int     `json:"allocated_cpu_units" yaml:"allocated_cpu_units"`
	AllocatedMemoryMiB int     `json:"allocated_memory_mib" yaml:"allocated_memory_mib"`
	FreeDiskPercent    int     `json:"free_disk_percent" yaml:"free_disk_percent"`
	PressureAvailable  bool    `json:"pressure_available" yaml:"pressure_available"`
	CPUSomeAvg10       float64 `json:"cpu_some_avg10" yaml:"cpu_some_avg10"`
	MemoryFullAvg10    float64 `json:"memory_full_avg10" yaml:"memory_full_avg10"`
	RecentOOMKills     int     `json:"recent_oom_kills" yaml:"recent_oom_kills"`
}

type ReservePolicy struct {
	MinimumCPUUnits int
	// MaximumFleetCPUPercent means aggregate CPU is enforced by the worker
	// cgroup. In that mode the admission ledger may commit all physical vCPU;
	// the cgroup, rather than stranded integer CPU units, preserves headroom.
	MaximumFleetCPUPercent int
	MinimumMemoryMiB       int
	MinimumPercent         int
	MinimumFreeDiskPercent int
	RequirePressure        bool
	MaxCPUSomeAvg10        float64
	MaxMemoryFullAvg10     float64
	MaxRecentOOMKills      int
}

type Request struct {
	PoolName  string
	VCPU      int
	MemoryMiB int
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
	if policy.MaximumFleetCPUPercent > 0 {
		cpuReserve = 0
	}
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
	case policy.RequirePressure && !snapshot.PressureAvailable:
		decision.Reason = ReasonPressureUnavailable
	case policy.RequirePressure && snapshot.PressureAvailable && snapshot.RecentOOMKills > policy.MaxRecentOOMKills:
		decision.Reason = ReasonRecentOOM
	case policy.RequirePressure && snapshot.PressureAvailable && snapshot.MemoryFullAvg10 > policy.MaxMemoryFullAvg10:
		decision.Reason = ReasonMemoryPressure
	case policy.RequirePressure && snapshot.PressureAvailable && snapshot.CPUSomeAvg10 > policy.MaxCPUSomeAvg10:
		decision.Reason = ReasonCPUPressure
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
	if snapshot.CPUSomeAvg10 < 0 || snapshot.CPUSomeAvg10 > 100 || snapshot.MemoryFullAvg10 < 0 || snapshot.MemoryFullAvg10 > 100 || snapshot.RecentOOMKills < 0 {
		return fmt.Errorf("pressure snapshot is invalid")
	}
	if policy.MinimumCPUUnits < 0 || policy.MinimumMemoryMiB < 0 || policy.MinimumPercent < 0 || policy.MinimumPercent > 100 {
		return fmt.Errorf("reserve policy is invalid")
	}
	if policy.MaximumFleetCPUPercent < 0 || policy.MaximumFleetCPUPercent > 98 {
		return fmt.Errorf("aggregate CPU policy is invalid")
	}
	if policy.MinimumFreeDiskPercent < 0 || policy.MinimumFreeDiskPercent > 100 {
		return fmt.Errorf("disk reserve policy is invalid")
	}
	if policy.MaxCPUSomeAvg10 < 0 || policy.MaxCPUSomeAvg10 > 100 || policy.MaxMemoryFullAvg10 < 0 || policy.MaxMemoryFullAvg10 > 100 || policy.MaxRecentOOMKills < 0 {
		return fmt.Errorf("pressure policy is invalid")
	}
	if request.PoolName == "" || request.VCPU <= 0 || request.MemoryMiB <= 0 {
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
