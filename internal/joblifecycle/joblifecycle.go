// Package joblifecycle turns the queue journal's current-state snapshots into
// a durable per-transition stream.
//
// Latency questions -- where did a job's wait go? -- were answered by
// correlating GARM journals by hand. The journal holds each intent's CURRENT
// state with the time it was entered; sampling it and emitting one record per
// observed transition yields the whole queued-acquiring-acquired-assigned-
// running ladder with the journal's own timestamps, plus a closing record when
// the intent reaches the terminal map or vanishes without it.
//
// The journal is read tolerantly on purpose: this package needs a stable
// handful of fields, and a strict reader would turn every journal schema
// addition into a two-deploy dance for a read-only exporter.
package joblifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

const (
	// StreamName is the OpenObserve logs stream the records land in.
	StreamName = "gha_fleet_job_lifecycle"
	// SchemaVersion stamps the watermark state file.
	SchemaVersion         = 1
	maximumJournalBytes   = 16 * 1024 * 1024
	maximumWatermarkBytes = 4 * 1024 * 1024
)

// Record is one observed lifecycle transition for one job intent.
type Record struct {
	// Timestamp is the journal's own state_entered_at for live transitions
	// and the observation time for closing records, where the journal keeps
	// no entry time.
	Timestamp      time.Time `json:"_timestamp"`
	Key            string    `json:"key"`
	State          string    `json:"state"`
	Repository     string    `json:"repository,omitempty"`
	WorkflowRunID  int64     `json:"workflow_run_id,omitempty"`
	JobID          string    `json:"job_id,omitempty"`
	JobDisplayName string    `json:"job_display_name,omitempty"`
	ScaleSet       string    `json:"scale_set,omitempty"`
	Owner          string    `json:"owner,omitempty"`
	RunnerName     string    `json:"runner_name,omitempty"`
	GitHubRunnerID int64     `json:"github_runner_id,omitempty"`
	EventName      string    `json:"event_name,omitempty"`
	WorkflowRef    string    `json:"workflow_ref,omitempty"`
	Priority       int       `json:"priority,omitempty"`
	// WaitedSeconds is how long the intent had been in flight when this
	// state was entered, measured from its first queue time.
	WaitedSeconds float64 `json:"waited_seconds,omitempty"`
}

// intentView is the tolerant read of one journal intent.
type intentView struct {
	Key            string    `json:"key"`
	JobID          string    `json:"job_id"`
	WorkflowRunID  int64     `json:"workflow_run_id"`
	JobDisplayName string    `json:"job_display_name"`
	GitHubRunnerID int64     `json:"github_runner_id"`
	ScaleSetName   string    `json:"scale_set_name"`
	RunnerName     string    `json:"runner_name"`
	Owner          string    `json:"owner"`
	Repository     string    `json:"repository"`
	WorkflowRef    string    `json:"workflow_ref"`
	EventName      string    `json:"event_name"`
	QueueTime      time.Time `json:"queue_time"`
	FirstQueuedAt  time.Time `json:"first_queued_at"`
	State          string    `json:"state"`
	Priority       int       `json:"priority"`
	StateEnteredAt time.Time `json:"state_entered_at"`
}

// journalView is the tolerant read of the queue journal.
type journalView struct {
	SchemaVersion int                   `json:"schema_version"`
	Generation    uint64                `json:"generation"`
	Intents       map[string]intentView `json:"intents"`
	TerminalJobs  map[string]time.Time  `json:"terminal_jobs"`
}

// Watermarks is what has already been said, so a transition is said once.
type Watermarks struct {
	SchemaVersion int `json:"schema_version"`
	// States maps intent key to the last state a record was emitted for.
	States map[string]string `json:"states"`
	// Closed maps intent keys whose closing record was emitted to the time
	// it was; entries are pruned once the journal forgets the key entirely.
	Closed map[string]time.Time `json:"closed"`
}

func emptyWatermarks() Watermarks {
	return Watermarks{SchemaVersion: SchemaVersion, States: map[string]string{}, Closed: map[string]time.Time{}}
}

// ReadJournal loads the queue journal tolerantly.
func ReadJournal(path string) (journalView, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return journalView{}, fmt.Errorf("read queue journal: %w", err)
	}
	if len(raw) == 0 || len(raw) > maximumJournalBytes {
		return journalView{}, fmt.Errorf("queue journal must contain 1..%d bytes", maximumJournalBytes)
	}
	var journal journalView
	if err := json.Unmarshal(raw, &journal); err != nil {
		return journalView{}, fmt.Errorf("decode queue journal: %w", err)
	}
	if journal.SchemaVersion < 1 {
		return journalView{}, errors.New("queue journal schema_version is invalid")
	}
	return journal, nil
}

