package provideradmission

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
)

type Allocation struct {
	InstanceName     string
	ControllerID     string
	PoolID           string
	PoolName         string
	VCPU             int
	MemoryMiB        int
	ImageFingerprint string
	// State is derived from immutable Incus lifecycle metadata. An empty value
	// is treated as StateCreated for compatibility with cold workers.
	State   providerjournal.LeaseState
	JobName string
}

type Request struct {
	Allocation
	MaxRunning            int
	QueueIntentAuthorized bool
}

type AdmissionResult struct {
	Decision             admission.Decision `json:"decision"`
	PreemptedWarmWorkers []string           `json:"preempted_warm_workers,omitempty"`
}

type WarmClaimRequest struct {
	JobName          string
	ControllerID     string
	PoolID           string
	PoolName         string
	ImageFingerprint string
}

type WarmClaimResult struct {
	InstanceName string
	State        providerjournal.ClaimState
	Found        bool
}

type Controller struct {
	Store        providerjournal.Store
	ControllerID string
	Policy       admission.ReservePolicy
	LeaseTTL     time.Duration
	Now          func() time.Time
}

// Reconcile records the exact observed Incus inventory without making a new
// capacity decision. It is used when a provider retry adopts a VM that already
// exists after a process or manager restart.
func (c Controller) Reconcile(ctx context.Context, observed []Allocation) error {
	if err := c.validate(); err != nil {
		return err
	}
	observedByName, err := c.validateObserved(observed)
	if err != nil {
		return err
	}
	_, err = c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		return c.reconcile(journal, observedByName, c.now())
	})
	return err
}

func (c Controller) Admit(
	ctx context.Context,
	host admission.HostSnapshot,
	observed []Allocation,
	request Request,
) (admission.Decision, error) {
	result, err := c.admit(ctx, host, observed, request, false)
	return result.Decision, err
}

// AdmitPreemptible atomically reserves a cold request and marks a deterministic
// capacity-satisfying set of unclaimed warm-ready leases as its victims.
// The caller must delete every returned VM before launching the reserved job
// worker, then call this method again with fresh host/inventory state to prove
// that the hand-off preserved all reserves.
func (c Controller) AdmitPreemptible(
	ctx context.Context,
	host admission.HostSnapshot,
	observed []Allocation,
	request Request,
) (AdmissionResult, error) {
	return c.admit(ctx, host, observed, request, true)
}

