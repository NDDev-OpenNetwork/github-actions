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

func TestEvaluateUsesAggregateQuotaInsteadOfStrandingIntegerCPUUnits(t *testing.T) {
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
	if !withQuota.Admitted || withQuota.RemainingCPUUnits != 0 || withQuota.RequiredCPUReserve != 0 {
		t.Fatalf("aggregate quota did not admit bounded full-vCPU commitment: %#v", withQuota)
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
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot, policy, request := validInputs()
			policy.RequirePressure = true
			policy.MaxCPUSomeAvg10 = 20
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
	policy.MaxMemoryFullAvg10 = 1
	decision, err := Evaluate(snapshot, policy, request)
	if err != nil || !decision.Admitted {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
}
