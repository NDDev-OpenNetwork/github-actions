package providerjournal

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestMaintenanceReconcileRemovesOnlyExpiredAbsentExactLease(t *testing.T) {
	now := time.Date(2026, time.August, 22, 8, 0, 0, 0, time.UTC)
	store := testStore(t)
	store.Now = func() time.Time { return now }
	expired := maintenanceLease("gha-image-builder-012345abcdef", now.Add(-10*time.Minute), now.Add(-2*time.Minute))
	visible := maintenanceLease("gha-image-smoke-fedcba543210", now.Add(-10*time.Minute), now.Add(-2*time.Minute))
	fresh := maintenanceLease("gha-image-builder-111111111111", now.Add(-5*time.Minute), now.Add(-30*time.Second))
	claimed := maintenanceLease("gha-image-builder-222222222222", now.Add(-10*time.Minute), now.Add(-2*time.Minute))
	malformed := maintenanceLease("gha-image-builder-not-a-digest", now.Add(-10*time.Minute), now.Add(-2*time.Minute))
	ordinary := validLease("runner-ordinary", now.Add(-10*time.Minute))
	ordinary.State = StateCreated
	ordinary.UpdatedAt = now.Add(-10 * time.Minute)
	ordinary.ExpiresAt = now.Add(-2 * time.Minute)
	_, err := store.Update(context.Background(), func(journal *Journal) error {
		for _, lease := range []Lease{expired, visible, fresh, claimed, malformed, ordinary} {
			journal.Leases[lease.InstanceName] = lease
		}
		journal.Claims["job-claimed"] = Claim{
			JobName: "job-claimed", InstanceName: claimed.InstanceName, ControllerID: claimed.ControllerID,
			PoolID: claimed.PoolID, PoolName: claimed.PoolName, ImageFingerprint: claimed.ImageFingerprint,
			State: ClaimReserved, ReservedAt: now.Add(-10 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute),
			ExpiresAt: now.Add(5 * time.Minute),
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	visibleInventory := map[string]struct{}{visible.InstanceName: {}}
	plan, err := ReconcileExpiredMaintenance(context.Background(), store, visibleInventory, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Applied || !slices.Equal(plan.Candidates, []string{expired.InstanceName}) || len(plan.Removed) != 0 {
		t.Fatalf("unexpected maintenance plan: %#v", plan)
	}
	before, err := store.Read(context.Background())
	if err != nil || len(before.Leases) != 6 {
		t.Fatalf("plan mutated journal: leases=%d err=%v", len(before.Leases), err)
	}
	result, err := ReconcileExpiredMaintenance(context.Background(), store, visibleInventory, now, true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !slices.Equal(result.Candidates, []string{expired.InstanceName}) ||
		!slices.Equal(result.Removed, []string{expired.InstanceName}) {
		t.Fatalf("unexpected maintenance apply: %#v", result)
	}
	after, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := after.Leases[expired.InstanceName]; exists || len(after.Leases) != 5 || len(after.Claims) != 1 {
		t.Fatalf("maintenance apply crossed its boundary: %#v", after)
	}
}

func TestImageMaintenanceIdentityIsExact(t *testing.T) {
	for _, name := range []string{"gha-image-builder-012345abcdef", "gha-image-smoke-fedcba543210"} {
		if !IsImageMaintenanceInstance(name) {
			t.Fatalf("exact maintenance identity %q was rejected", name)
		}
	}
	for _, name := range []string{"gha-image-builder-not-a-digest", "gha-image-builder-012345ABCDEf", "gha-image-smoke-0123", "runner-012345abcdef"} {
		if IsImageMaintenanceInstance(name) {
			t.Fatalf("malformed maintenance identity %q was accepted", name)
		}
	}
}

func maintenanceLease(name string, updatedAt, expiresAt time.Time) Lease {
	return Lease{
		InstanceName: name, ControllerID: "controller-test", PoolID: "image-maintenance/nddev-linux-standard",
		PoolName: "nddev-linux-standard", VCPU: 2, MemoryMiB: 4096,
		ImageFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		State:            StateCreated, AdmittedAt: updatedAt, UpdatedAt: updatedAt, ExpiresAt: expiresAt,
	}
}
