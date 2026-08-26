//go:build linux

package queueintent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverUnboundRunningUsesExactCASAndGeneration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "queue-intents.json")
	lockPath := filepath.Join(directory, "queue-intents.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Nanosecond)
	key := intentKey(1, "stale-job")
	journal := Journal{
		SchemaVersion: SchemaVersion, Generation: 7, UpdatedAt: updatedAt,
		Intents: map[string]Intent{key: {
			Key: key, ScaleSetID: 1, JobID: "stale-job", ScaleSetName: "nddev-linux-standard",
			Owner: "example-org", Repository: "example-org",
			WorkflowRef: "unavailable-before-job-available", EventName: "unavailable-before-job-available",
			QueueTime: updatedAt.Add(-time.Minute), State: StateRunning, Priority: 2,
			StateEnteredAt: updatedAt, UpdatedAt: updatedAt, ExpiresAt: updatedAt.Add(24 * time.Hour),
		}},
		Repositories: map[string]RepositoryState{"example-org": {Repository: "example-org", Weight: 1}},
		TerminalJobs: map[string]time.Time{},
	}
	content, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalPath, 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := RecoverUnboundRunning(context.Background(), journalPath, lockPath, key, updatedAt, false)
	if err != nil || dry.Applied || dry.Generation != 7 {
		t.Fatalf("dry recovery=%#v err=%v", dry, err)
	}
	applied, err := RecoverUnboundRunning(context.Background(), journalPath, lockPath, key, updatedAt, true)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || applied.PreviousGeneration != 7 || applied.Generation != 8 {
		t.Fatalf("applied recovery=%#v", applied)
	}
	observed, err := readJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := observed.Intents[key]; exists || observed.Generation != 8 {
		t.Fatalf("recovered journal=%#v", observed)
	}
	if _, err := RecoverUnboundRunning(context.Background(), journalPath, lockPath, key, updatedAt, true); err == nil {
		t.Fatal("replay unexpectedly removed an absent intent")
	}
}

func TestRecoverUnboundRunningRejectsChangedPrecondition(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "queue-intents.json")
	lockPath := filepath.Join(directory, "queue-intents.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := intentKey(1, "bound-job")
	journal := Journal{SchemaVersion: SchemaVersion, TerminalJobs: map[string]time.Time{}, Intents: map[string]Intent{key: {
		Key: key, ScaleSetID: 1, JobID: "bound-job", RunnerRequestID: 42,
		ScaleSetName: "nddev-linux-standard", Owner: "example-org", Repository: "example-org/repository",
		WorkflowRef: "workflow.yml@refs/heads/main", EventName: "push", QueueTime: updatedAt,
		State: StateRunning, Priority: 2, StateEnteredAt: updatedAt, UpdatedAt: updatedAt, ExpiresAt: updatedAt.Add(24 * time.Hour),
	}}, Repositories: map[string]RepositoryState{"example-org/repository": {Repository: "example-org/repository", Weight: 1}}}
	content, _ := json.Marshal(journal)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(journalPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverUnboundRunning(context.Background(), journalPath, lockPath, key, updatedAt, true); err == nil {
		t.Fatal("bound running intent was recoverable")
	}
}

func TestRecoverCanceledUnboundAcceptsOnlySparseQueuedOrAssigned(t *testing.T) {
	t.Parallel()
	for _, state := range []State{StateQueued, StateAssigned} {
		t.Run(string(state), func(t *testing.T) {
			directory := t.TempDir()
			journalPath := filepath.Join(directory, "queue-intents.json")
			lockPath := filepath.Join(directory, "queue-intents.lock")
			updatedAt := time.Now().UTC().Add(-time.Hour)
			key := intentKey(6, "canceled-job")
			journal := Journal{SchemaVersion: SchemaVersion, TerminalJobs: map[string]time.Time{}, Intents: map[string]Intent{key: {
				Key: key, ScaleSetID: 6, JobID: "canceled-job", ScaleSetName: "nddev-linux-untrusted",
				Owner: "example-org", Repository: "example-org",
				WorkflowRef: "unavailable-before-job-available", EventName: "unavailable-before-job-available",
				QueueTime: updatedAt, State: state, Priority: 2, StateEnteredAt: updatedAt, UpdatedAt: updatedAt, ExpiresAt: updatedAt.Add(time.Hour),
			}}, Repositories: map[string]RepositoryState{"example-org": {Repository: "example-org", Weight: 1}}}
			content, _ := json.Marshal(journal)
			if err := os.WriteFile(journalPath, content, 0o600); err != nil {
				t.Fatal(err)
			}
			result, err := RecoverCanceledUnbound(context.Background(), journalPath, lockPath, key, updatedAt, true)
			if err != nil || !result.Applied {
				t.Fatalf("recovery=%#v err=%v", result, err)
			}
		})
	}
}
