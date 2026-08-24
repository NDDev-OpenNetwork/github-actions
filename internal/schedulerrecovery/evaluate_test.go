package schedulerrecovery

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvaluateRequiresExactStuckSignature(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	policy := Policy{MinimumStuckAge: 90 * time.Second, MinimumUptime: 2 * time.Minute, Cooldown: 10 * time.Minute}
	base := Observation{
		ObservedAt: now, ActiveIntents: 1, ManagerUptime: 3 * time.Minute,
		PendingCreates: []PendingCreate{{ID: "instance-1", Age: 91 * time.Second}},
	}
	require.Equal(t, Decision{Recover: true, Reason: "stale-pending-create-attempt-zero", Stuck: []string{"instance-1"}}, Evaluate(policy, base))

	withoutDemand := base
	withoutDemand.ActiveIntents = 0
	require.Equal(t, "no-admitted-demand", Evaluate(policy, withoutDemand).Reason)

	alreadyAttempted := base
	alreadyAttempted.PendingCreates[0].CreateAttempt = 1
	require.Equal(t, "no-stale-undispatched-instance", Evaluate(policy, alreadyAttempted).Reason)
}

func TestEvaluatePreventsDuplicateRecovery(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	policy := Policy{MinimumStuckAge: 90 * time.Second, MinimumUptime: 2 * time.Minute, Cooldown: 10 * time.Minute}
	observation := Observation{
		ObservedAt: now, ActiveIntents: 1, ManagerUptime: time.Hour,
		PendingCreates: []PendingCreate{{ID: "instance-1", Age: 2 * time.Minute}},
		LastRecoveryAt: now.Add(-9 * time.Minute),
	}
	require.Equal(t, "recovery-cooldown", Evaluate(policy, observation).Reason)
	observation.RecoveryRunning = true
	require.Equal(t, "recovery-already-running", Evaluate(policy, observation).Reason)
}
