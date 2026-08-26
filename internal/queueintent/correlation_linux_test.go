//go:build linux

package queueintent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunningCorrelationBindsAuthenticatedRunnerExactlyAndIdempotently(t *testing.T) {
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	path := filepath.Join(directory, "queue-intents.json")
	lockPath := filepath.Join(directory, "queue-intents.lock")
	key := intentKey(42, "job-1")
	writeCorrelationFixture(t, path, Journal{
		SchemaVersion: SchemaVersion, Generation: 7, UpdatedAt: now,
		Intents: map[string]Intent{key: {
			Key: key, ScaleSetID: 42, JobID: "job-1", RunnerRequestID: 99,
			ScaleSetName: "example-standard", RunnerName: "runner-exact", Owner: "example-org",
			Repository: "example-org", WorkflowRef: "unavailable-before-job-available",
			EventName: "workflow_dispatch", QueueTime: now.Add(-time.Minute), State: StateRunning,
			Priority: 1, StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}}, Repositories: map[string]RepositoryState{}, TerminalJobs: map[string]time.Time{},
	})
	correlator := Correlator{Path: path, LockPath: lockPath, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1}
	correlation := RunningCorrelation{
		RunnerName: "runner-exact", PoolName: "example-standard", Repository: "example-org/example-repo", WorkflowRunID: 1234,
		JobDisplayName: "quality", WorkflowRef: "example-org/example-repo/.github/workflows/ci.yml@refs/heads/main",
	}
	result, err := correlator.BindRunning(context.Background(), correlation)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.Key != key || result.Generation != 8 {
		t.Fatalf("result=%+v", result)
	}
	snapshot, err := (Reader{Path: path, Now: func() time.Time { return now.Add(time.Second) }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	intent := snapshot.Active[0]
	if intent.Repository != correlation.Repository || intent.WorkflowRunID != correlation.WorkflowRunID ||
		intent.JobDisplayName != correlation.JobDisplayName || intent.WorkflowRef != correlation.WorkflowRef {
		t.Fatalf("bound intent=%+v", intent)
	}
	replay, err := correlator.BindRunning(context.Background(), correlation)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Changed || replay.Generation != 8 || replay.Key != key {
		t.Fatalf("replay=%+v", replay)
	}
}

func TestRunningCorrelationRefusesAmbiguousOrConflictingIdentity(t *testing.T) {
	now := time.Date(2026, 8, 26, 18, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	path := filepath.Join(directory, "queue-intents.json")
	lockPath := filepath.Join(directory, "queue-intents.lock")
	intents := map[string]Intent{}
	for index, scaleSetID := range []int64{42, 43} {
		key := intentKey(scaleSetID, "job-"+string(rune('1'+index)))
		intents[key] = Intent{
			Key: key, ScaleSetID: scaleSetID, JobID: "job-" + string(rune('1'+index)), RunnerRequestID: 90 + int64(index),
			ScaleSetName: "example-standard", Owner: "example-org",
			Repository: "example-org/example-repo", WorkflowRef: "example/ref", EventName: "push",
			JobDisplayName: "quality", QueueTime: now.Add(-time.Minute), State: StateAcquired, Priority: 1,
			StateEnteredAt: now.Add(-30 * time.Second), UpdatedAt: now, ExpiresAt: now.Add(time.Hour),
		}
	}
	writeCorrelationFixture(t, path, Journal{SchemaVersion: SchemaVersion, Generation: 3, UpdatedAt: now, Intents: intents, Repositories: map[string]RepositoryState{}, TerminalJobs: map[string]time.Time{}})
	correlator := Correlator{Path: path, LockPath: lockPath, Now: func() time.Time { return now.Add(time.Second) }, Attempts: 1}
	_, err := correlator.BindRunning(context.Background(), RunningCorrelation{
		RunnerName: "runner-collision", PoolName: "example-standard", Repository: "example-org/example-repo", WorkflowRunID: 1234,
		JobDisplayName: "quality", WorkflowRef: "example/ref",
	})
	if err == nil || errors.Is(err, ErrRunningCorrelationNotReady) {
		t.Fatalf("ambiguous identity err=%v", err)
	}
}

func TestRunningCorrelationWaitsForExactRunnerAndRepository(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "queue-intents.json")
	writeCorrelationFixture(t, path, Journal{SchemaVersion: SchemaVersion, Intents: map[string]Intent{}, Repositories: map[string]RepositoryState{}, TerminalJobs: map[string]time.Time{}})
	_, err := (Correlator{Path: path, LockPath: filepath.Join(directory, "queue-intents.lock"), Attempts: 1}).BindRunning(context.Background(), RunningCorrelation{
		RunnerName: "runner-missing", PoolName: "example-standard", Repository: "example-org/example-repo", WorkflowRunID: 1234,
		JobDisplayName: "quality", WorkflowRef: "example/ref",
	})
	if !errors.Is(err, ErrRunningCorrelationNotReady) {
		t.Fatalf("missing exact intent err=%v", err)
	}
}

func writeCorrelationFixture(t *testing.T, path string, journal Journal) {
	t.Helper()
	raw, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
