package scaleset

import (
	"context"
	"errors"
	"testing"
	"time"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	commonParams "github.com/cloudbase/garm-provider-common/params"
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

func TestIdleRetirementLocalPlanSkipsNetworkWhenNothingIsAged(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	runners := map[string]params.Instance{
		"example-young": {
			Name: "example-young", AgentID: 7, Status: commonParams.InstanceRunning,
			RunnerStatus: params.RunnerIdle, CreatedAt: now.Add(-time.Minute),
		},
		"example-creating": {
			Name: "example-creating", AgentID: 8, Status: commonParams.InstanceCreating,
			RunnerStatus: params.RunnerPending, CreatedAt: now.Add(-time.Hour),
		},
	}
	idleCount, aged := idleRetirementLocalPlan(runners, 0, now)
	if idleCount != 1 || len(aged) != 0 {
		t.Fatalf("idleCount=%d aged=%d", idleCount, len(aged))
	}
}

func TestIdleRetirementGatePrunesUnseenIDs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	var gate idleRetirementGate
	gate.confirm(baseEvidence(now))
	gone := baseEvidence(now)
	gone.AgentID = 99
	gone.RESTID = 99
	gone.ActionsID = 99
	gate.confirm(gone)
	gate.prune(map[int64]struct{}{42: {}})
	if _, ok := gate.seen[99]; ok {
		t.Fatal("unseen agent ID must not keep a first observation")
	}
	if _, ok := gate.seen[42]; !ok {
		t.Fatal("live agent ID must keep its first observation")
	}
}

func TestIdleRetirementReadErrorResetsConfirmation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	var gate idleRetirementGate
	gate.confirm(baseEvidence(now))
	gate.forget(42)
	again := gate.confirm(baseEvidence(now.Add(time.Minute)))
	if again.Ready || again.Reason != "first-observation" {
		t.Fatalf("failed freshness must not keep the old first observation: %#v", again)
	}
}

func TestWarnIdleRetirementDoesNotReturn(t *testing.T) {
	t.Parallel()
	// Helper evidence only: production consolidateRunnerState calls
	// warnIdleRetirement then keeps the original body. This does not construct
	// a Worker or exercise DB/provider consolidation.
	warnIdleRetirement(context.Background(), errors.New("Actions GetRunner 500"))
	warnIdleRetirement(context.Background(), nil)
}

func TestRetireExcessIdleCapacityNoAgedCandidatesMakesNoClientCall(t *testing.T) {
	now := time.Now().UTC()
	w := &Worker{
		ctx:      context.Background(),
		scaleSet: params.ScaleSet{MinIdleRunners: 0, ScaleSetID: 7, Name: "example-integration"},
		runners: map[string]params.Instance{
			"example-young": {
				Name: "example-young", AgentID: 7, Status: commonParams.InstanceRunning,
				RunnerStatus: params.RunnerIdle, CreatedAt: now.Add(-time.Minute),
			},
		},
	}
	w.idleRetire.seen = map[int64]idleObservation{99: {AgentID: 99, Name: "example-gone", ScaleSetID: 7, FirstSeen: now.Add(-time.Hour)}}
	if err := w.retireExcessIdleCapacity(); err != nil {
		t.Fatalf("empty aged-candidate path must not call GitHub/Actions clients: %v", err)
	}
	if _, ok := w.idleRetire.seen[99]; ok {
		t.Fatal("unseen observation must be pruned without a network call")
	}
}

func TestDecideIdleRemoveRunnerHTTPSequence(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		removeErr error
		getErr    error
		getFound  bool
		want      idleRemoveOutcome
		wantErr   bool
	}{
		{name: "204-then-404", getErr: runnerErrors.ErrNotFound, want: idleRemoveMarkAbsent},
		{name: "remove-already-404", removeErr: runnerErrors.ErrNotFound, want: idleRemoveMarkAbsent},
		{name: "409-job-still-running", removeErr: errString("conflict: JobStillRunningException"), want: idleRemoveConflict},
		{name: "unexpected-200", removeErr: errString("removing runner 42: unexpected status 200"), wantErr: true},
		{name: "remove-500", removeErr: errString("removing runner 42: unexpected status 500"), wantErr: true},
		{name: "missing-readback", getErr: errors.New("timeout"), wantErr: true},
		{name: "non-404-readback-still-present", getFound: true, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decideIdleRemoveRunner(tc.removeErr, tc.getErr, tc.getFound)
			if tc.wantErr {
				if err == nil || got != idleRemoveNone {
					t.Fatalf("got %s err=%v", got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %s err=%v want %s", got, err, tc.want)
			}
		})
	}
}

