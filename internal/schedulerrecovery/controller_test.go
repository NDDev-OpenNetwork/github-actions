package schedulerrecovery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type staticObserver struct{ observation Observation }

func (observer staticObserver) Observe(context.Context) (Observation, error) {
	return observer.observation, nil
}

type staticHeartbeat struct{ heartbeat Heartbeat }

func (reader staticHeartbeat) ReadHeartbeat(context.Context) (Heartbeat, error) {
	return reader.heartbeat, nil
}

type eventRecorder struct{ events []Event }

func (recorder *eventRecorder) Emit(_ context.Context, event Event) error {
	recorder.events = append(recorder.events, event)
	return nil
}

func TestControllerRecoversFaultInjectedStoppedDispatcher(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := &memoryAttempts{}
	executor := &faultExecutor{progressed: []string{"instance-1"}}
	events := &eventRecorder{}
	controller := Controller{
		Policy: Policy{MinimumStuckAge: 90 * time.Second, MinimumUptime: 2 * time.Minute, Cooldown: 10 * time.Minute, HeartbeatStale: time.Minute},
		Observer: staticObserver{Observation{
			ObservedAt: at, ActiveIntents: 1, ManagerUptime: time.Hour,
			PendingCreates: []PendingCreate{{ID: "instance-1", Age: 2 * time.Minute}},
		}},
		Heartbeat: staticHeartbeat{Heartbeat{At: at.Add(-2 * time.Minute), Progress: "job-before-stall"}},
		Attempts:  store, Executor: executor, Events: events,
		Now: func() time.Time { return at.Add(time.Minute) },
	}
	decision, result, err := controller.Tick(context.Background())
	require.NoError(t, err)
	require.True(t, decision.Recover)
	require.True(t, result.Recovered)
	require.Equal(t, 1, executor.restarts)
	require.Equal(t, []string{"unhealthy", "recovering", "recovered"}, []string{events.events[0].State, events.events[1].State, events.events[2].State})

	_, replay, err := controller.Tick(context.Background())
	require.NoError(t, err)
	require.True(t, replay.Suppressed)
	require.False(t, replay.Recovered)
	require.Equal(t, 1, executor.restarts)
}

func TestControllerKeepsStalledWorkUnhealthyDuringCurrentHeartbeat(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	events := &eventRecorder{}
	controller := Controller{
		Policy:    Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute, Cooldown: time.Minute, HeartbeatStale: time.Minute},
		Observer:  staticObserver{Observation{ObservedAt: at, ActiveIntents: 1, ManagerUptime: time.Hour, PendingCreates: []PendingCreate{{ID: "instance-1", Age: 2 * time.Minute}}}},
		Heartbeat: staticHeartbeat{Heartbeat{At: at.Add(-time.Second), Progress: "job-1"}},
		Attempts:  &memoryAttempts{}, Executor: &faultExecutor{}, Events: events, Now: func() time.Time { return at },
	}
	decision, _, err := controller.Tick(context.Background())
	require.NoError(t, err)
	require.False(t, decision.Recover)
	require.Equal(t, "dispatcher-heartbeat-current", decision.Reason)
	require.Equal(t, "unhealthy", events.events[0].State)
	require.Equal(t, []string{"instance-1"}, events.events[0].Stuck)
}

func TestControllerSuppressionDoesNotClearIncident(t *testing.T) {
	at := time.Now().UTC()
	for _, reason := range []string{"manager-startup-grace", "recovery-cooldown"} {
		t.Run(reason, func(t *testing.T) {
			observation := Observation{ObservedAt: at, ActiveIntents: 1, ManagerUptime: time.Hour,
				StaleAssigned: []AssignedIntent{{ID: "intent-a", Age: time.Hour}}}
			if reason == "manager-startup-grace" {
				observation.ManagerUptime = time.Second
			} else {
				observation.LastRecoveryAt = at.Add(-time.Second)
			}
			events, executor := &eventRecorder{}, &faultExecutor{}
			controller := Controller{
				Policy:   Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute, Cooldown: time.Minute, HeartbeatStale: time.Minute},
				Observer: staticObserver{observation}, Heartbeat: staticHeartbeat{}, Attempts: &memoryAttempts{},
				Executor: executor, Events: events, Now: func() time.Time { return at },
			}
			decision, _, err := controller.Tick(context.Background())
			require.NoError(t, err)
			require.False(t, decision.Recover)
			require.Equal(t, reason, decision.Reason)
			require.Zero(t, executor.restarts)
			require.Equal(t, "unhealthy", events.events[0].State)
			require.Equal(t, []string{"intent-a"}, events.events[0].Stuck)
		})
	}
}