func (c Controller) admit(
	ctx context.Context,
	host admission.HostSnapshot,
	observed []Allocation,
	request Request,
	allowWarmPreemption bool,
) (AdmissionResult, error) {
	if err := c.validate(); err != nil {
		return AdmissionResult{}, err
	}
	if err := validateAllocation(request.Allocation); err != nil {
		return AdmissionResult{}, fmt.Errorf("validate request: %w", err)
	}
	if request.MaxRunning <= 0 {
		return AdmissionResult{}, fmt.Errorf("validate request: max running must be positive")
	}
	if request.ControllerID != c.ControllerID {
		return AdmissionResult{}, fmt.Errorf("request controller ID does not match admission controller")
	}
	if allowWarmPreemption && strings.HasPrefix(request.PoolID, "warm/") {
		return AdmissionResult{}, fmt.Errorf("warm capacity cannot request warm preemption")
	}
	observedByName, err := c.validateObserved(observed)
	if err != nil {
		return AdmissionResult{}, err
	}

	var result AdmissionResult
	_, err = c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		now := c.now()
		if err := c.reconcile(journal, observedByName, now); err != nil {
			return err
		}

		if existing, exists := journal.Leases[request.InstanceName]; exists {
			if err := leaseMatches(existing, request.Allocation); err != nil {
				return err
			}
			preemptions := preemptionsFor(journal.Leases, request.InstanceName)
			result.PreemptedWarmWorkers = preemptions
			excluded := append([]string{request.InstanceName}, preemptions...)
			hostWithoutExisting := withAllocatedLeasesExcluding(host, journal.Leases, request.PoolName, exclusionSet(excluded...))
			hostWithoutExisting.AvailableMemoryMiB = projectedAvailableMemory(host, journal.Leases, preemptions)
			result.Decision, err = admission.Evaluate(hostWithoutExisting, c.Policy, admission.Request{
				PoolName: request.PoolName, VCPU: request.VCPU, MemoryMiB: request.MemoryMiB, MaxRunning: request.MaxRunning,
			})
			if err != nil {
				return err
			}
			if !result.Decision.Admitted && len(preemptions) == 0 {
				return nil
			}
			existing.UpdatedAt = now
			existing.ExpiresAt = now.Add(c.LeaseTTL)
			journal.Leases[request.InstanceName] = existing
			for _, name := range preemptions {
				victim := journal.Leases[name]
				victim.UpdatedAt = now
				victim.ExpiresAt = now.Add(c.LeaseTTL)
				journal.Leases[name] = victim
			}
			return nil
		}

		allocatedHost := withAllocatedLeases(host, journal.Leases, request.PoolName, "")
		result.Decision, err = admission.Evaluate(allocatedHost, c.Policy, admission.Request{
			PoolName:   request.PoolName,
			VCPU:       request.VCPU,
			MemoryMiB:  request.MemoryMiB,
			MaxRunning: request.MaxRunning,
		})
		if err != nil {
			return err
		}
		if !result.Decision.Admitted && allowWarmPreemption && preemptibleReason(result.Decision.Reason) {
			result.Decision, result.PreemptedWarmWorkers, err = c.planWarmPreemption(host, journal, request)
			if err != nil {
				return err
			}
		}
		if !result.Decision.Admitted {
			return nil
		}
		journal.Leases[request.InstanceName] = providerjournal.Lease{
			InstanceName:     request.InstanceName,
			ControllerID:     request.ControllerID,
			PoolID:           request.PoolID,
			PoolName:         request.PoolName,
			VCPU:             request.VCPU,
			MemoryMiB:        request.MemoryMiB,
			ImageFingerprint: request.ImageFingerprint,
			State:            providerjournal.StateAdmitted,
			AdmittedAt:       now,
			UpdatedAt:        now,
			ExpiresAt:        now.Add(c.LeaseTTL),
		}
		for _, name := range result.PreemptedWarmWorkers {
			victim := journal.Leases[name]
			victim.PreemptedBy = request.InstanceName
			victim.UpdatedAt = now
			victim.ExpiresAt = now.Add(c.LeaseTTL)
			journal.Leases[name] = victim
		}
		increment := uint64(len(result.PreemptedWarmWorkers))
		if ^uint64(0)-journal.WarmPreemptionsTotal < increment {
			return fmt.Errorf("warm preemption counter overflow")
		}
		journal.WarmPreemptionsTotal += increment
		return nil
	})
	if err != nil {
		return AdmissionResult{}, err
	}
	return result, nil
}

