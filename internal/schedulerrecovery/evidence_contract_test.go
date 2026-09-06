package schedulerrecovery

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestProgressIsAnExactPartition(t *testing.T) {
	for _, test := range []struct {
		name                            string
		expected, progressed, remaining []string
		valid                           bool
	}{
		{"all-started", []string{"a", "b"}, []string{"b", "a"}, nil, true},
		{"partial", []string{"a", "b"}, []string{"a"}, []string{"b"}, true},
		{"unresolved", []string{"a"}, nil, []string{"a"}, true},
		{"empty-receipt", []string{"a"}, nil, nil, false},
		{"omitted", []string{"a", "b"}, []string{"a"}, nil, false},
		{"foreign", []string{"a"}, []string{"b"}, nil, false},
		{"overlap", []string{"a"}, []string{"a"}, []string{"a"}, false},
		{"duplicate-progress", []string{"a"}, []string{"a", "a"}, nil, false},
		{"duplicate-remaining", []string{"a"}, nil, []string{"a", "a"}, false},
		{"duplicate-attempt", []string{"a", "a"}, nil, []string{"a"}, false},
		{"empty-attempt", nil, nil, nil, false},
		{"delimiter-injection", []string{"a,b"}, nil, []string{"a,b"}, false},
		{"empty-id", []string{""}, nil, []string{""}, false},
		{"whitespace-id", []string{" a"}, nil, []string{" a"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateProgress(test.expected, test.progressed, test.remaining)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v, error=%v", test.valid, err)
			}
		})
	}
}

type receiptStore struct {
	begun   int
	results []Result
}

func (s *receiptStore) Active(context.Context) ([]Attempt, error)    { return nil, nil }
func (s *receiptStore) Begin(context.Context, Attempt) (bool, error) { s.begun++; return true, nil }
func (s *receiptStore) Finish(_ context.Context, result Result) error {
	s.results = append(s.results, result)
	return nil
}

type receiptExecutor struct {
	checkpoints, restarts, reads int
	progressed, remaining        []string
	err                          error
	restarted                    []string
	advanceRemaining             bool
}

func (e *receiptExecutor) Checkpoint(context.Context, Attempt) (string, error) {
	e.checkpoints++
	return "checkpoint", nil
}
func (e *receiptExecutor) RestartDispatcher(_ context.Context, a Attempt) error {
	e.restarts++
	e.restarted = a.Stuck
	return nil
}
func (e *receiptExecutor) AwaitProgress(_ context.Context, a Attempt) ([]string, []string, error) {
	e.reads++
	if e.advanceRemaining && e.reads > 1 {
		return a.Stuck, nil, nil
	}
	return e.progressed, e.remaining, e.err
}

func TestRecoveryRejectsEmptySuccessAndKeepsIdentities(t *testing.T) {
	s := &receiptStore{}
	e := &receiptExecutor{}
	at := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	r, err := Recover(context.Background(), at, Decision{Recover: true, Stuck: []string{"a"}}, s, e, func() time.Time { return at })
	if err == nil || r.Recovered || !reflect.DeepEqual(r.Remaining, []string{"a"}) || len(s.results) != 1 {
		t.Fatalf("invalid receipt accepted: result=%+v err=%v", r, err)
	}
}

func TestRecoveryRejectsUnboundAttemptBeforeMutation(t *testing.T) {
	s := &receiptStore{}
	e := &receiptExecutor{}
	_, err := Recover(context.Background(), time.Now(), Decision{Recover: true}, s, e, time.Now)
	if err == nil || s.begun != 0 || e.checkpoints != 0 || e.restarts != 0 {
		t.Fatal("unbound attempt mutated state")
	}
}

func TestResumeUnknownProgressNeverRestarts(t *testing.T) {
	for _, observationErr := range []error{nil, errors.New("observer unavailable")} {
		s := &receiptStore{}
		e := &receiptExecutor{err: observationErr}
		r, err := resumeAcquired(context.Background(), Attempt{ID: "old", Stuck: []string{"a"}}, s, e, time.Now)
		if err == nil || r.Recovered || e.restarts != 0 || e.checkpoints != 0 || !reflect.DeepEqual(r.Remaining, []string{"a"}) {
			t.Fatalf("unknown resume restarted or lost identity: %+v %v", r, err)
		}
	}
}

func TestResumeRetainsPriorVerifiedProgress(t *testing.T) {
	s := &receiptStore{}
	e := &receiptExecutor{progressed: []string{"a"}, remaining: []string{"b"}, advanceRemaining: true}
	r, err := resumeAcquired(context.Background(), Attempt{ID: "old", Stuck: []string{"a", "b"}}, s, e, time.Now)
	if err != nil || !r.Recovered || !reflect.DeepEqual(r.Progressed, []string{"a", "b"}) || !reflect.DeepEqual(e.restarted, []string{"b"}) || len(s.results) != 1 {
		t.Fatalf("resume receipt or scope incorrect: %+v err=%v executor=%+v", r, err, e)
	}
}

func TestRestartBlockersPreserveStalledDemand(t *testing.T) {
	p := Policy{MinimumStuckAge: time.Minute, MinimumUptime: time.Minute}
	o := Observation{ObservedAt: time.Now(), ActiveIntents: 1, ManagerUptime: time.Hour,
		StaleAssigned: []AssignedIntent{{ID: "a", Age: time.Hour}}, RestartBlockers: []string{"provider-journal-unknown"}}
	d := Evaluate(p, o)
	if d.Recover || !reflect.DeepEqual(d.Stuck, []string{"a"}) || !strings.Contains(d.Reason, "provider-journal-unknown") {
		t.Fatalf("stall hidden or restart permitted: %+v", d)
	}
	o.RestartBlockers = nil
	if !Evaluate(p, o).Recover {
		t.Fatal("removing blocker did not restore ordinary eligibility")
	}
}

func TestCommandProgressRejectsMalformedOrIncompleteReceipts(t *testing.T) {
	for _, payload := range []string{`{}`, `null`, `{"progressed":[],"remaining":[]}`, `{"progressed":["foreign"]}`, `{"progressed":["a"]} {}`} {
		e := CommandExecutor{Config: CommandConfig{Checkpoint: []string{"/bin/true"}, Restart: []string{"/bin/true"},
			Progress: []string{"/bin/sh", "-c", "printf '%s' '" + payload + "'"}, Timeout: time.Second}}
		if _, _, err := e.AwaitProgress(context.Background(), Attempt{Stuck: []string{"a"}}); err == nil {
			t.Fatalf("accepted %s", payload)
		}
	}
}

func TestCommandObserverCarriesRestartBlockers(t *testing.T) {
	payload := `{"observed_at":"2026-09-07T00:00:00Z","active_intents":1,"manager_uptime_seconds":1000,"stale_assigned_intents":[{"id":"a","age_nanoseconds":1000000000000}],"restart_blockers":["provider-journal-unknown"]}`
	o := CommandObserver{Argv: []string{"/bin/sh", "-c", "printf '%s' '" + payload + "'"}, Timeout: time.Second}
	value, err := o.Observe(context.Background())
	if err != nil || !reflect.DeepEqual(value.RestartBlockers, []string{"provider-journal-unknown"}) {
		t.Fatalf("blocker lost: %+v %v", value, err)
	}
	o.Argv[2] += "; printf '{}'"
	if _, err := o.Observe(context.Background()); err == nil {
		t.Fatal("accepted concatenated observations")
	}
}
