package schedulerrecovery

import (
	"testing"
	"time"
)

func TestAssignedOnlyBlockerPreservesExactRecovery(t *testing.T) {
	policy := Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute}
	for _, kind := range []string{"pending", "retry"} {
		t.Run(kind, func(t *testing.T) {
			observation := Observation{
				ObservedAt: time.Now(), ActiveIntents: 2, ManagerUptime: time.Hour,
				StaleAssigned:        []AssignedIntent{{ID: "assigned-a", Age: time.Hour}},
				RestartBlockers:      []string{"assigned-only-active-provider-work"},
				CapacityBackpressure: true,
			}
			if kind == "pending" {
				observation.PendingCreates = []PendingCreate{{ID: "pending-b", Age: time.Hour}}
			} else {
				observation.OverdueRetries = []ProviderRetry{{ID: "retry-b", OverdueAge: time.Hour}}
			}
			if decision := Evaluate(policy, observation); !decision.Recover {
				t.Fatalf("exact create path unexpectedly blocked: %+v", decision)
			}
			observation.RestartBlockers = append(observation.RestartBlockers, "provider-journal-unknown")
			if decision := Evaluate(policy, observation); decision.Recover {
				t.Fatal("unknown provider state bypassed")
			}
		})
	}
}
