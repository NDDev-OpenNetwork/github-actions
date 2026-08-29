package admission

import (
	"strings"
	"testing"
)

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
		},
		ReservePolicy{
			MinimumCPUUnits:        4,
			MinimumMemoryMiB:       16 * 1024,
			MinimumPercent:         10,
			MinimumFreeDiskPercent: 20,
		},
		Request{PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 12 * 1024},
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

func TestEvaluateTreatsAllocationsAbovePressureEligibleCapacityAsSaturation(t *testing.T) {
	t.Parallel()
	snapshot, policy, request := validInputs()
	snapshot.TotalCPUUnits = 8
	snapshot.AllocatedCPUUnits = 12
	snapshot.TotalMemoryMiB = 16 * 1024
	snapshot.AvailableMemoryMiB = 16 * 1024
	snapshot.AllocatedMemoryMiB = 20 * 1024
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("dynamic capacity contraction became a provider error: %v", err)
	}
	if decision.Admitted || decision.Reason != ReasonInsufficientCPU {
		t.Fatalf("contracted capacity decision = %#v", decision)
	}
}

func TestEvaluateUsesAggregateQuotaAsCPUAllowanceEnvelope(t *testing.T) {
	t.Parallel()
	snapshot, policy, request := validInputs()
	snapshot.TotalCPUUnits = 8
	snapshot.AllocatedCPUUnits = 4
	request.VCPU = 4
	withoutQuota, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if withoutQuota.Admitted || withoutQuota.Reason != ReasonInsufficientCPU {
		t.Fatalf("integer reserve unexpectedly admitted the full host: %#v", withoutQuota)
	}
	policy.MaximumFleetCPUPercent = 90
	withQuota, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if withQuota.Admitted || withQuota.Reason != ReasonCPUAllowance || withQuota.RequiredCPUReserve != 0 {
		t.Fatalf("aggregate quota did not bound declared CPU allowance: %#v", withQuota)
	}
}

func TestEvaluateBlocksBurstBeforePSIFeedbackCatchesUp(t *testing.T) {
	t.Parallel()
	snapshot, policy, request := validInputs()
	policy.MaximumFleetCPUPercent = 98
	snapshot.TotalCPUUnits = 32
	snapshot.AllocatedCPUUnits = 8
	snapshot.AllocatedCPUAllowanceUnits = 28
	request.VCPU = 1
	request.CPUAllowanceUnits = 4
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Admitted || decision.Reason != ReasonCPUAllowance || decision.RemainingCPUAllowanceUnits >= 0 {
		t.Fatalf("soft allowance burst was not blocked: %#v", decision)
	}
	if !strings.Contains(string(decision.Reason), "insufficient-cpu") {
		t.Fatalf("CPU allowance rejection is not classifiable as capacity: %q", decision.Reason)
	}
}

func TestEvaluateUsesOneLedgerAcrossCapabilityNames(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	standard, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	request.PoolName = "nddev-linux-integration"
	integration, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatalf("evaluate integration: %v", err)
	}
	if standard.Admitted != integration.Admitted || standard.RemainingCPUUnits != integration.RemainingCPUUnits ||
		standard.RemainingMemoryMiB != integration.RemainingMemoryMiB {
		t.Fatalf("capability name partitioned the shared ledger: standard=%#v integration=%#v", standard, integration)
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
			PoolName:  "nddev-linux-standard",
			VCPU:      4,
			MemoryMiB: 12 * 1024,
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

func TestEvaluatePressurePolicyFailsClosed(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*HostSnapshot)
		reason Reason
	}{
		{name: "unavailable", mutate: func(*HostSnapshot) {}, reason: ReasonPressureUnavailable},
		{name: "recent OOM", mutate: func(s *HostSnapshot) { s.PressureAvailable = true; s.RecentOOMKills = 1 }, reason: ReasonRecentOOM},
		{name: "memory full", mutate: func(s *HostSnapshot) { s.PressureAvailable = true; s.MemoryFullAvg10 = 1.1 }, reason: ReasonMemoryPressure},
		{name: "CPU some", mutate: func(s *HostSnapshot) { s.PressureAvailable = true; s.CPUSomeAvg10 = 20.1 }, reason: ReasonCPUPressure},
		{name: "I/O full", mutate: func(s *HostSnapshot) { s.PressureAvailable = true; s.IOFullAvg10 = 5.1 }, reason: ReasonIOPressure},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, policy, request := validInputs()
			policy.RequirePressure = true
			policy.MaxCPUSomeAvg10 = 20
			policy.MaxIOFullAvg10 = 5
			policy.MaxMemoryFullAvg10 = 1
			policy.MaxRecentOOMKills = 0
			test.mutate(&snapshot)
			decision, err := Evaluate(snapshot, policy, request)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Admitted || decision.Reason != test.reason {
				t.Fatalf("decision=%#v, want %q", decision, test.reason)
			}
		})
	}
}

