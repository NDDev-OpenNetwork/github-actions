package schedulerrecovery

import "time"

type Policy struct {
	MinimumStuckAge time.Duration
	MinimumUptime   time.Duration
	Cooldown        time.Duration
}

type PendingCreate struct {
	ID            string
	Age           time.Duration
	CreateAttempt int
}

type Observation struct {
	ObservedAt      time.Time
	ActiveIntents   int
	PendingCreates  []PendingCreate
	ManagerUptime   time.Duration
	LastRecoveryAt  time.Time
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
	stuck := make([]string, 0, len(observation.PendingCreates))
	for _, pending := range observation.PendingCreates {
		if pending.CreateAttempt == 0 && pending.Age >= policy.MinimumStuckAge {
			stuck = append(stuck, pending.ID)
		}
	}
	if len(stuck) == 0 {
		return Decision{Reason: "no-stale-undispatched-instance"}
	}
	if observation.ManagerUptime < policy.MinimumUptime {
		return Decision{Reason: "manager-startup-grace", Stuck: stuck}
	}
	if !observation.LastRecoveryAt.IsZero() && observation.ObservedAt.Sub(observation.LastRecoveryAt) < policy.Cooldown {
		return Decision{Reason: "recovery-cooldown", Stuck: stuck}
	}
	return Decision{Recover: true, Reason: "stale-pending-create-attempt-zero", Stuck: stuck}
}
