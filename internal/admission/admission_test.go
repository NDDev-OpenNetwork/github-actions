package admission

import "testing"

func TestEvaluateAdmitsWithinReserves(t *testing.T) {
	t.Parallel()

	decision, err := Evaluate(
		HostSnapshot{
			Healthy:            true,
			TotalCPUUnits:      32,
			TotalMemoryMiB:     128 * 1024,
			AvailableMemoryMiB: 96 * 1024,
			AllocatedCPUUnits:  8,
			AllocatedMemoryMiB: 24 * 1024,
			FreeDiskPercent:    60,
			PoolRunning:        1,
		},
		ReservePolicy{
			MinimumCPUUnits:        4,
			MinimumMemoryMiB:       16 * 1024,
			MinimumPercent:         10,
			MinimumFreeDiskPercent: 20,
		},
		Request{PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 12 * 1024, MaxRunning: 4},
	)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if !decision.Admitted || decision.Reason != ReasonAdmitted {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestEvaluateFailsClosedUnderDiskPressure(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	snapshot.FreeDiskPercent = 19
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Admitted || decision.Reason != ReasonDiskPressure {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestEvaluatePreservesCPUReserve(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	snapshot.TotalCPUUnits = 16
	snapshot.AllocatedCPUUnits = 10
	request.VCPU = 4
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Admitted || decision.Reason != ReasonInsufficientCPU {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func TestEvaluateEnforcesPoolLimitBeforeCapacity(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	snapshot.PoolRunning = request.MaxRunning
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Admitted || decision.Reason != ReasonPoolSaturated {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func validInputs() (HostSnapshot, ReservePolicy, Request) {
	return HostSnapshot{
			Healthy:            true,
			TotalCPUUnits:      32,
			TotalMemoryMiB:     128 * 1024,
			AvailableMemoryMiB: 128 * 1024,
			FreeDiskPercent:    50,
		}, ReservePolicy{
			MinimumCPUUnits:        4,
			MinimumMemoryMiB:       16 * 1024,
			MinimumPercent:         10,
			MinimumFreeDiskPercent: 20,
		}, Request{
			PoolName:   "nddev-linux-standard",
			VCPU:       4,
			MemoryMiB:  12 * 1024,
			MaxRunning: 4,
		}
}

func TestEvaluatePreservesLiveAvailableMemoryReserve(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	snapshot.AvailableMemoryMiB = request.MemoryMiB + policy.MinimumMemoryMiB - 1
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if decision.Admitted || decision.Reason != ReasonInsufficientMemory {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}
