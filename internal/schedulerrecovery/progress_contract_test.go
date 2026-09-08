package schedulerrecovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProgressPartitionRejectsMissingForeignAndRepeatedIdentities(t *testing.T) {
	cases := []struct {
		name                            string
		expected, progressed, remaining []string
		valid                           bool
	}{
		{"complete", []string{"a", "b"}, []string{"b", "a"}, nil, true},
		{"partial", []string{"a", "b"}, []string{"a"}, []string{"b"}, true},
		{"none", []string{"a"}, nil, []string{"a"}, true},
		{"empty-output", []string{"a"}, nil, nil, false},
		{"foreign", []string{"a"}, []string{"b"}, nil, false},
		{"omitted", []string{"a", "b"}, []string{"a"}, nil, false},
		{"overlap", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"duplicate-progress", []string{"a"}, []string{"a", "a"}, nil, false},
		{"duplicate-expected", []string{"a", "a"}, []string{"a"}, nil, false},
		{"empty-expected", nil, nil, nil, false},
		{"delimiter", []string{"a,b"}, nil, []string{"a,b"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateProgress(tc.expected, tc.progressed, tc.remaining); (err == nil) != tc.valid {
				t.Fatalf("valid=%v, got %v", tc.valid, err)
			}
		})
	}
}

func TestRecoveryBlockersPreserveExactStalledIdentities(t *testing.T) {
	now := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	policy := Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute, Cooldown: time.Minute, HeartbeatStale: time.Minute}
	observation := Observation{ObservedAt: now, ActiveIntents: 1, ManagerUptime: time.Hour,
		StaleAssigned:    []AssignedIntent{{ID: "a", Age: time.Hour}},
		RecoveryBlockers: []string{"provider-leases-active", "provider-journal-unknown"}}
	decision := Evaluate(policy, observation)
	if decision.Recover || len(decision.Stuck) != 1 || decision.Stuck[0] != "a" || !strings.HasPrefix(decision.Reason, "recovery-blocked:") {
		t.Fatalf("blocked work must remain visible: %+v", decision)
	}
	if observation.RecoveryBlockers[0] != "provider-leases-active" {
		t.Fatal("evaluation mutated caller-owned observation")
	}
	observation.RecoveryBlockers = nil
	if !Evaluate(policy, observation).Recover {
		t.Fatal("eligible unchanged intent did not recover after blocker cleared")
	}
}

type evidenceStore struct {
	results []Result
	begins  int
}

func (s *evidenceStore) Active(context.Context) ([]Attempt, error)    { return nil, nil }
func (s *evidenceStore) Begin(context.Context, Attempt) (bool, error) { s.begins++; return true, nil }
func (s *evidenceStore) Finish(_ context.Context, r Result) error {
	s.results = append(s.results, r)
	return nil
}
func (s *evidenceStore) History(context.Context) ([]Result, error) {
	return append([]Result(nil), s.results...), nil
}

type evidenceExecutor struct {
	progressed, remaining []string
	err                   error
	restarts              int
}

func (e *evidenceExecutor) Checkpoint(context.Context, Attempt) (string, error) {
	return "checkpoint", nil
}
func (e *evidenceExecutor) RestartDispatcher(context.Context, Attempt) error {
	e.restarts++
	return nil
}
func (e *evidenceExecutor) AwaitProgress(context.Context, Attempt) ([]string, []string, error) {
	return e.progressed, e.remaining, e.err
}

func TestRecoveryCannotSucceedWithEmptyExecutorProof(t *testing.T) {
	store, executor := &evidenceStore{}, &evidenceExecutor{}
	r, err := Recover(context.Background(), time.Now(), Decision{Recover: true, Stuck: []string{"a"}}, store, executor, time.Now)
	if err == nil || r.Recovered || len(r.Remaining) != 1 || len(store.results) != 1 {
		t.Fatalf("malformed progress accepted: result=%+v err=%v", r, err)
	}
}

func TestResumeDoesNotRestartOnUnknownProgress(t *testing.T) {
	for _, proofErr := range []error{nil, errors.New("observer unavailable")} {
		store, executor := &evidenceStore{}, &evidenceExecutor{err: proofErr}
		r, err := resumeAcquired(context.Background(), Attempt{ID: "attempt", Stuck: []string{"a"}}, store, executor, time.Now)
		if err == nil || r.Recovered || executor.restarts != 0 || len(store.results) != 1 {
			t.Fatalf("unknown progress caused destructive retry: result=%+v err=%v restarts=%d", r, err, executor.restarts)
		}
	}
}