func (c Controller) planWarmPreemption(
	host admission.HostSnapshot,
	journal *providerjournal.Journal,
	request Request,
) (admission.Decision, []string, error) {
	projected := withAllocatedLeases(host, journal.Leases, request.PoolName, "")
	decision, err := admission.Evaluate(projected, c.Policy, admission.Request{
		PoolName: request.PoolName, VCPU: request.VCPU, MemoryMiB: request.MemoryMiB, MaxRunning: request.MaxRunning,
	})
	if err != nil {
		return admission.Decision{}, nil, err
	}
	candidates := make([]providerjournal.Lease, 0)
	claimedInstances := make(map[string]struct{}, len(journal.Claims))
	for _, claim := range journal.Claims {
		claimedInstances[claim.InstanceName] = struct{}{}
	}
	for _, lease := range journal.Leases {
		if _, claimed := claimedInstances[lease.InstanceName]; claimed {
			continue
		}
		preemptibleState := lease.State == providerjournal.StateWarmReady ||
			(request.QueueIntentAuthorized && lease.State == providerjournal.StateCreated)
		if !preemptibleState || lease.PreemptedBy != "" ||
			!strings.HasPrefix(lease.PoolID, "warm/") || lease.PoolName == request.PoolName {
			continue
		}
		candidates = append(candidates, lease)
	}
	sort.Slice(candidates, func(left, right int) bool {
		if candidates[left].VCPU != candidates[right].VCPU {
			return candidates[left].VCPU > candidates[right].VCPU
		}
		if candidates[left].MemoryMiB != candidates[right].MemoryMiB {
			return candidates[left].MemoryMiB > candidates[right].MemoryMiB
		}
		if !candidates[left].AdmittedAt.Equal(candidates[right].AdmittedAt) {
			return candidates[left].AdmittedAt.Before(candidates[right].AdmittedAt)
		}
		return candidates[left].InstanceName < candidates[right].InstanceName
	})

	selected := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		selected = append(selected, candidate.InstanceName)
		projected = withAllocatedLeasesExcluding(host, journal.Leases, request.PoolName, exclusionSet(selected...))
		projected.AvailableMemoryMiB = projectedAvailableMemory(host, journal.Leases, selected)
		decision, err = admission.Evaluate(projected, c.Policy, admission.Request{
			PoolName: request.PoolName, VCPU: request.VCPU, MemoryMiB: request.MemoryMiB, MaxRunning: request.MaxRunning,
		})
		if err != nil {
			return admission.Decision{}, nil, err
		}
		if decision.Admitted {
			return decision, selected, nil
		}
	}
	return decision, nil, nil
}

func preemptibleReason(reason admission.Reason) bool {
	return reason == admission.ReasonInsufficientCPU || reason == admission.ReasonInsufficientMemory
}

// ClaimWarm atomically binds one GARM job identity to one already observed,
// unregistered warm VM. It performs no Incus mutation and is safe to repeat
// after a provider crash: the same job always receives the same provider ID.
func (c Controller) ClaimWarm(
	ctx context.Context,
	observed []Allocation,
	request WarmClaimRequest,
) (WarmClaimResult, error) {
	if err := c.validate(); err != nil {
		return WarmClaimResult{}, err
	}
	if request.JobName == "" || request.ControllerID == "" || request.PoolID == "" || request.PoolName == "" || request.ImageFingerprint == "" {
		return WarmClaimResult{}, fmt.Errorf("warm claim identity is incomplete")
	}
	if request.ControllerID != c.ControllerID {
		return WarmClaimResult{}, fmt.Errorf("warm claim controller ID does not match admission controller")
	}
	observedByName, err := c.validateObserved(observed)
	if err != nil {
		return WarmClaimResult{}, err
	}

	var result WarmClaimResult
	_, err = c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		now := c.now()
		if err := c.reconcile(journal, observedByName, now); err != nil {
			return err
		}
		if existing, exists := journal.Claims[request.JobName]; exists {
			if err := claimMatches(existing, request); err != nil {
				return err
			}
			existing.UpdatedAt = now
			existing.ExpiresAt = now.Add(c.LeaseTTL)
			journal.Claims[request.JobName] = existing
			lease := journal.Leases[existing.InstanceName]
			lease.UpdatedAt = now
			lease.ExpiresAt = now.Add(c.LeaseTTL)
			journal.Leases[existing.InstanceName] = lease
			result = WarmClaimResult{InstanceName: existing.InstanceName, State: existing.State, Found: true}
			return nil
		}

		candidates := make([]string, 0)
		for name, lease := range journal.Leases {
			if lease.State == providerjournal.StateWarmReady &&
				lease.PreemptedBy == "" &&
				lease.ControllerID == request.ControllerID &&
				lease.PoolName == request.PoolName &&
				lease.ImageFingerprint == request.ImageFingerprint {
				candidates = append(candidates, name)
			}
		}
		if len(candidates) == 0 {
			return nil
		}
		sort.Strings(candidates)
		instanceName := candidates[0]
		lease := journal.Leases[instanceName]
		lease.State = providerjournal.StateWarmClaimed
		lease.UpdatedAt = now
		lease.ExpiresAt = now.Add(c.LeaseTTL)
		journal.Leases[instanceName] = lease
		claim := providerjournal.Claim{
			JobName:          request.JobName,
			InstanceName:     instanceName,
			ControllerID:     request.ControllerID,
			PoolID:           request.PoolID,
			PoolName:         request.PoolName,
			ImageFingerprint: request.ImageFingerprint,
			State:            providerjournal.ClaimReserved,
			ReservedAt:       now,
			UpdatedAt:        now,
			ExpiresAt:        now.Add(c.LeaseTTL),
		}
		journal.Claims[request.JobName] = claim
		result = WarmClaimResult{InstanceName: instanceName, State: claim.State, Found: true}
		return nil
	})
	return result, err
}

