package fleetobserve

import (
	"context"
	"strings"
	"testing"
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
