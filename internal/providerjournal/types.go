package providerjournal

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const SchemaVersion = 4

const LegacySchemaVersion = 1
const ClaimsSchemaVersion = 2
const PreemptionSchemaVersion = 3

type LeaseState string

const (
	StateAdmitted    LeaseState = "admitted"
	StateCreated     LeaseState = "created"
	StateDeleting    LeaseState = "deleting"
	StateWarmReady   LeaseState = "warm-ready"
	StateWarmClaimed LeaseState = "warm-claimed"
)

type ClaimState string

const (
	ClaimReserved ClaimState = "reserved"
	ClaimInjected ClaimState = "injected"
)

// Claim binds one GARM runner identity to one pre-booted, unregistered VM.
// The binding is persisted before any job credential crosses the Incus API.
// InstanceName is the provider ID returned to GARM; JobName is GARM's stable
// retry identity and therefore the map key.
type Claim struct {
	JobName          string     `json:"job_name"`
	InstanceName     string     `json:"instance_name"`
	ControllerID     string     `json:"controller_id"`
	PoolID           string     `json:"pool_id"`
	PoolName         string     `json:"pool_name"`
	ImageFingerprint string     `json:"image_fingerprint"`
	State            ClaimState `json:"state"`
	ReservedAt       time.Time  `json:"reserved_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
}

type Lease struct {
	InstanceName      string     `json:"instance_name"`
	ControllerID      string     `json:"controller_id"`
	PoolID            string     `json:"pool_id"`
	PoolName          string     `json:"pool_name"`
	VCPU              int        `json:"vcpu"`
	CPUAllowanceUnits int        `json:"cpu_allowance_units,omitempty"`
	MemoryMiB         int        `json:"memory_mib"`
	ImageFingerprint  string     `json:"image_fingerprint"`
	State             LeaseState `json:"state"`
	// PreemptedBy durably binds speculative warm capacity to the admitted cold
	// request that is replacing it. It is set before VM teardown, so a warm
	// claim or replenishment process cannot race into the capacity hand-off.
	// State remains the observed lifecycle state until teardown actually starts;
	// MarkDeleting then moves it to StateDeleting.
	PreemptedBy string    `json:"preempted_by,omitempty"`
	AdmittedAt  time.Time `json:"admitted_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type Journal struct {
	SchemaVersion        int              `json:"schema_version"`
	Generation           uint64           `json:"generation"`
	UpdatedAt            time.Time        `json:"updated_at"`
	WarmPreemptionsTotal uint64           `json:"warm_preemptions_total"`
	Leases               map[string]Lease `json:"leases"`
	Claims               map[string]Claim `json:"claims"`
}

func newJournal() Journal {
	return Journal{
		SchemaVersion: SchemaVersion,
		Leases:        make(map[string]Lease),
		Claims:        make(map[string]Claim),
	}
}

func (j Journal) Validate() error {
	if j.SchemaVersion != SchemaVersion {
		return fmt.Errorf("journal schema_version must be %d", SchemaVersion)
	}
	if j.Leases == nil {
		return fmt.Errorf("journal leases must not be null")
	}
	if j.Claims == nil {
		return fmt.Errorf("journal claims must not be null")
	}
	keys := make([]string, 0, len(j.Leases))
	for key := range j.Leases {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		lease := j.Leases[key]
		if key == "" || lease.InstanceName != key {
			return fmt.Errorf("lease key %q does not match instance_name %q", key, lease.InstanceName)
		}
		if lease.ControllerID == "" || lease.PoolID == "" || lease.PoolName == "" {
			return fmt.Errorf("lease %q has incomplete ownership", key)
		}
		if lease.VCPU <= 0 || lease.MemoryMiB <= 0 {
			return fmt.Errorf("lease %q has invalid resources", key)
		}
		if lease.CPUAllowanceUnits > 0 && lease.CPUAllowanceUnits < lease.VCPU {
			return fmt.Errorf("lease %q CPU allowance is below its reservation", key)
		}
		if lease.ImageFingerprint == "" {
			return fmt.Errorf("lease %q has no image fingerprint", key)
		}
		switch lease.State {
		case StateAdmitted, StateCreated, StateDeleting, StateWarmReady, StateWarmClaimed:
		default:
			return fmt.Errorf("lease %q has invalid state %q", key, lease.State)
		}
		if lease.AdmittedAt.IsZero() || lease.UpdatedAt.IsZero() || lease.ExpiresAt.IsZero() {
			return fmt.Errorf("lease %q has incomplete timestamps", key)
		}
		if lease.ExpiresAt.Before(lease.UpdatedAt) {
			return fmt.Errorf("lease %q expires before its last update", key)
		}
		if lease.PreemptedBy != "" {
			preemptibleState := lease.State == StateCreated || lease.State == StateWarmReady || lease.State == StateDeleting
			if !preemptibleState || !strings.HasPrefix(lease.PoolID, "warm/") || lease.PreemptedBy == key {
				return fmt.Errorf("lease %q has invalid warm preemption ownership", key)
			}
		}
	}
	claimKeys := make([]string, 0, len(j.Claims))
	for key := range j.Claims {
		claimKeys = append(claimKeys, key)
	}
	sort.Strings(claimKeys)
	claimedInstances := make(map[string]string, len(j.Claims))
	for _, key := range claimKeys {
		claim := j.Claims[key]
		if key == "" || claim.JobName != key {
			return fmt.Errorf("claim key %q does not match job_name %q", key, claim.JobName)
		}
		if claim.InstanceName == "" || claim.ControllerID == "" || claim.PoolID == "" || claim.PoolName == "" || claim.ImageFingerprint == "" {
			return fmt.Errorf("claim %q has incomplete ownership", key)
		}
		if previous, exists := claimedInstances[claim.InstanceName]; exists {
			return fmt.Errorf("instance %q is claimed by both %q and %q", claim.InstanceName, previous, key)
		}
		claimedInstances[claim.InstanceName] = key
		lease, exists := j.Leases[claim.InstanceName]
		if !exists {
			return fmt.Errorf("claim %q references missing lease %q", key, claim.InstanceName)
		}
		if lease.ControllerID != claim.ControllerID || lease.PoolName != claim.PoolName || lease.ImageFingerprint != claim.ImageFingerprint {
			return fmt.Errorf("claim %q conflicts with lease %q", key, claim.InstanceName)
		}
		if lease.PreemptedBy != "" {
			return fmt.Errorf("claim %q references preempted lease %q", key, claim.InstanceName)
		}
		if lease.State != StateWarmClaimed && lease.State != StateCreated && lease.State != StateDeleting {
			return fmt.Errorf("claim %q references lease %q in state %q", key, claim.InstanceName, lease.State)
		}
		switch claim.State {
		case ClaimReserved, ClaimInjected:
		default:
			return fmt.Errorf("claim %q has invalid state %q", key, claim.State)
		}
		if claim.ReservedAt.IsZero() || claim.UpdatedAt.IsZero() || claim.ExpiresAt.IsZero() {
			return fmt.Errorf("claim %q has incomplete timestamps", key)
		}
		if claim.ExpiresAt.Before(claim.UpdatedAt) {
			return fmt.Errorf("claim %q expires before its last update", key)
		}
	}
	for key, lease := range j.Leases {
		if lease.PreemptedBy == "" {
			continue
		}
		target, exists := j.Leases[lease.PreemptedBy]
		if !exists || strings.HasPrefix(target.PoolID, "warm/") || target.PreemptedBy != "" {
			return fmt.Errorf("lease %q references invalid preemption target %q", key, lease.PreemptedBy)
		}
	}
	return nil
}
