package schedulerrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Attempt struct {
	ID        string
	StartedAt time.Time
	Stuck     []string
}

type Result struct {
	AttemptID  string
	Suppressed bool
	Checkpoint string
	Progressed []string
	Remaining  []string
	Recovered  bool
	FinishedAt time.Time
	Error      string
}

type AttemptStore interface {
	Active(context.Context) ([]Attempt, error)
	Begin(context.Context, Attempt) (bool, error)
	Finish(context.Context, Result) error
}

func resumeAcquired(ctx context.Context, attempt Attempt, store AttemptStore, executor Executor, now func() time.Time) (Result, error) {
	progressed, remaining, progressErr := executor.AwaitProgress(ctx, attempt)
	if progressErr == nil && len(remaining) == 0 {
		result := Result{
			AttemptID: attempt.ID, Progressed: slices.Clone(progressed), Recovered: true,
			FinishedAt: now().UTC(),
		}
		if err := store.Finish(ctx, result); err != nil {
			return result, fmt.Errorf("finish resumed recovery attempt: %w", err)
		}
		return result, nil
	}
	// The previous process may have died before or during the manager restart.
	// Re-running the checkpoint-first sequence is idempotent; restricting it to
	// the still-stuck identities avoids replaying work already proven progressed.
	if len(remaining) > 0 {
		attempt.Stuck = slices.Clone(remaining)
	}
	return recoverAcquired(ctx, attempt, store, executor, now)
}

type Executor interface {
	Checkpoint(context.Context, Attempt) (string, error)
	RestartDispatcher(context.Context, Attempt) error
	AwaitProgress(context.Context, Attempt) (progressed, remaining []string, err error)
}

func NewAttempt(observedAt time.Time, stuck []string) Attempt {
	ordered := slices.Clone(stuck)
	slices.Sort(ordered)
	payload := observedAt.UTC().Format(time.RFC3339Nano) + "\x00" + strings.Join(ordered, "\x00")
	digest := sha256.Sum256([]byte(payload))
	return Attempt{ID: hex.EncodeToString(digest[:]), StartedAt: observedAt.UTC(), Stuck: ordered}
}

func Recover(ctx context.Context, observedAt time.Time, decision Decision, store AttemptStore, executor Executor, now func() time.Time) (Result, error) {
	if !decision.Recover {
		return Result{}, fmt.Errorf("recovery refused: %s", decision.Reason)
	}
	attempt := NewAttempt(observedAt, decision.Stuck)
	acquired, err := store.Begin(ctx, attempt)
	if err != nil {
		return Result{}, fmt.Errorf("begin recovery attempt: %w", err)
	}
	if !acquired {
		return Result{AttemptID: attempt.ID, Suppressed: true}, nil
	}
	return recoverAcquired(ctx, attempt, store, executor, now)
}

func recoverAcquired(ctx context.Context, attempt Attempt, store AttemptStore, executor Executor, now func() time.Time) (Result, error) {
	result := Result{AttemptID: attempt.ID}
	finish := func(operationErr error) (Result, error) {
		result.FinishedAt = now().UTC()
		if operationErr != nil {
			result.Error = operationErr.Error()
		}
		if err := store.Finish(ctx, result); err != nil {
			return result, fmt.Errorf("finish recovery attempt: %w", err)
		}
		return result, operationErr
	}
	checkpoint, err := executor.Checkpoint(ctx, attempt)
	if err != nil {
		return finish(fmt.Errorf("checkpoint scheduler state: %w", err))
	}
	result.Checkpoint = checkpoint
	if err := executor.RestartDispatcher(ctx, attempt); err != nil {
		return finish(fmt.Errorf("restart dispatcher: %w", err))
	}
	progressed, remaining, err := executor.AwaitProgress(ctx, attempt)
	result.Progressed = slices.Clone(progressed)
	result.Remaining = slices.Clone(remaining)
	if err != nil {
		return finish(fmt.Errorf("verify dispatcher progress: %w", err))
	}
	result.Recovered = len(remaining) == 0
	if !result.Recovered {
		return finish(fmt.Errorf("recovery incomplete: %d stuck instances remain", len(remaining)))
	}
	return finish(nil)
}
