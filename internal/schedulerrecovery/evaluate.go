package schedulerrecovery

import "time"

type Policy struct {
	MinimumStuckAge time.Duration
	MinimumUptime   time.Duration
	Cooldown        time.Duration
	HeartbeatStale  time.Duration
}

type PendingCreate struct {
	ID            string        `json:"id"`
	Age           time.Duration `json:"age_nanoseconds"`
	CreateAttempt int           `json:"create_attempt"`
}

// ProviderRetry identifies a non-terminal provider-create retry that has not
// advanced or cleared after its own next_allowed_at deadline. Unlike the
// dispatcher heartbeat, this is scoped to one exact create path: sibling
// scale sets may continue making progress while this retry is parked.
type ProviderRetry struct {
	ID         string        `json:"id"`
	OverdueAge time.Duration `json:"overdue_age_nanoseconds"`
}

// AssignedIntent identifies central queue ownership that never produced a
// corresponding GARM instance. Sibling scale sets can keep both the process
// heartbeat and provider retry journal moving while this exact dispatch path
// is no longer scheduled.
type AssignedIntent struct {
	ID  string        `json:"id"`
	Age time.Duration `json:"age_nanoseconds"`
}

type Observation struct {
	ObservedAt      time.Time
	ActiveIntents   int
	PendingCreates  []PendingCreate
	OverdueRetries  []ProviderRetry
	StaleAssigned   []AssignedIntent
	ManagerUptime   time.Duration
	LastRecoveryAt  time.Time
	HeartbeatAt     time.Time
	RecoveryRunning bool
}

type Decision struct {
	Recover bool
	Reason  string
	Stuck   []string
}

func Evaluate(policy Policy, observation Observation) Decision {
	if observation.RecoveryRunning {
		return Decision{Reason: "recovery-already-running"}
	}
	if observation.ActiveIntents == 0 {
		return Decision{Reason: "no-admitted-demand"}
	}
	stuck := make([]string, 0, len(observation.PendingCreates)+len(observation.OverdueRetries)+len(observation.StaleAssigned))
	overdueRetry := false
	staleAssigned := false
	for _, pending := range observation.PendingCreates {
		if pending.CreateAttempt == 0 && pending.Age >= policy.MinimumStuckAge {
			stuck = append(stuck, pending.ID)
		}
	}
	for _, retry := range observation.OverdueRetries {
		if retry.OverdueAge >= policy.MinimumStuckAge {
			stuck = append(stuck, retry.ID)
			overdueRetry = true
		}
	}
	for _, assigned := range observation.StaleAssigned {
		if assigned.Age >= policy.MinimumStuckAge {
			stuck = append(stuck, assigned.ID)
			staleAssigned = true
		}
	}
	if len(stuck) == 0 {
		return Decision{Reason: "no-stale-undispatched-instance"}
	}
	// A process-wide heartbeat proves only that some dispatcher work advanced.
	// It cannot clear an exact retry that is already overdue: production has
	// shown one scale set parked while sibling classes kept the heartbeat fresh.
	if !overdueRetry && !staleAssigned && !observation.HeartbeatAt.IsZero() && observation.ObservedAt.Sub(observation.HeartbeatAt) < policy.HeartbeatStale {
		return Decision{Reason: "dispatcher-heartbeat-current", Stuck: stuck}
	}
	if observation.ManagerUptime < policy.MinimumUptime {
		return Decision{Reason: "manager-startup-grace", Stuck: stuck}
	}
	if !observation.LastRecoveryAt.IsZero() && observation.ObservedAt.Sub(observation.LastRecoveryAt) < policy.Cooldown {
		return Decision{Reason: "recovery-cooldown", Stuck: stuck}
	}
	reason := "stale-pending-create-attempt-zero"
	if overdueRetry {
		reason = "stale-provider-retry-past-next-allowed"
	}
	if staleAssigned {
		reason = "stale-assigned-intent-without-instance"
	}
	return Decision{Recover: true, Reason: reason, Stuck: stuck}
}