func (c Controller) MarkWarmInjected(ctx context.Context, jobName, instanceName string) error {
	if err := c.validate(); err != nil {
		return err
	}
	_, err := c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		claim, exists := journal.Claims[jobName]
		if !exists || claim.InstanceName != instanceName || claim.ControllerID != c.ControllerID {
			return fmt.Errorf("warm claim %q does not own instance %q", jobName, instanceName)
		}
		now := c.now()
		claim.State = providerjournal.ClaimInjected
		claim.UpdatedAt = now
		claim.ExpiresAt = now.Add(c.LeaseTTL)
		journal.Claims[jobName] = claim
		lease := journal.Leases[instanceName]
		lease.UpdatedAt = now
		lease.ExpiresAt = now.Add(c.LeaseTTL)
		journal.Leases[instanceName] = lease
		return nil
	})
	return err
}

func (c Controller) Resolve(ctx context.Context, identifier string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	journal, err := c.Store.Read(ctx)
	if err != nil {
		return "", err
	}
	if _, exists := journal.Leases[identifier]; exists {
		return identifier, nil
	}
	if claim, exists := journal.Claims[identifier]; exists && claim.ControllerID == c.ControllerID {
		return claim.InstanceName, nil
	}
	return identifier, nil
}

// AuthorizeWarmDrain verifies under the journal lock that the exact instance is
// an unclaimed ready lease. The manager must remain stopped after this check so
// no new claim can be admitted before the provider teardown begins.
func (c Controller) AuthorizeWarmDrain(ctx context.Context, instanceName string) error {
	if err := c.validate(); err != nil {
		return err
	}
	if instanceName == "" {
		return fmt.Errorf("warm drain instance name is required")
	}
	_, err := c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		if len(journal.Claims) != 0 {
			return fmt.Errorf("warm drain requires zero claims, found %d", len(journal.Claims))
		}
		lease, exists := journal.Leases[instanceName]
		if !exists {
			return fmt.Errorf("warm drain lease %q does not exist", instanceName)
		}
		if lease.ControllerID != c.ControllerID {
			return fmt.Errorf("warm drain lease %q belongs to controller %q", instanceName, lease.ControllerID)
		}
		if lease.State != providerjournal.StateWarmReady {
			return fmt.Errorf("warm drain lease %q has state %q", instanceName, lease.State)
		}
		if lease.PreemptedBy != "" {
			return fmt.Errorf("warm drain lease %q is reserved by preemption target %q", instanceName, lease.PreemptedBy)
		}
		return nil
	})
	return err
}

// MarkCreated records that Incus accepted the create transaction. It refuses
// to manufacture a lease because admission must precede every mutation.
func (c Controller) MarkCreated(ctx context.Context, instanceName string) error {
	return c.transition(ctx, instanceName, providerjournal.StateCreated, true)
}

func (c Controller) MarkDeleting(ctx context.Context, instanceName string) error {
	return c.transition(ctx, instanceName, providerjournal.StateDeleting, false)
}

