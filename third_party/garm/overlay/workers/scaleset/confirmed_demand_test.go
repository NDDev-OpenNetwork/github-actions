package scaleset

import (
	"context"
	"testing"
	"time"

	"github.com/cloudbase/garm/params"
)

func TestConfirmedDemandUnknownSessionFailsClosed(t *testing.T) {
	local := params.ScaleSet{Enabled: true, MaxRunners: 8, DesiredRunnerCount: 0}
	var gate confirmedDemandGate
	target, err := gate.target(context.Background(), time.Now(), liveMessageDemand{}, local, 0, 4)
	if err == nil || target != 0 {
		t.Fatalf("unknown demand allocated capacity: target=%d err=%v", target, err)
	}
	target, err = gate.target(context.Background(), time.Now(), liveMessageDemand{sessionID: "old", assigned: 0, observed: false}, local, 1, 4)
	if err == nil || target != 1 {
		t.Fatalf("unobserved stale zero allocated capacity: target=%d err=%v", target, err)
	}
}

func TestConfirmedDemandUsesLiveMessageStatistics(t *testing.T) {
	now := time.Now()
	var gate confirmedDemandGate
	local := params.ScaleSet{ScaleSetID: 5, Name: "example-integration", Enabled: true, MaxRunners: 8, DesiredRunnerCount: 9}
	target, err := gate.target(context.Background(), now, liveMessageDemand{sessionID: "sess-1", assigned: 0, observed: true}, local, 0, 4)
	if err != nil || target != 0 {
		t.Fatalf("live zero allocated capacity: target=%d err=%v", target, err)
	}
	target, err = gate.target(context.Background(), now, liveMessageDemand{sessionID: "sess-1", assigned: 3, observed: true}, local, 0, 4)
	if err != nil || target != 3 {
		t.Fatalf("live positive demand did not reopen: target=%d err=%v", target, err)
	}
}

func TestConfirmedDemandPreservesAdmissionAndMaximum(t *testing.T) {
	for _, tc := range []struct {
		admitted, maximum, current, assigned, want int
	}{
		{2, 8, 0, 3, 2},
		{5, 1, 0, 3, 1},
		{0, 8, 0, 3, 0},
		{2, 8, 2, 3, 2},
		{4, 8, 5, 3, 5},
		{4, 8, 0, 0, 0},
	} {
		local := params.ScaleSet{Enabled: true, MaxRunners: uint(tc.maximum), DesiredRunnerCount: 99}
		var gate confirmedDemandGate
		demand := liveMessageDemand{sessionID: "sess-1", assigned: tc.assigned, observed: true}
		target, err := gate.target(context.Background(), time.Now(), demand, local, tc.current, tc.admitted)
		if err != nil || target != tc.want {
			t.Fatalf("%+v: target=%d err=%v", tc, target, err)
		}
	}
}

func TestConfirmedDemandInvalidCountFailsClosed(t *testing.T) {
	local := params.ScaleSet{Enabled: true, MaxRunners: 8}
	var gate confirmedDemandGate
	target, err := gate.target(context.Background(), time.Now(), liveMessageDemand{sessionID: "sess-1", assigned: -1, observed: true}, local, 1, 4)
	if err == nil || target != 1 {
		t.Fatalf("invalid message demand permitted create: target=%d err=%v", target, err)
	}
}

func TestIdleFreshRequiresRecentMessageNotSessionCreate(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	sessionCreate := liveMessageDemand{sessionID: "sess-1", assigned: 0, observed: true, messageID: 0, observedAt: now}
	if sessionCreate.idleFresh(now) {
		t.Fatal("session-create zero with messageID 0 must not authorize idle retirement")
	}
	if !sessionCreate.scalingKnown() {
		t.Fatal("session-create counter must remain usable for ordinary scaling")
	}
	stale := liveMessageDemand{sessionID: "sess-1", assigned: 0, observed: true, messageID: 9, observedAt: now.Add(-5 * time.Minute)}
	if stale.idleFresh(now) {
		t.Fatal("a five-minute-old zero must not be reused as two fresh idle observations")
	}
	fresh := liveMessageDemand{sessionID: "sess-1", assigned: 0, observed: true, messageID: 9, observedAt: now.Add(-30 * time.Second)}
	if !fresh.idleFresh(now) {
		t.Fatal("a recent MESSAGE zero must be usable for idle retirement")
	}
	later := liveMessageDemand{sessionID: "sess-1", assigned: 0, observed: true, messageID: 10, observedAt: now, generation: 2}
	if later.unchangedIdleZero(fresh, now) {
		t.Fatal("a changed generation must not count as the same idle observation")
	}
	if !fresh.unchangedIdleZero(fresh, now) {
		t.Fatal("the same fresh zero must hold through removal")
	}
	started := fresh
	started.assigned = 1
	started.messageID = 10
	started.generation = 3
	started.observedAt = now
	if started.unchangedIdleZero(fresh, now) {
		t.Fatal("JobStarted demand must refuse idle removal")
	}
	changedBeforeRemove := fresh
	changedBeforeRemove.messageID = 11
	changedBeforeRemove.generation = 4
	changedBeforeRemove.observedAt = now
	if changedBeforeRemove.unchangedIdleZero(fresh, now) {
		t.Fatal("demand that changed after the runner lock must not authorize RemoveRunner")
	}
	if sessionCreate.unchangedIdleZero(fresh, now) {
		t.Fatal("messageID 0 must not confirm idle removal")
	}
}

func TestConfirmedDemandSessionCreateZeroDoesNotExpireScaling(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	var gate confirmedDemandGate
	local := params.ScaleSet{Enabled: true, MaxRunners: 8}
	demand := liveMessageDemand{sessionID: "sess-1", assigned: 3, observed: true, messageID: 0, observedAt: now.Add(-time.Hour)}
	target, err := gate.target(context.Background(), now, demand, local, 0, 4)
	if err != nil || target != 3 {
		t.Fatalf("aged scaling counter must not be expired: target=%d err=%v", target, err)
	}
}
