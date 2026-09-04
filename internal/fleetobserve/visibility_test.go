package fleetobserve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

// A drain-marked offline member makes the instance listing partial by design:
// its residents are unobservable, so gap counts are unattributable and must
// not page — measured live on 2026-09-01, when a trim window's stopped incus
// turned one missing instance and seven uncovered intents into
// fleet_platform_unhealthy and lifecycle_inventory_gap pages about a member
// that was exactly where its operator put it.
func TestHeldOutMemberSuppressesGapsWithoutHidingThem(t *testing.T) {
	collector := healthyCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{
			{Name: "gha-runner-1", Online: true},
			{Name: "gha-runner-3", Online: false, DrainReason: "drained: thin-pool trim"},
		}, nil
	}
	// The journal expects runner-one, but the partial listing does not show it.
	collector.Instances = func(context.Context) ([]string, error) {
		return []string{}, nil
	}
	snapshot := collector.Collect(context.Background())
	if len(snapshot.HeldOutMembers) != 1 || snapshot.HeldOutMembers[0] != "gha-runner-3" {
		t.Fatalf("held out = %v", snapshot.HeldOutMembers)
	}
	if snapshot.Incus.MissingInstances != 0 {
		t.Fatalf("a gap paged during a marked hold: %#v", snapshot.Incus)
	}
	// Suppressed is not hidden: the raw count survives as unattributable.
	if snapshot.Incus.MissingUnattributable != 1 && snapshot.Incus.MissingCreatedWithinGrace != 1 {
		t.Fatalf("the unattributable gap vanished entirely: %#v", snapshot.Incus)
	}
	if !snapshot.Healthy {
		t.Fatalf("a marked maintenance hold must not fail the platform: %v", snapshot.CollectionErrors)
	}
}

// The suppression must never extend to an offline member nobody drained:
// that is an incident, and it fails the platform loudly.
func TestOfflineMemberWithoutADrainFailsCollection(t *testing.T) {
	collector := healthyCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{{Name: "gha-runner-2", Online: false}}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy {
		t.Fatal("an undrained offline member left the platform healthy")
	}
	found := false
	for _, message := range snapshot.CollectionErrors {
		if strings.Contains(message, "gha-runner-2") && strings.Contains(message, "offline without a drain") {
			found = true
		}
	}
	if !found {
		t.Fatalf("collection errors do not name the member: %v", snapshot.CollectionErrors)
	}
	if len(snapshot.HeldOutMembers) != 0 {
		t.Fatalf("an undrained member was treated as held out: %v", snapshot.HeldOutMembers)
	}
}

// Online members change nothing: the healthy fixture stays byte-identical in
// behavior with the member source present.
func TestOnlineMembersLeaveTheSnapshotUntouched(t *testing.T) {
	collector := healthyCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{
			{Name: "gha-runner-1", Online: true},
			{Name: "gha-runner-2", Online: true},
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || len(snapshot.HeldOutMembers) != 0 || len(snapshot.CollectionErrors) != 0 {
		t.Fatalf("online members disturbed the snapshot: %#v", snapshot)
	}
}

// A daemon that is deliberately down can fail the whole cluster listing
// loudly. While its member is held out, that loudness is maintenance: the
// error moves to ListingUnavailable, the platform stays healthy, and the
// degraded-visibility rule guards the window. Without a hold the same
// failure pages exactly as before.
func TestListingFailureIsMaintenanceOnlyUnderAHold(t *testing.T) {
	collector := healthyCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{{Name: "gha-runner-4", Online: false, DrainReason: "drained: proof"}}, nil
	}
	collector.Instances = func(context.Context) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy {
		t.Fatalf("held-out listing failure failed the platform: %v", snapshot.CollectionErrors)
	}
	if snapshot.Incus.ListingUnavailable == "" {
		t.Fatal("the listing failure vanished instead of being attributed")
	}

	loud := healthyCollector(t)
	loud.Instances = func(context.Context) ([]string, error) {
		return nil, context.DeadlineExceeded
	}
	snapshot = loud.Collect(context.Background())
	if snapshot.Healthy {
		t.Fatal("an unattributed listing failure left the platform healthy")
	}
}

func strandedUncoveredCollector(t *testing.T) Collector {
	t.Helper()
	collector := healthyCollector(t)
	collector.Journal = func(context.Context) (providerjournal.Journal, error) {
		return providerjournal.Journal{
			SchemaVersion: providerjournal.SchemaVersion,
			Leases:        map[string]providerjournal.Lease{},
			Claims:        map[string]providerjournal.Claim{},
		}, nil
	}
	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }
	collector.Queue = func(context.Context) (queueintent.Snapshot, error) {
		return queueintent.Snapshot{Active: []queueintent.Intent{{
			Key: "stuck-running", ScaleSetID: 1,
			JobID: "da4c5ef8-1e3a-54a9-ab12-14c34f8dfd71", ScaleSetName: "nddev-linux-standard",
			Repository: "owner/repo", RunnerName: "nddev-stranded",
			QueueTime: observationTime.Add(-30 * time.Minute),
			State:     queueintent.StateRunning, Priority: 2,
			StateEnteredAt: observationTime.Add(-30 * time.Minute),
			UpdatedAt:      observationTime.Add(-30 * time.Minute),
			ExpiresAt:      observationTime.Add(time.Minute),
		}}}, nil
	}
	return collector
}

// An authorized online drain of every member leaves no eligible placement.
// Queue and assigned pages must still fire (HeldOutMembers stays empty because
// the listing is complete). Platform health must not drop, or a bake drain
// pages fleet_platform_unhealthy about capacity the operator just removed.
func TestOnlineDrainOfEveryMemberDoesNotFailPlatformHealth(t *testing.T) {
	collector := strandedUncoveredCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{
			{Name: "gha-runner-1", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-2", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-3", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-4", Online: true, DrainReason: "drained: image bake"},
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Queue.UncoveredRunningBeyondGrace == 0 {
		t.Fatal("the uncovered running intent vanished instead of staying observable")
	}
	if len(snapshot.HeldOutMembers) != 0 {
		t.Fatalf("online drain was counted as a listing hold-out: %v", snapshot.HeldOutMembers)
	}
	if !snapshot.Healthy {
		t.Fatalf("a full online drain failed the platform: errors=%v uncovered=%d",
			snapshot.CollectionErrors, snapshot.Queue.UncoveredRunningBeyondGrace)
	}
}

// One undrained sibling can still place work, so the same stranded intent is
// a platform failure.
func TestPartialOnlineDrainStillFailsPlatformHealthOnUncoveredRunning(t *testing.T) {
	collector := strandedUncoveredCollector(t)
	collector.Members = func(context.Context) ([]MemberVisibility, error) {
		return []MemberVisibility{
			{Name: "gha-runner-1", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-2", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-3", Online: true, DrainReason: "drained: image bake"},
			{Name: "gha-runner-4", Online: true},
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Queue.UncoveredRunningBeyondGrace == 0 {
		t.Fatalf("partial drain hid an uncovered running gap: healthy=%v uncovered=%d",
			snapshot.Healthy, snapshot.Queue.UncoveredRunningBeyondGrace)
	}
}
