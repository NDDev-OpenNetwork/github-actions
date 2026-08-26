package schedulerrecovery

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type memoryAttempts struct {
	mu       sync.Mutex
	active   map[string]Attempt
	finished []Result
}

func (store *memoryAttempts) Begin(_ context.Context, attempt Attempt) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.active == nil {
		store.active = map[string]Attempt{}
	}
	if _, exists := store.active[attempt.ID]; exists {
		return false, nil
	}
	for _, result := range store.finished {
		if result.AttemptID == attempt.ID {
			return false, nil
		}
	}
	store.active[attempt.ID] = attempt
	return true, nil
}

func (store *memoryAttempts) Finish(_ context.Context, result Result) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.finished = append(store.finished, result)
	delete(store.active, result.AttemptID)
	return nil
}

func (store *memoryAttempts) Active(_ context.Context) ([]Attempt, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	attempts := make([]Attempt, 0, len(store.active))
	for _, attempt := range store.active {
		attempts = append(attempts, attempt)
	}
	return attempts, nil
}

type faultExecutor struct {
	mu          sync.Mutex
	checkpoints int
	restarts    int
	progressed  []string
	remaining   []string
	checkpoint  error
}

func (executor *faultExecutor) Checkpoint(_ context.Context, _ Attempt) (string, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.checkpoints++
	return "checkpoint-1", executor.checkpoint
}

func (executor *faultExecutor) RestartDispatcher(_ context.Context, _ Attempt) error {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.restarts++
	return nil
}

func (executor *faultExecutor) AwaitProgress(_ context.Context, _ Attempt) ([]string, []string, error) {
	return executor.progressed, executor.remaining, nil
}

func TestRecoverFaultInjectedDispatcherExactlyOnce(t *testing.T) {
	t.Parallel()
	observedAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	decision := Decision{Recover: true, Reason: "stale-pending-create-attempt-zero", Stuck: []string{"instance-2", "instance-1"}}
	store := &memoryAttempts{}
	executor := &faultExecutor{progressed: []string{"instance-1", "instance-2"}}
	now := func() time.Time { return observedAt.Add(time.Minute) }

	var wait sync.WaitGroup
	results := make([]Result, 2)
	errorsFound := make([]error, 2)
	for index := range results {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results[index], errorsFound[index] = Recover(context.Background(), observedAt, decision, store, executor, now)
		}()
	}
	wait.Wait()
	require.NoError(t, errorsFound[0])
	require.NoError(t, errorsFound[1])
	require.Equal(t, 1, executor.checkpoints)
	require.Equal(t, 1, executor.restarts)
	require.Len(t, store.finished, 1)
	require.True(t, store.finished[0].Recovered)
}

func TestRecoverNeverRestartsWithoutCheckpoint(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	store := &memoryAttempts{}
	executor := &faultExecutor{checkpoint: errors.New("backup unavailable")}
	result, err := Recover(context.Background(), at, Decision{Recover: true, Stuck: []string{"instance-1"}}, store, executor, func() time.Time { return at })
	require.ErrorContains(t, err, "checkpoint scheduler state")
	require.Zero(t, executor.restarts)
	require.Empty(t, result.Checkpoint)
	require.Len(t, store.finished, 1)
	require.False(t, result.Recovered)
}