func TestControllerFinishesInterruptedRecoveryAfterRestartProgressed(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 26, 14, 13, 56, 0, time.UTC)
	attempt := NewAttempt(at, []string{"retry-1"})
	store := &memoryAttempts{active: map[string]Attempt{attempt.ID: attempt}}
	executor := &faultExecutor{progressed: []string{"retry-1"}}
	events := &eventRecorder{}
	controller := Controller{
		Policy:   Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute, Cooldown: time.Minute, HeartbeatStale: time.Minute},
		Observer: staticObserver{}, Heartbeat: staticHeartbeat{}, Attempts: store, Executor: executor,
		Events: events, Now: func() time.Time { return at.Add(time.Minute) },
	}
	decision, result, err := controller.Tick(context.Background())
	require.NoError(t, err)
	require.Equal(t, "resume-interrupted-recovery", decision.Reason)
	require.True(t, result.Recovered)
	require.Zero(t, executor.restarts)
	require.Empty(t, store.active)
	require.Equal(t, []string{"recovering", "recovered"}, []string{events.events[0].State, events.events[1].State})
}

func TestControllerBoundsInfrastructureRetriesAcrossAttemptIDs(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 9, 8, 2, 35, 16, 0, time.UTC)
	store := &memoryAttempts{}
	executor := &faultExecutor{remaining: []string{"intent-a", "intent-b"}}
	events := &eventRecorder{}
	now := at
	controller := Controller{
		Policy: Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute, Cooldown: time.Minute, HeartbeatStale: time.Minute},
		Observer: staticObserver{Observation{
			ObservedAt: at, ActiveIntents: 2, ManagerUptime: time.Hour,
			StaleAssigned: []AssignedIntent{{ID: "intent-a", Age: time.Hour}, {ID: "intent-b", Age: time.Hour}},
		}},
		Heartbeat: staticHeartbeat{}, Attempts: store, Executor: executor, Events: events,
		Now: func() time.Time { return now },
	}
	for attempt := 0; attempt < maxIncidentRecoveryAttempts; attempt++ {
		now = at.Add(time.Duration(attempt) * time.Minute)
		observation := controller.Observer.(staticObserver).observation
		observation.ObservedAt = now
		controller.Observer = staticObserver{observation}
		decision, result, err := controller.Tick(context.Background())
		require.Error(t, err)
		require.True(t, decision.Recover)
		require.False(t, result.Recovered)
		require.Equal(t, attempt+1, executor.restarts)
	}
	now = at.Add(10 * time.Minute)
	observation := controller.Observer.(staticObserver).observation
	observation.ObservedAt = now
	observation.StaleAssigned = []AssignedIntent{
		{ID: "intent-a", Age: time.Hour},
		{ID: "intent-b", Age: time.Hour},
		{ID: "intent-c", Age: time.Hour},
	}
	controller.Observer = staticObserver{observation}
	events.events = nil
	decision, result, err := controller.Tick(context.Background())
	require.NoError(t, err)
	require.False(t, decision.Recover)
	require.Equal(t, "recovery-retry-budget-exhausted", decision.Reason)
	require.False(t, result.Recovered)
	require.Equal(t, maxIncidentRecoveryAttempts, executor.restarts)
	require.Equal(t, "unhealthy", events.events[0].State)
	require.Equal(t, []string{"intent-a", "intent-b", "intent-c"}, decision.Stuck)
}

func TestIncidentRecoveryAttemptsResetAfterSuccessAndIgnoreSuppressed(t *testing.T) {
	t.Parallel()
	stuck := []string{"intent-a"}
	history := []Result{
		{AttemptID: "one", Remaining: []string{"intent-a"}, Error: "incomplete"},
		{AttemptID: "two", Remaining: []string{"intent-a"}, Error: "incomplete"},
		{AttemptID: "dup", Remaining: []string{"intent-a"}, Suppressed: true},
		{AttemptID: "ok", Progressed: []string{"intent-a"}, Recovered: true},
		{AttemptID: "later", Remaining: []string{"intent-a", "intent-z"}, Error: "incomplete"},
	}
	require.Equal(t, 1, incidentRecoveryAttempts(history, stuck))
	require.Equal(t, 0, incidentRecoveryAttempts(history, []string{"unrelated"}))
}
