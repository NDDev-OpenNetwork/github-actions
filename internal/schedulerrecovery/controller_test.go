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

func TestControllerReportsHealthyCurrentHeartbeat(t *testing.T) {
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
	require.Equal(t, "healthy", events.events[0].State)
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
