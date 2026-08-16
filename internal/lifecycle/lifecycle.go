package lifecycle

import (
	"fmt"
	"regexp"

	"github.com/NDDev-OpenNetwork/github-actions/internal/domain"
)

type State string

const (
	StateProvisioning  State = "provisioning"
	StateAvailableWarm State = "available-warm"
	StateAdmitted      State = "admitted"
	StateRegistering   State = "registering"
	StateOnline        State = "online"
	StateAssigned      State = "assigned"
	StateRunning       State = "running"
	StateCollecting    State = "collecting"
	StateDestroying    State = "destroying"
	StateDestroyed     State = "destroyed"
	StateQuarantined   State = "quarantined"
)

type Event string

const (
	EventWarmReady            Event = "warm-ready"
	EventAdmit                Event = "admit"
	EventBeginRegistration    Event = "begin-registration"
	EventRunnerOnline         Event = "runner-online"
	EventAssigned             Event = "assigned"
	EventJobStarted           Event = "job-started"
	EventJobSucceeded         Event = "job-succeeded"
	EventJobFailed            Event = "job-failed"
	EventJobCancelled         Event = "job-cancelled"
	EventProvisionFailed      Event = "provision-failed"
	EventRegistrationFailed   Event = "registration-failed"
	EventAssignmentTimedOut   Event = "assignment-timed-out"
	EventDrain                Event = "drain"
	EventDiagnosticsCollected Event = "diagnostics-collected"
	EventDiagnosticsFailed    Event = "diagnostics-failed"
	EventDestroyed            Event = "destroyed"
	EventDestroyFailed        Event = "destroy-failed"
	EventRetryDestroy         Event = "retry-destroy"
)

type Outcome string

const (
	OutcomeNone                  Outcome = ""
	OutcomeSucceeded             Outcome = "succeeded"
	OutcomeFailed                Outcome = "failed"
	OutcomeCancelled             Outcome = "cancelled"
	OutcomeInfrastructureFailure Outcome = "infrastructure-failure"
)

type Worker struct {
	ID          string        `json:"id"`
	ImageDigest string        `json:"image_digest"`
	State       State         `json:"state"`
	JobKey      domain.JobKey `json:"job_key,omitempty"`
	HasRun      bool          `json:"has_run"`
	Outcome     Outcome       `json:"outcome,omitempty"`
}

type transitionKey struct {
	state State
	event Event
}

var (
	imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	transitions        = map[transitionKey]State{
		{StateProvisioning, EventWarmReady}:          StateAvailableWarm,
		{StateProvisioning, EventProvisionFailed}:    StateDestroying,
		{StateAvailableWarm, EventAdmit}:             StateAdmitted,
		{StateAvailableWarm, EventDrain}:             StateDestroying,
		{StateAdmitted, EventBeginRegistration}:      StateRegistering,
		{StateAdmitted, EventJobCancelled}:           StateCollecting,
		{StateRegistering, EventRunnerOnline}:        StateOnline,
		{StateRegistering, EventRegistrationFailed}:  StateCollecting,
		{StateRegistering, EventJobCancelled}:        StateCollecting,
		{StateOnline, EventAssigned}:                 StateAssigned,
		{StateOnline, EventAssignmentTimedOut}:       StateCollecting,
		{StateOnline, EventJobCancelled}:             StateCollecting,
		{StateAssigned, EventJobStarted}:             StateRunning,
		{StateAssigned, EventJobCancelled}:           StateCollecting,
		{StateRunning, EventJobSucceeded}:            StateCollecting,
		{StateRunning, EventJobFailed}:               StateCollecting,
		{StateRunning, EventJobCancelled}:            StateCollecting,
		{StateCollecting, EventDiagnosticsCollected}: StateDestroying,
		{StateCollecting, EventDiagnosticsFailed}:    StateDestroying,
		{StateDestroying, EventDestroyed}:            StateDestroyed,
		{StateDestroying, EventDestroyFailed}:        StateQuarantined,
		{StateQuarantined, EventRetryDestroy}:        StateDestroying,
	}
)

func NewWorker(id, imageDigest string) (Worker, error) {
	if id == "" {
		return Worker{}, fmt.Errorf("worker ID is required")
	}
	if !imageDigestPattern.MatchString(imageDigest) {
		return Worker{}, fmt.Errorf("image digest must be an immutable sha256 digest")
	}
	return Worker{ID: id, ImageDigest: imageDigest, State: StateProvisioning}, nil
}

// Transition returns a new aggregate and leaves the receiver unchanged. The
// caller persists the returned aggregate and event atomically in the journal.
func (worker Worker) Transition(event Event, jobKey domain.JobKey) (Worker, error) {
	nextState, allowed := transitions[transitionKey{state: worker.State, event: event}]
	if !allowed {
		return Worker{}, fmt.Errorf("event %q is not valid from state %q", event, worker.State)
	}

	if event == EventAdmit {
		if jobKey.IsZero() {
			return Worker{}, fmt.Errorf("admission requires a job key")
		}
		if !worker.JobKey.IsZero() || worker.HasRun {
			return Worker{}, fmt.Errorf("worker has already been bound or executed")
		}
		worker.JobKey = jobKey
	} else if !worker.JobKey.IsZero() {
		if jobKey.IsZero() || worker.JobKey != jobKey {
			return Worker{}, fmt.Errorf("event %q does not match the admitted job", event)
		}
	} else if eventNeedsJob(event) {
		return Worker{}, fmt.Errorf("event %q requires an admitted job", event)
	} else if !jobKey.IsZero() {
		return Worker{}, fmt.Errorf("event %q carries a job key before admission", event)
	}

	worker.State = nextState
	switch event {
	case EventJobStarted:
		worker.HasRun = true
	case EventJobSucceeded:
		worker.Outcome = OutcomeSucceeded
	case EventJobFailed:
		worker.Outcome = OutcomeFailed
	case EventJobCancelled:
		worker.Outcome = OutcomeCancelled
	case EventRegistrationFailed, EventAssignmentTimedOut:
		worker.Outcome = OutcomeInfrastructureFailure
	}

	if worker.State == StateAvailableWarm && (worker.HasRun || !worker.JobKey.IsZero()) {
		return Worker{}, fmt.Errorf("executed or admitted workers cannot return to the warm pool")
	}
	return worker, nil
}

func eventNeedsJob(event Event) bool {
	switch event {
	case EventAdmit,
		EventBeginRegistration,
		EventRunnerOnline,
		EventAssigned,
		EventJobStarted,
		EventJobSucceeded,
		EventJobFailed,
		EventJobCancelled,
		EventRegistrationFailed,
		EventAssignmentTimedOut:
		return true
	default:
		return false
	}
}
