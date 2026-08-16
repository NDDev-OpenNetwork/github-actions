package lifecycle

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/domain"
)

const testImageDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestWorkerHappyPathAlwaysEndsDestroyed(t *testing.T) {
	t.Parallel()

	jobKey := mustJobKey(t, 1)
	worker, err := NewWorker("vm-01", testImageDigest)
	if err != nil {
		t.Fatalf("new worker: %v", err)
	}

	steps := []struct {
		event Event
		key   domain.JobKey
		state State
	}{
		{EventWarmReady, "", StateAvailableWarm},
		{EventAdmit, jobKey, StateAdmitted},
		{EventBeginRegistration, jobKey, StateRegistering},
		{EventRunnerOnline, jobKey, StateOnline},
		{EventAssigned, jobKey, StateAssigned},
		{EventJobStarted, jobKey, StateRunning},
		{EventJobSucceeded, jobKey, StateCollecting},
		{EventDiagnosticsCollected, jobKey, StateDestroying},
		{EventDestroyed, jobKey, StateDestroyed},
	}

	for _, step := range steps {
		worker, err = worker.Transition(step.event, step.key)
		if err != nil {
			t.Fatalf("transition %s: %v", step.event, err)
		}
		if worker.State != step.state {
			t.Fatalf("transition %s reached %s, want %s", step.event, worker.State, step.state)
		}
	}
	if !worker.HasRun || worker.Outcome != OutcomeSucceeded {
		t.Fatalf("unexpected terminal worker: %#v", worker)
	}
}

func TestWorkerRejectsDifferentJobAfterAdmission(t *testing.T) {
	t.Parallel()

	first := mustJobKey(t, 1)
	second := mustJobKey(t, 2)
	worker, _ := NewWorker("vm-02", testImageDigest)
	worker, _ = worker.Transition(EventWarmReady, "")
	worker, _ = worker.Transition(EventAdmit, first)

	if _, err := worker.Transition(EventBeginRegistration, second); err == nil {
		t.Fatal("worker accepted an event for a different job attempt")
	}
}

func TestWorkerRequiresJobKeyThroughTeardown(t *testing.T) {
	t.Parallel()

	jobKey := mustJobKey(t, 1)
	worker, _ := NewWorker("vm-05", testImageDigest)
	worker, _ = worker.Transition(EventWarmReady, "")
	worker, _ = worker.Transition(EventAdmit, jobKey)

	if _, err := worker.Transition(EventBeginRegistration, ""); err == nil {
		t.Fatal("bound worker accepted a transition without its job key")
	}
}

func TestWorkerCannotBeReusedAfterExecution(t *testing.T) {
	t.Parallel()

	jobKey := mustJobKey(t, 1)
	worker, _ := NewWorker("vm-03", testImageDigest)
	for _, event := range []Event{
		EventWarmReady,
		EventAdmit,
		EventBeginRegistration,
		EventRunnerOnline,
		EventAssigned,
		EventJobStarted,
		EventJobSucceeded,
		EventDiagnosticsCollected,
		EventDestroyed,
	} {
		key := jobKey
		if event == EventWarmReady {
			key = ""
		}
		var err error
		worker, err = worker.Transition(event, key)
		if err != nil {
			t.Fatalf("transition %s: %v", event, err)
		}
	}

	if _, err := worker.Transition(EventWarmReady, ""); err == nil {
		t.Fatal("destroyed worker returned to the warm pool")
	}
}

func TestDestroyFailureIsQuarantinedAndRetryable(t *testing.T) {
	t.Parallel()

	worker, _ := NewWorker("vm-04", testImageDigest)
	worker, _ = worker.Transition(EventProvisionFailed, "")
	worker, err := worker.Transition(EventDestroyFailed, "")
	if err != nil {
		t.Fatalf("quarantine: %v", err)
	}
	if worker.State != StateQuarantined {
		t.Fatalf("state = %s, want %s", worker.State, StateQuarantined)
	}
	worker, err = worker.Transition(EventRetryDestroy, "")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if worker.State != StateDestroying {
		t.Fatalf("state = %s, want %s", worker.State, StateDestroying)
	}
}

func mustJobKey(t *testing.T, attempt int64) domain.JobKey {
	t.Helper()
	key, err := (domain.WebhookJobIdentity{
		RepositoryID:  42,
		WorkflowJobID: 9001,
		RunAttempt:    attempt,
	}).Key()
	if err != nil {
		t.Fatalf("job key: %v", err)
	}
	return key
}