func TestEvaluatePressurePolicyAdmitsBelowThresholds(t *testing.T) {
	t.Parallel()
	snapshot, policy, request := validInputs()
	snapshot.PressureAvailable = true
	snapshot.CPUSomeAvg10 = 19.9
	snapshot.MemoryFullAvg10 = 0.9
	policy.RequirePressure = true
	policy.MaxCPUSomeAvg10 = 20
	policy.MaxIOFullAvg10 = 5
	policy.MaxMemoryFullAvg10 = 1
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil || !decision.Admitted {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}

// The fleet's own numbers: four members declaring ten CPU units each, an
// allowance envelope of 98%, and the priority integration class whose declared
// share is four units against a measured p90 of 0.72 cores. Strict one-to-one
// stops that class at nine workers cluster-wide while the members sit near
// idle; the multiplier is what lets the memory ledger and PSI decide instead.
func TestEvaluateOvercommitAdmitsWhatStrictShareAccountingRefuses(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	policy.MaximumFleetCPUPercent = 98
	snapshot.TotalCPUUnits = 40
	snapshot.AllocatedCPUUnits = 16
	snapshot.AllocatedCPUAllowanceUnits = 36
	request.VCPU = 2
	request.CPUAllowanceUnits = 4

	strict, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if strict.Admitted || strict.Reason != ReasonCPUAllowance {
		t.Fatalf("strict share accounting did not refuse the tenth worker: %#v", strict)
	}

	policy.CPUAllowanceOvercommit = 3
	overcommitted, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if !overcommitted.Admitted || overcommitted.Reason != ReasonAdmitted {
		t.Fatalf("measured overcommit did not admit the same worker: %#v", overcommitted)
	}
	if overcommitted.RemainingCPUAllowanceUnits <= strict.RemainingCPUAllowanceUnits {
		t.Fatalf("overcommit did not widen the envelope: strict=%d overcommitted=%d",
			strict.RemainingCPUAllowanceUnits, overcommitted.RemainingCPUAllowanceUnits)
	}
}

// The multiplier applies to the share and to nothing else. The reservation
// ledger is per-worker measured consumption and stays the bound it was, so a
// fleet whose reservations are spent is still refused however wide the share
// envelope is opened.
func TestEvaluateOvercommitLeavesTheReservationLedgerBinding(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	policy.MaximumFleetCPUPercent = 98
	policy.CPUAllowanceOvercommit = 4
	snapshot.TotalCPUUnits = 40
	snapshot.AllocatedCPUUnits = 39
	snapshot.AllocatedCPUAllowanceUnits = 0
	request.VCPU = 2
	request.CPUAllowanceUnits = 4

	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Admitted || decision.Reason != ReasonInsufficientCPU {
		t.Fatalf("overcommit leaked into the reservation ledger: %#v", decision)
	}
}

// A policy written before the field existed keeps refusing exactly what it
// refused, so the deployment order between binary and configuration is free.
func TestEvaluateUnsetOvercommitKeepsStrictShareAccounting(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	policy.MaximumFleetCPUPercent = 98
	snapshot.TotalCPUUnits = 40
	snapshot.AllocatedCPUUnits = 16
	snapshot.AllocatedCPUAllowanceUnits = 36
	request.VCPU = 2
	request.CPUAllowanceUnits = 4

	decision, err := Evaluate(snapshot, policy, request)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Admitted || decision.Reason != ReasonCPUAllowance {
		t.Fatalf("unset overcommit changed the previous decision: %#v", decision)
	}
}

func TestEvaluateRejectsOvercommitBeyondTheMeasuredCeiling(t *testing.T) {
	t.Parallel()

	snapshot, policy, request := validInputs()
	policy.MaximumFleetCPUPercent = 98
	policy.CPUAllowanceOvercommit = 5
	if _, err := Evaluate(snapshot, policy, request); err == nil {
		t.Fatal("an overcommit past the measured p99 was accepted")
	}
}
