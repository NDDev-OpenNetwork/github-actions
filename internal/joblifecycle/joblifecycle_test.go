package joblifecycle

import (
	"path/filepath"
	"testing"
	"time"
)

func journalWith(intents map[string]intentView, terminal map[string]time.Time) journalView {
	return journalView{SchemaVersion: 1, Generation: 7, Intents: intents, TerminalJobs: terminal}
}

func intent(key, state string, entered time.Time) intentView {
	return intentView{Key: key, State: state, StateEnteredAt: entered,
		Repository: "example-org/example-repo", WorkflowRunID: 42, ScaleSetName: "example-standard",
		FirstQueuedAt: entered.Add(-30 * time.Second)}
}

func TestDiffEmitsEachTransitionExactlyOnce(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	journal := journalWith(map[string]intentView{"job-1": intent("job-1", "queued", now)}, nil)
	records, marks := Diff(emptyWatermarks(), journal, now)
	if len(records) != 1 || records[0].State != "queued" || records[0].Key != "job-1" {
		t.Fatalf("records=%+v", records)
	}
	if records[0].WaitedSeconds != 30 {
		t.Fatalf("waited=%v, want 30", records[0].WaitedSeconds)
	}

	// The same journal again says nothing new.
	again, marks2 := Diff(marks, journal, now.Add(time.Minute))
	if len(again) != 0 {
		t.Fatalf("unchanged journal produced %+v", again)
	}

	// A state change speaks once, with the journal's own entry time.
	entered := now.Add(2 * time.Minute)
	journal = journalWith(map[string]intentView{"job-1": intent("job-1", "assigned", entered)}, nil)
	records, marks3 := Diff(marks2, journal, now.Add(3*time.Minute))
	if len(records) != 1 || records[0].State != "assigned" || !records[0].Timestamp.Equal(entered) {
		t.Fatalf("records=%+v", records)
	}

	// Reaching the terminal map closes the story exactly once.
	journal = journalWith(map[string]intentView{}, map[string]time.Time{"job-1": now.Add(time.Hour)})
	records, marks4 := Diff(marks3, journal, now.Add(4*time.Minute))
	if len(records) != 1 || records[0].State != "completed" {
		t.Fatalf("records=%+v", records)
	}
	records, marks5 := Diff(marks4, journal, now.Add(5*time.Minute))
	if len(records) != 0 {
		t.Fatalf("closed intent spoke twice: %+v", records)
	}

	// Once the journal forgets the key entirely, the watermark forgets too.
	records, marks6 := Diff(marks5, journalWith(map[string]intentView{}, nil), now.Add(6*time.Minute))
	if len(records) != 0 || len(marks6.Closed) != 0 || len(marks6.States) != 0 {
		t.Fatalf("records=%+v marks=%+v", records, marks6)
	}
}

func TestDiffTellsReleasedFromVanished(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	journal := journalWith(map[string]intentView{"job-2": intent("job-2", "running", now)}, nil)
	_, marks := Diff(emptyWatermarks(), journal, now)
	// Gone from RUNNING without a terminal record: the broker's reclaim
	// released it after its lease ended -- the fleet's ordinary finish line.
	records, marks2 := Diff(marks, journalWith(map[string]intentView{}, nil), now.Add(time.Minute))
	if len(records) != 1 || records[0].State != "released" {
		t.Fatalf("records=%+v", records)
	}
	_ = marks2

	// Gone from a PRE-running state is the orphan class: it never provably
	// ran anywhere, and it keeps the vanished name.
	journal = journalWith(map[string]intentView{"job-3": intent("job-3", "assigned", now)}, nil)
	_, marks3 := Diff(emptyWatermarks(), journal, now)
	records, _ = Diff(marks3, journalWith(map[string]intentView{}, nil), now.Add(time.Minute))
	if len(records) != 1 || records[0].State != "vanished" {
		t.Fatalf("records=%+v", records)
	}
}

func TestWatermarksRoundTripAndRefuseForeignSchema(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "marks.json")
	marks := emptyWatermarks()
	marks.States["job-1"] = "queued"
	if err := WriteWatermarks(path, marks); err != nil {
		t.Fatal(err)
	}
	read, err := ReadWatermarks(path)
	if err != nil || read.States["job-1"] != "queued" {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	missing, err := ReadWatermarks(filepath.Join(directory, "absent.json"))
	if err != nil || len(missing.States) != 0 {
		t.Fatalf("missing watermark file must read empty: %+v %v", missing, err)
	}
}
