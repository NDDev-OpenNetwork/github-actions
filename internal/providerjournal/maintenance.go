package providerjournal

import (
	"context"
	"sort"
	"strings"
	"time"
)

const maintenanceExpiryGrace = time.Minute

type MaintenanceReconcileResult struct {
	Applied    bool      `json:"applied"`
	ObservedAt time.Time `json:"observed_at"`
	Candidates []string  `json:"candidates"`
	Removed    []string  `json:"removed"`
	Retained   []string  `json:"retained"`
}

func IsImageMaintenanceInstance(name string) bool {
	for _, prefix := range []string{"gha-image-builder-", "gha-image-smoke-"} {
		suffix, found := strings.CutPrefix(name, prefix)
		if !found || len(suffix) != 12 {
			continue
		}
		if strings.IndexFunc(suffix, func(character rune) bool {
			return (character < '0' || character > '9') && (character < 'a' || character > 'f')
		}) == -1 {
			return true
		}
	}
	return false
}

func ReconcileExpiredMaintenance(
	ctx context.Context,
	store Store,
	visible map[string]struct{},
	now time.Time,
	apply bool,
) (MaintenanceReconcileResult, error) {
	now = now.UTC()
	result := MaintenanceReconcileResult{Applied: apply, ObservedAt: now, Candidates: []string{}, Removed: []string{}, Retained: []string{}}
	journal, err := store.Read(ctx)
	if err != nil {
		return result, err
	}
	candidates := maintenanceCandidates(journal, visible, now)
	result.Candidates = candidates
	if !apply {
		result.Retained = retainedMaintenance(journal, candidates)
		return result, nil
	}
	updated, err := store.Update(ctx, func(current *Journal) error {
		for _, name := range maintenanceCandidates(*current, visible, now) {
			delete(current.Leases, name)
			for jobName, claim := range current.Claims {
				if claim.InstanceName == name {
					delete(current.Claims, jobName)
				}
			}
			result.Removed = append(result.Removed, name)
		}
		return nil
	})
	if err != nil {
		return MaintenanceReconcileResult{}, err
	}
	result.Retained = retainedMaintenance(updated, result.Removed)
	return result, nil
}

func maintenanceCandidates(journal Journal, visible map[string]struct{}, now time.Time) []string {
	claimed := make(map[string]struct{}, len(journal.Claims))
	for _, claim := range journal.Claims {
		claimed[claim.InstanceName] = struct{}{}
	}
	result := make([]string, 0)
	for name, lease := range journal.Leases {
		_, isVisible := visible[name]
		_, isClaimed := claimed[name]
		if !IsImageMaintenanceInstance(name) || !strings.HasPrefix(lease.PoolID, "image-maintenance/") ||
			isVisible || isClaimed || lease.PreemptedBy != "" ||
			now.Before(lease.ExpiresAt.Add(maintenanceExpiryGrace)) {
			continue
		}
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func retainedMaintenance(journal Journal, removed []string) []string {
	removedSet := make(map[string]struct{}, len(removed))
	for _, name := range removed {
		removedSet[name] = struct{}{}
	}
	result := make([]string, 0)
	for name, lease := range journal.Leases {
		if IsImageMaintenanceInstance(name) && strings.HasPrefix(lease.PoolID, "image-maintenance/") {
			if _, wasRemoved := removedSet[name]; !wasRemoved {
				result = append(result, name)
			}
		}
	}
	sort.Strings(result)
	return result
}