type fakeIdleActions struct {
	removeErr error
	getErr    error
	removed   int
	got       int
}

func (f *fakeIdleActions) RemoveRunner(context.Context, int64) error {
	f.removed++
	return f.removeErr
}

func (f *fakeIdleActions) GetRunner(context.Context, int64) (params.RunnerReference, error) {
	f.got++
	if f.getErr != nil {
		return params.RunnerReference{}, f.getErr
	}
	return params.RunnerReference{ID: 42, Name: "example-runner"}, nil
}

func TestApplyIdleRemoveRunnerWires204And404Once(t *testing.T) {
	t.Parallel()
	cli := &fakeIdleActions{getErr: runnerErrors.ErrNotFound}
	got, err := applyIdleRemoveRunner(context.Background(), cli, 42)
	if err != nil || got != idleRemoveMarkAbsent || cli.removed != 1 || cli.got != 1 {
		t.Fatalf("got %s err=%v removed=%d got=%d", got, err, cli.removed, cli.got)
	}
}

func TestApplyIdleRemoveRunnerConflictDoesNotReadBack(t *testing.T) {
	t.Parallel()
	cli := &fakeIdleActions{removeErr: errString("conflict: JobStillRunningException")}
	got, err := applyIdleRemoveRunner(context.Background(), cli, 42)
	if err != nil || got != idleRemoveConflict || cli.removed != 1 || cli.got != 0 {
		t.Fatalf("got %s err=%v removed=%d got=%d", got, err, cli.removed, cli.got)
	}
}

func TestApplyConfirmedIdleRemovalPendingDeleteOnce(t *testing.T) {
	// Helper evidence only: fake Actions client plus injected writer, not a
	// Worker, database, or live HTTP transport.
	var marked []string
	runner := params.Instance{Name: "example-runner", AgentID: 42}
	cli := &fakeIdleActions{getErr: runnerErrors.ErrNotFound}
	outcome, err := applyConfirmedIdleRemoval(context.Background(), cli, runner, func(got params.Instance) error {
		marked = append(marked, got.Name)
		return nil
	})
	if err != nil || outcome != idleRemoveMarkAbsent || len(marked) != 1 || marked[0] != "example-runner" || cli.removed != 1 || cli.got != 1 {
		t.Fatalf("outcome=%s err=%v marked=%v removed=%d got=%d", outcome, err, marked, cli.removed, cli.got)
	}
}

func TestApplyConfirmedIdleRemovalConflictDoesNotPendingDelete(t *testing.T) {
	var marked []string
	cli := &fakeIdleActions{removeErr: errString("conflict: JobStillRunningException")}
	outcome, err := applyConfirmedIdleRemoval(context.Background(), cli, params.Instance{Name: "example-runner", AgentID: 42}, func(got params.Instance) error {
		marked = append(marked, got.Name)
		return nil
	})
	if err != nil || outcome != idleRemoveConflict || len(marked) != 0 || cli.got != 0 {
		t.Fatalf("conflict must not pending-delete: outcome=%s err=%v marked=%v got=%d", outcome, err, marked, cli.got)
	}
}

func TestApplyConfirmedIdleRemovalRefusesUnexpectedStatus(t *testing.T) {
	for _, tc := range []struct {
		name      string
		removeErr error
		getErr    error
	}{
		{name: "unexpected-200", removeErr: errString("removing runner 42: unexpected status 200")},
		{name: "remove-500", removeErr: errString("removing runner 42: unexpected status 500")},
		{name: "non-404-readback", getErr: errors.New("timeout")},
		{name: "still-present", getErr: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var marked []string
			cli := &fakeIdleActions{removeErr: tc.removeErr, getErr: tc.getErr}
			_, err := applyConfirmedIdleRemoval(context.Background(), cli, params.Instance{Name: "example-runner", AgentID: 42}, func(got params.Instance) error {
				marked = append(marked, got.Name)
				return nil
			})
			if err == nil || len(marked) != 0 {
				t.Fatalf("err=%v marked=%v", err, marked)
			}
		})
	}
}

type errString string

func (e errString) Error() string { return string(e) }