func (c Controller) Release(ctx context.Context, instanceName string) error {
	if err := c.validate(); err != nil {
		return err
	}
	_, err := c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		if claim, exists := journal.Claims[instanceName]; exists {
			instanceName = claim.InstanceName
		}
		lease, exists := journal.Leases[instanceName]
		if !exists {
			return nil
		}
		if lease.ControllerID != c.ControllerID {
			return fmt.Errorf("lease %q belongs to controller %q", instanceName, lease.ControllerID)
		}
		for name, candidate := range journal.Leases {
			if candidate.PreemptedBy != instanceName {
				continue
			}
			if candidate.State == providerjournal.StateDeleting {
				return fmt.Errorf("cannot release preemption target %q while victim %q teardown is active", instanceName, name)
			}
			candidate.PreemptedBy = ""
			candidate.UpdatedAt = c.now()
			candidate.ExpiresAt = candidate.UpdatedAt.Add(c.LeaseTTL)
			journal.Leases[name] = candidate
		}
		delete(journal.Leases, instanceName)
		for jobName, claim := range journal.Claims {
			if claim.InstanceName == instanceName {
				delete(journal.Claims, jobName)
			}
		}
		return nil
	})
	return err
}

func (c Controller) transition(ctx context.Context, instanceName string, state providerjournal.LeaseState, requireExisting bool) error {
	if err := c.validate(); err != nil {
		return err
	}
	if instanceName == "" {
		return fmt.Errorf("instance name is required")
	}
	_, err := c.Store.Update(ctx, func(journal *providerjournal.Journal) error {
		lease, exists := journal.Leases[instanceName]
		if !exists {
			if requireExisting {
				return fmt.Errorf("lease %q does not exist", instanceName)
			}
			return nil
		}
		if lease.ControllerID != c.ControllerID {
			return fmt.Errorf("lease %q belongs to controller %q", instanceName, lease.ControllerID)
		}
		now := c.now()
		lease.State = state
		lease.UpdatedAt = now
		lease.ExpiresAt = now.Add(c.LeaseTTL)
		journal.Leases[instanceName] = lease
		return nil
	})
	return err
}

