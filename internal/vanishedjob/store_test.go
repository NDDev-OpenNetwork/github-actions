package vanishedjob

import (
	"path/filepath"
	"testing"
	"time"
)

func TestFileStorePersistsCASLifecycleAndSuppressesReplay(t *testing.T) {
	directory := t.TempDir()
	store := FileStore{Path: filepath.Join(directory, "state.json"), LockPath: filepath.Join(directory, "state.lock")}
	at := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	record := Record{
		Repository: "example-org/example-repo", RunID: 42, JobID: 84, RunnerID: 21,
		RunnerName: "example-runner", ScaleSet: "example-ci", OriginalAttempt: 1,
		Stage: StageDetected, UpdatedAt: at,
	}
	created, err := store.Begin(record)
	if err != nil || !created {
		t.Fatalf("begin=%t err=%v", created, err)
	}
	reopened := FileStore{Path: store.Path, LockPath: store.LockPath}
	created, err = reopened.Begin(record)
	if err != nil || created {
		t.Fatalf("duplicate begin=%t err=%v", created, err)
	}
	key := RecordKey(record.Repository, record.RunID, record.OriginalAttempt)
	if err := reopened.Advance(key, StageDetected, StageCancelRequested, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Advance(key, StageDetected, StageRerunRequested, at.Add(2*time.Minute)); err == nil {
		t.Fatal("stale stage compare-and-swap succeeded")
	}
	if err := reopened.Advance(key, StageCancelRequested, StageRerunRequested, at.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	result := Result{Key: key, OriginalAttempt: 1, ReplacementAttempt: 2, Conclusion: "success", FinishedAt: at.Add(3 * time.Minute)}
	if err := reopened.Finish(key, StageRerunRequested, result); err != nil {
		t.Fatal(err)
	}
	if current, err := store.Get(key); err != nil || current != nil {
		t.Fatalf("finished record=%#v err=%v", current, err)
	}
	created, err = store.Begin(record)
	if err != nil || created {
		t.Fatalf("finished replay begin=%t err=%v", created, err)
	}
	state, err := readState(store.Path)
	if err != nil || len(state.Finished) != 1 || state.Generation != 4 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}