// ReadWatermarks loads the export state; a missing file is an empty state.
func ReadWatermarks(path string) (Watermarks, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyWatermarks(), nil
	}
	if err != nil {
		return Watermarks{}, fmt.Errorf("read lifecycle watermarks: %w", err)
	}
	if len(raw) == 0 || len(raw) > maximumWatermarkBytes {
		return Watermarks{}, errors.New("lifecycle watermark size is invalid")
	}
	var marks Watermarks
	if err := json.Unmarshal(raw, &marks); err != nil {
		return Watermarks{}, fmt.Errorf("decode lifecycle watermarks: %w", err)
	}
	if marks.SchemaVersion != SchemaVersion {
		return Watermarks{}, errors.New("lifecycle watermark schema_version is unsupported")
	}
	if marks.States == nil {
		marks.States = map[string]string{}
	}
	if marks.Closed == nil {
		marks.Closed = map[string]time.Time{}
	}
	return marks, nil
}

// WriteWatermarks stores the export state atomically beside its final path.
func WriteWatermarks(path string, marks Watermarks) (err error) {
	marks.SchemaVersion = SchemaVersion
	encoded, err := json.Marshal(marks)
	if err != nil {
		return fmt.Errorf("encode lifecycle watermarks: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write lifecycle watermarks: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(temporary)
		}
	}()
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("commit lifecycle watermarks: %w", err)
	}
	return nil
}

func record(intent intentView, state string, at time.Time) Record {
	waited := 0.0
	since := intent.FirstQueuedAt
	if since.IsZero() {
		since = intent.QueueTime
	}
	if !since.IsZero() && at.After(since) {
		waited = at.Sub(since).Seconds()
	}
	return Record{
		Timestamp: at, Key: intent.Key, State: state,
		Repository: intent.Repository, WorkflowRunID: intent.WorkflowRunID,
		JobID: intent.JobID, JobDisplayName: intent.JobDisplayName,
		ScaleSet: intent.ScaleSetName, Owner: intent.Owner,
		RunnerName: intent.RunnerName, GitHubRunnerID: intent.GitHubRunnerID,
		EventName: intent.EventName, WorkflowRef: intent.WorkflowRef,
		Priority: intent.Priority, WaitedSeconds: waited,
	}
}

// Diff emits every transition the watermarks have not yet said and returns
// the advanced watermarks. It is pure: the caller persists the state only
// after the records are durably delivered, so a failed export retries.
func Diff(marks Watermarks, journal journalView, now time.Time) ([]Record, Watermarks) {
	next := Watermarks{SchemaVersion: SchemaVersion,
		States: make(map[string]string, len(journal.Intents)),
		Closed: make(map[string]time.Time, len(marks.Closed))}
	records := make([]Record, 0, 8)
	for key, intent := range journal.Intents {
		if intent.Key == "" {
			intent.Key = key
		}
		next.States[key] = intent.State
		if marks.States[key] == intent.State {
			continue
		}
		at := intent.StateEnteredAt
		if at.IsZero() {
			at = now
		}
		records = append(records, record(intent, intent.State, at))
	}
	for key, closedAt := range marks.Closed {
		_, live := journal.Intents[key]
		_, terminal := journal.TerminalJobs[key]
		if live || terminal {
			next.Closed[key] = closedAt
		}
	}
	for key, lastState := range marks.States {
		if _, live := journal.Intents[key]; live {
			continue
		}
		if _, alreadyClosed := next.Closed[key]; alreadyClosed {
			continue
		}
		// Three ways an intent leaves the journal, told apart by where it was
		// last seen. The terminal map is GARM's own completion record. A
		// RUNNING intent that disappears without one was released by the
		// broker's reclaim after its execution lease ended -- the fleet's
		// ordinary finish line, measured at ~2 per minute. An intent that
		// disappears from any EARLIER state never ran anywhere this fleet can
		// prove, and that is the orphan class worth a name of its own.
		state := "vanished"
		if _, terminal := journal.TerminalJobs[key]; terminal {
			state = "completed"
		} else if lastState == "running" {
			state = "released"
		}
		records = append(records, Record{Timestamp: now, Key: key, State: state,
			JobDisplayName: "", Repository: "", WorkflowRunID: 0})
		next.Closed[key] = now
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Timestamp.Equal(records[j].Timestamp) {
			return records[i].Key < records[j].Key
		}
		return records[i].Timestamp.Before(records[j].Timestamp)
	})
	return records, next
}
