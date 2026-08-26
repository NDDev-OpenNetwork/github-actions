package vanishedjob

import (
	"context"
	"fmt"
	"time"
)

type RunClient interface {
	ForceCancel(context.Context, string, int64) error
	FullRerun(context.Context, string, int64) error
}

type Event struct {
	At         time.Time `json:"at"`
	Action     Action    `json:"action"`
	Reason     string    `json:"reason"`
	Repository string    `json:"repository,omitempty"`
	RunID      int64     `json:"run_id,omitempty"`
	JobID      int64     `json:"job_id,omitempty"`
	RunnerID   int64     `json:"runner_id,omitempty"`
	ScaleSet   string    `json:"scale_set,omitempty"`
	Error      string    `json:"error,omitempty"`
}

type EventSink interface {
	Emit(context.Context, Event) error
}

type Controller struct {
	Policy Policy
	Store  FileStore
	Client RunClient
	Events EventSink
	Now    func() time.Time
}

func (controller Controller) Reconcile(ctx context.Context, job Job) (Decision, error) {
	if controller.Client == nil || controller.Events == nil || controller.Now == nil {
		return Decision{}, fmt.Errorf("vanished-runner recovery controller is incomplete")
	}
	key := RecordKey(job.Repository, job.RunID, job.RunAttempt)
	existing, err := controller.Store.Get(key)
	if err != nil {
		return Decision{}, fmt.Errorf("read vanished-runner recovery: %w", err)
	}
	// A replacement attempt is indexed by the original record, not by its new
	// attempt number. Find that exact active transaction when the direct key is
	// absent; there can be only one record for a repository/run at a time.
	if existing == nil {
		existing, key, err = controller.Store.ForRun(job.Repository, job.RunID)
		if err != nil {
			return Decision{}, err
		}
	}
	decision, err := Evaluate(controller.Policy, job, existing, controller.Now().UTC())
	if err != nil {
		return decision, controller.emit(ctx, job, decision, err)
	}
	if existing == nil && decision.Action == ActionForceCancel {
		created, beginErr := controller.Store.Begin(decision.Record)
		if beginErr != nil {
			return decision, controller.emit(ctx, job, decision, beginErr)
		}
		if !created {
			return decision, nil
		}
		key = RecordKey(decision.Record.Repository, decision.Record.RunID, decision.Record.OriginalAttempt)
		existing = &decision.Record
	}
	switch decision.Action {
	case ActionForceCancel:
		if err := controller.Client.ForceCancel(ctx, job.Repository, job.RunID); err != nil {
			return decision, controller.emit(ctx, job, decision, err)
		}
		if existing.Stage == StageDetected {
			if err := controller.Store.Advance(key, StageDetected, StageCancelRequested, controller.Now()); err != nil {
				return decision, controller.emit(ctx, job, decision, err)
			}
		}
	case ActionFullRerun:
		if err := controller.Client.FullRerun(ctx, job.Repository, job.RunID); err != nil {
			return decision, controller.emit(ctx, job, decision, err)
		}
		if err := controller.Store.Advance(key, existing.Stage, StageRerunRequested, controller.Now()); err != nil {
			return decision, controller.emit(ctx, job, decision, err)
		}
	case ActionComplete:
		result := Result{
			Key: key, OriginalAttempt: existing.OriginalAttempt, ReplacementAttempt: job.RunAttempt,
			Conclusion: job.RunConclusion, FinishedAt: controller.Now().UTC(),
		}
		if err := controller.Store.Finish(key, existing.Stage, result); err != nil {
			return decision, controller.emit(ctx, job, decision, err)
		}
	}
	return decision, controller.emit(ctx, job, decision, nil)
}

func (controller Controller) emit(ctx context.Context, job Job, decision Decision, operationErr error) error {
	event := Event{
		At: controller.Now().UTC(), Action: decision.Action, Reason: decision.Reason,
		Repository: job.Repository, RunID: job.RunID, JobID: job.JobID,
		RunnerID: job.RunnerID, ScaleSet: job.ScaleSet,
	}
	if operationErr != nil {
		event.Error = operationErr.Error()
	}
	if err := controller.Events.Emit(ctx, event); err != nil {
		return fmt.Errorf("emit vanished-runner recovery event: %w", err)
	}
	return operationErr
}