func (c Controller) reconcile(journal *providerjournal.Journal, observed map[string]Allocation, now time.Time) error {
	observedStates := make(map[string]providerjournal.LeaseState, len(observed))
	for name, allocation := range observed {
		observedStates[name] = normalizedObservedState(allocation.State)
	}
	claimsByInstance := make(map[string]providerjournal.Claim, len(journal.Claims))
	for _, claim := range journal.Claims {
		claimsByInstance[claim.InstanceName] = claim
	}
	leaseNames := make([]string, 0, len(journal.Leases))
	for name := range journal.Leases {
		leaseNames = append(leaseNames, name)
	}
	sort.Strings(leaseNames)
	for _, name := range leaseNames {
		lease := journal.Leases[name]
		if lease.ControllerID != c.ControllerID {
			return fmt.Errorf("journal lease %q belongs to controller %q", name, lease.ControllerID)
		}
		allocation, exists := observed[name]
		if exists {
			if err := leaseMatches(lease, allocation); err != nil {
				claim, claimed := claimsByInstance[name]
				if !claimed || !allocationMatchesClaim(allocation, claim) ||
					lease.InstanceName != allocation.InstanceName || lease.ControllerID != allocation.ControllerID ||
					lease.PoolName != allocation.PoolName || lease.VCPU != allocation.VCPU ||
					lease.MemoryMiB != allocation.MemoryMiB || lease.ImageFingerprint != allocation.ImageFingerprint {
					return fmt.Errorf("observed instance conflicts with journal: %w", err)
				}
				lease.PoolID = allocation.PoolID
			}
			observedState := normalizedObservedState(allocation.State)
			if lease.State == providerjournal.StateWarmClaimed && observedState == providerjournal.StateWarmReady {
				observedState = providerjournal.StateWarmClaimed
			}
			if lease.PreemptedBy != "" {
				if _, targetExists := journal.Leases[lease.PreemptedBy]; targetExists && now.Before(lease.ExpiresAt) {
					if lease.State == providerjournal.StateDeleting {
						observedState = providerjournal.StateDeleting
					}
				} else {
					lease.PreemptedBy = ""
				}
			}
			lease.State = observedState
			lease.UpdatedAt = now
			lease.ExpiresAt = now.Add(c.LeaseTTL)
			journal.Leases[name] = lease
			if claim, claimed := claimsByInstance[name]; claimed {
				claim.UpdatedAt = now
				claim.ExpiresAt = now.Add(c.LeaseTTL)
				journal.Claims[claim.JobName] = claim
			}
			delete(observed, name)
			continue
		}
		if lease.State != providerjournal.StateAdmitted || !now.Before(lease.ExpiresAt) {
			delete(journal.Leases, name)
			if claim, claimed := claimsByInstance[name]; claimed {
				delete(journal.Claims, claim.JobName)
			}
		}
	}
	for name, lease := range journal.Leases {
		if lease.PreemptedBy == "" {
			continue
		}
		if _, exists := journal.Leases[lease.PreemptedBy]; exists {
			continue
		}
		observedState, exists := observedStates[name]
		if !exists {
			delete(journal.Leases, name)
			continue
		}
		lease.State = observedState
		lease.PreemptedBy = ""
		lease.UpdatedAt = now
		lease.ExpiresAt = now.Add(c.LeaseTTL)
		journal.Leases[name] = lease
	}

	observedNames := make([]string, 0, len(observed))
	for name := range observed {
		observedNames = append(observedNames, name)
	}
	sort.Strings(observedNames)
	for _, name := range observedNames {
		allocation := observed[name]
		journal.Leases[name] = providerjournal.Lease{
			InstanceName:     allocation.InstanceName,
			ControllerID:     allocation.ControllerID,
			PoolID:           allocation.PoolID,
			PoolName:         allocation.PoolName,
			VCPU:             allocation.VCPU,
			MemoryMiB:        allocation.MemoryMiB,
			ImageFingerprint: allocation.ImageFingerprint,
			State:            normalizedObservedState(allocation.State),
			AdmittedAt:       now,
			UpdatedAt:        now,
			ExpiresAt:        now.Add(c.LeaseTTL),
		}
	}
	return nil
}

func normalizedObservedState(state providerjournal.LeaseState) providerjournal.LeaseState {
	if state == "" {
		return providerjournal.StateCreated
	}
	return state
}

func allocationMatchesClaim(allocation Allocation, claim providerjournal.Claim) bool {
	return allocation.JobName == claim.JobName && allocation.InstanceName == claim.InstanceName &&
		allocation.ControllerID == claim.ControllerID && allocation.PoolID == claim.PoolID &&
		allocation.PoolName == claim.PoolName && allocation.ImageFingerprint == claim.ImageFingerprint
}

func claimMatches(claim providerjournal.Claim, request WarmClaimRequest) error {
	switch {
	case claim.JobName != request.JobName:
		return fmt.Errorf("job name mismatch for warm claim %q", claim.JobName)
	case claim.ControllerID != request.ControllerID:
		return fmt.Errorf("controller mismatch for warm claim %q", claim.JobName)
	case claim.PoolID != request.PoolID:
		return fmt.Errorf("pool ID mismatch for warm claim %q", claim.JobName)
	case claim.PoolName != request.PoolName:
		return fmt.Errorf("pool name mismatch for warm claim %q", claim.JobName)
	case claim.ImageFingerprint != request.ImageFingerprint:
		return fmt.Errorf("image fingerprint mismatch for warm claim %q", claim.JobName)
	}
	return nil
}

