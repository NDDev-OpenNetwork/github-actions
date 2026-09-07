package scaleset

import (
	"testing"
	"time"

	"github.com/cloudbase/garm/params"
)

func TestActionsListBusyDefaultIsUntrusted(t *testing.T) {
	t.Parallel()
	var encoded params.RunnerReference
	if encoded.Busy {
		t.Fatal("zero RunnerReference.Busy must stay false so omitted JSON cannot look busy")
	}
	actionsListBusyUntrusted(encoded.Busy)
}

func TestRestBusyOmittedIsUnknown(t *testing.T) {
	t.Parallel()
	if restBusyFromPointer(nil) != restBusyUnknown {
		t.Fatal("omitted REST busy must be unknown, not false")
	}
	busy := false
	if restBusyFromPointer(&busy) != restBusyFalse {
		t.Fatal("explicit false must be distinct from omitted")
	}
	busy = true
	if restBusyFromPointer(&busy) != restBusyTrue {
		t.Fatal("explicit true must refuse retirement")
	}
}

func baseEvidence(now time.Time) idleRetirementEvidence {
	busy := false
	return idleRetirementEvidence{
		AgentID:           42,
		Name:              "example-runner",
		ScaleSetID:        7,
		LocalStatus:       params.RunnerPending,
		CreatedAt:         now.Add(-45 * time.Minute),
		MinIdle:           0,
		IdleCount:         4,
		Now:               now,
		RESTID:            42,
		RESTName:          "example-runner",
		RESTStatus:        "online",
		RESTBusy:          &busy,
		ActionsID:         42,
		ActionsName:       "example-runner",
		ActionsScaleSetID: 7,
		ActionsEphemeral:  true,
		ActionsEnabled:    true,
		ActionsState:      "Provisioned",
		ActionsStatus:     "online",
		StatisticsPresent: true,
	}
}

func TestEvaluateIdleRetirementPositive(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	got := evaluateIdleRetirement(baseEvidence(now))
	if !got.Eligible || got.Reason != "eligible" {
		t.Fatalf("got %#v", got)
	}
}

func TestEvaluateIdleRetirementRefusesOmittedBusy(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	ev.RESTBusy = nil
	got := evaluateIdleRetirement(ev)
	if got.Eligible || got.Reason != "rest-busy-omitted" {
		t.Fatalf("got %#v", got)
	}
}

func TestEvaluateIdleRetirementRefusesBusyTrue(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	busy := true
	ev.RESTBusy = &busy
	got := evaluateIdleRetirement(ev)
	if got.Eligible || got.Reason != "rest-busy" {
		t.Fatalf("got %#v", got)
	}
}

func TestEvaluateIdleRetirementStatsZeroIsNotABlocker(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	ev.AssignedJobs, ev.AcquiredJobs, ev.AvailableJobs, ev.RunningJobs = 0, 0, 0, 0
	got := evaluateIdleRetirement(ev)
	if !got.Eligible {
		t.Fatalf("all-zero statistics must not prove demand and must not block REST-idle retirement: %#v", got)
	}
}

func TestEvaluateIdleRetirementRefusesPositiveDemand(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	ev.AssignedJobs = 1
	got := evaluateIdleRetirement(ev)
	if got.Eligible || got.Reason != "scale-set-has-demand" {
		t.Fatalf("got %#v", got)
	}
}

func TestEvaluateIdleRetirementRefusesIdentityChange(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	ev.RESTName = "example-other"
	got := evaluateIdleRetirement(ev)
	if got.Eligible || got.Reason != "identity-mismatch" {
		t.Fatalf("got %#v", got)
	}
}

func TestEvaluateIdleRetirementRespectsMinIdle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	ev := baseEvidence(now)
	ev.MinIdle = 4
	ev.IdleCount = 4
	got := evaluateIdleRetirement(ev)
	if got.Eligible || got.Reason != "at-or-below-min-idle" {
		t.Fatalf("got %#v", got)
	}
}

func TestIdleRetirementGateRequiresTwoObservations(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	var gate idleRetirementGate
	first := gate.confirm(baseEvidence(now))
	if first.Ready || first.Reason != "first-observation" {
		t.Fatalf("first %#v", first)
	}
	tooSoon := gate.confirm(baseEvidence(now.Add(10 * time.Second)))
	if tooSoon.Ready || tooSoon.Reason != "confirmation-pending" {
		t.Fatalf("too soon %#v", tooSoon)
	}
	ready := gate.confirm(baseEvidence(now.Add(idleRetirementConfirmAfter)))
	if !ready.Ready || ready.Reason != "confirmed" {
		t.Fatalf("confirmed %#v", ready)
	}
}

func TestIdleRetirementGateDropsChangedIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	var gate idleRetirementGate
	gate.confirm(baseEvidence(now))
	changed := baseEvidence(now.Add(time.Minute))
	changed.RESTName = "example-replaced"
	changed.Name = "example-replaced"
	changed.ActionsName = "example-replaced"
	got := gate.confirm(changed)
	if got.Ready || got.Reason != "first-observation" {
		t.Fatalf("replacement must restart observation: %#v", got)
	}
}

func TestIsConflictError(t *testing.T) {
	t.Parallel()
	if !isConflictError(errString("conflict while calling agents/42: JobStillRunningException")) {
		t.Fatal("JobStillRunningException must refuse retirement")
	}
	if isConflictError(errString("not found")) {
		t.Fatal("not-found is not a busy conflict")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