func TestRecoveryRejectsAmbiguousAttemptBeforeStoreMutation(t *testing.T) {
	store := &evidenceStore{}
	_, err := Recover(context.Background(), time.Now(), Decision{Recover: true, Stuck: []string{"a", "a"}}, store, &evidenceExecutor{}, time.Now)
	if err == nil || store.begins != 0 {
		t.Fatalf("invalid attempt was acquired: %v", err)
	}
}

func outputCommand(t *testing.T, body string) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "output.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ncat <<'PROOF'\n"+body+"\nPROOF\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}

func TestCommandProgressRejectsTrailingJSONAndEmptyObjects(t *testing.T) {
	for _, body := range []string{"{}", `{"progressed":["a"],"remaining":[]} {}`, `{"progressed":["b"],"remaining":[]}`} {
		argv := outputCommand(t, body)
		executor := CommandExecutor{Config: CommandConfig{Checkpoint: argv, Restart: argv, Progress: argv, Timeout: time.Second}}
		if _, _, err := executor.AwaitProgress(context.Background(), Attempt{Stuck: []string{"a"}}); err == nil {
			t.Fatalf("accepted invalid proof %s", body)
		}
	}
}

func TestCommandObservationPreservesRecoveryBlockers(t *testing.T) {
	body := `{"observed_at":"2026-09-07T00:00:00Z","active_intents":1,"manager_uptime_seconds":600,"stale_assigned_intents":[{"id":"a","age_nanoseconds":600000000000}],"recovery_blockers":["provider-journal-unknown"]}`
	observer := CommandObserver{Argv: outputCommand(t, body), Timeout: time.Second}
	observation, err := observer.Observe(context.Background())
	if err != nil || len(observation.RecoveryBlockers) != 1 {
		t.Fatalf("lost blocker: %+v %v", observation, err)
	}
	observer.Argv = outputCommand(t, body+` {}`)
	if _, err := observer.Observe(context.Background()); err == nil {
		t.Fatal("accepted concatenated JSON")
	}
}

func TestResumeRequiresFreshDecisionForRemainingWork(t *testing.T) {
	store, executor := &evidenceStore{}, &evidenceExecutor{remaining: []string{"a"}}
	r, err := resumeAcquired(context.Background(), Attempt{ID: "attempt", Stuck: []string{"a"}}, store, executor, time.Now)
	if err == nil || r.Recovered || executor.restarts != 0 || len(store.results) != 1 {
		t.Fatalf("stored attempt bypassed fresh eligibility: result=%+v err=%v", r, err)
	}
}

type proofObserver struct{ observation Observation }

func (o proofObserver) Observe(context.Context) (Observation, error) { return o.observation, nil }

type proofHeartbeat struct{}

func (proofHeartbeat) ReadHeartbeat(context.Context) (Heartbeat, error) { return Heartbeat{}, nil }

type proofEvents struct{ events []Event }

func (e *proofEvents) Emit(_ context.Context, event Event) error {
	e.events = append(e.events, event)
	return nil
}

func TestBlockedControllerIsUnhealthyWithoutRestart(t *testing.T) {
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	store, executor, events := &evidenceStore{}, &evidenceExecutor{}, &proofEvents{}
	controller := Controller{Policy: Policy{MinimumStuckAge: time.Minute},
		Observer: proofObserver{Observation{ObservedAt: at, ActiveIntents: 1, ManagerUptime: time.Hour,
			StaleAssigned: []AssignedIntent{{ID: "a", Age: time.Hour}}, RecoveryBlockers: []string{"provider-leases-active"}}},
		Heartbeat: proofHeartbeat{}, Attempts: store, Executor: executor, Events: events, Now: func() time.Time { return at }}
	decision, _, err := controller.Tick(context.Background())
	if err != nil || decision.Recover || store.begins != 0 || executor.restarts != 0 || len(events.events) != 1 || events.events[0].State != "unhealthy" {
		t.Fatalf("blocked work was hidden or restarted: decision=%+v events=%+v err=%v", decision, events.events, err)
	}
}