func (c Controller) validateObserved(observed []Allocation) (map[string]Allocation, error) {
	result := make(map[string]Allocation, len(observed))
	for _, allocation := range observed {
		if err := validateAllocation(allocation); err != nil {
			return nil, fmt.Errorf("validate observed instance: %w", err)
		}
		if allocation.ControllerID != c.ControllerID {
			return nil, fmt.Errorf("foreign instance %q belongs to controller %q", allocation.InstanceName, allocation.ControllerID)
		}
		if _, exists := result[allocation.InstanceName]; exists {
			return nil, fmt.Errorf("duplicate observed instance %q", allocation.InstanceName)
		}
		result[allocation.InstanceName] = allocation
	}
	return result, nil
}

func (c Controller) validate() error {
	if c.ControllerID == "" {
		return fmt.Errorf("controller ID is required")
	}
	if c.LeaseTTL < time.Minute || c.LeaseTTL > time.Hour {
		return fmt.Errorf("lease TTL must be between one minute and one hour")
	}
	return nil
}

func (c Controller) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func validateAllocation(allocation Allocation) error {
	if allocation.InstanceName == "" || allocation.ControllerID == "" || allocation.PoolID == "" || allocation.PoolName == "" {
		return fmt.Errorf("allocation identity is incomplete")
	}
	if allocation.VCPU <= 0 || allocation.MemoryMiB <= 0 || allocation.ImageFingerprint == "" {
		return fmt.Errorf("allocation resources or image fingerprint are invalid")
	}
	return nil
}

func leaseMatches(lease providerjournal.Lease, allocation Allocation) error {
	switch {
	case lease.InstanceName != allocation.InstanceName:
		return fmt.Errorf("instance name mismatch for lease %q", lease.InstanceName)
	case lease.ControllerID != allocation.ControllerID:
		return fmt.Errorf("controller mismatch for lease %q", lease.InstanceName)
	case lease.PoolID != allocation.PoolID:
		return fmt.Errorf("pool ID mismatch for lease %q", lease.InstanceName)
	case lease.PoolName != allocation.PoolName:
		return fmt.Errorf("pool name mismatch for lease %q", lease.InstanceName)
	case lease.VCPU != allocation.VCPU || lease.MemoryMiB != allocation.MemoryMiB:
		return fmt.Errorf("resource mismatch for lease %q", lease.InstanceName)
	case lease.ImageFingerprint != allocation.ImageFingerprint:
		return fmt.Errorf("image fingerprint mismatch for lease %q", lease.InstanceName)
	}
	return nil
}

func withAllocatedLeases(host admission.HostSnapshot, leases map[string]providerjournal.Lease, poolName, exclude string) admission.HostSnapshot {
	return withAllocatedLeasesExcluding(host, leases, poolName, exclusionSet(exclude))
}

func withAllocatedLeasesExcluding(
	host admission.HostSnapshot,
	leases map[string]providerjournal.Lease,
	poolName string,
	excluded map[string]struct{},
) admission.HostSnapshot {
	allocatedCPU := 0
	allocatedMemory := 0
	poolRunning := 0
	for name, lease := range leases {
		if _, skip := excluded[name]; skip {
			continue
		}
		allocatedCPU += lease.VCPU
		allocatedMemory += lease.MemoryMiB
		if lease.PoolName == poolName {
			poolRunning++
		}
	}
	host.AllocatedCPUUnits = allocatedCPU
	host.AllocatedMemoryMiB = allocatedMemory
	host.PoolRunning = poolRunning
	return host
}

func exclusionSet(names ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			result[name] = struct{}{}
		}
	}
	return result
}

func preemptionsFor(leases map[string]providerjournal.Lease, requestName string) []string {
	result := make([]string, 0)
	for name, lease := range leases {
		if lease.PreemptedBy == requestName {
			result = append(result, name)
		}
	}
	sort.Strings(result)
	return result
}

func projectedAvailableMemory(
	host admission.HostSnapshot,
	leases map[string]providerjournal.Lease,
	preemptions []string,
) int {
	available := host.AvailableMemoryMiB
	for _, name := range preemptions {
		available += leases[name].MemoryMiB
	}
	if available > host.TotalMemoryMiB {
		return host.TotalMemoryMiB
	}
	return available
}
