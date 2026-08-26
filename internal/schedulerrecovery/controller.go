package schedulerrecovery

import (
	"context"
	"fmt"
	"time"
)

type Observer interface {
	Observe(context.Context) (Observation, error)
}

type HeartbeatReader interface {
	ReadHeartbeat(context.Context) (Heartbeat, error)
}

type Event struct {
	At        time.Time `json:"at"`
	State     string    `json:"state"`
	Reason    string    `json:"reason"`
	AttemptID string    `json:"attempt_id,omitempty"`
	Stuck     []string  `json:"stuck,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type EventSink interface {
	Emit(context.Context, Event) error
}

type Controller struct {
	Policy    Policy
	Observer  Observer
	Heartbeat HeartbeatReader
	Attempts  AttemptStore
	Executor  Executor
	Events    EventSink
	Now       func() time.Time
}

func (controller Controller) Tick(ctx context.Context) (Decision, Result, error) {
	if controller.Observer == nil || controller.Heartbeat == nil || controller.Attempts == nil || controller.Executor == nil || controller.Events == nil || controller.Now == nil {
		return Decision{}, Result{}, fmt.Errorf("scheduler recovery controller is incomplete")
	}
	active, err := controller.Attempts.Active(ctx)
	if err != nil {
		return Decision{}, Result{}, fmt.Errorf("read active recovery attempt: %w", err)
	}
	if len(active) > 1 {
		return Decision{}, Result{}, fmt.Errorf("multiple active recovery attempts require deterministic repair")
	}
	if len(active) == 1 {
		attempt := active[0]
		if err := controller.emit(ctx, Event{At: controller.Now().UTC(), State: "recovering", Reason: "resume-interrupted-recovery", AttemptID: attempt.ID, Stuck: attempt.Stuck}); err != nil {
			return Decision{}, Result{}, err
		}
		result, recoveryErr := resumeAcquired(ctx, attempt, controller.Attempts, controller.Executor, controller.Now)
		terminal := Event{At: controller.Now().UTC(), State: "recovered", Reason: "resume-interrupted-recovery", AttemptID: attempt.ID, Stuck: result.Remaining}
		if recoveryErr != nil {
			terminal.State = "failed"
			terminal.Error = recoveryErr.Error()
		}
		if err := controller.emit(ctx, terminal); err != nil {
			return Decision{}, result, err
		}
		return Decision{Reason: "resume-interrupted-recovery", Stuck: attempt.Stuck}, result, recoveryErr
	}
	observation, err := controller.Observer.Observe(ctx)
	if err != nil {
		return Decision{}, Result{}, fmt.Errorf("observe scheduler: %w", err)
	}
	heartbeat, err := controller.Heartbeat.ReadHeartbeat(ctx)
	if err != nil {
		return Decision{}, Result{}, fmt.Errorf("read dispatcher heartbeat: %w", err)
	}
	observation.HeartbeatAt = heartbeat.At
	decision := Evaluate(controller.Policy, observation)
	state := "healthy"
	if decision.Recover {
		state = "unhealthy"
	}
	if err := controller.emit(ctx, Event{At: observation.ObservedAt, State: state, Reason: decision.Reason, Stuck: decision.Stuck}); err != nil {
		return decision, Result{}, err
	}
	if !decision.Recover {
		return decision, Result{}, nil
	}
	attempt := NewAttempt(observation.ObservedAt, decision.Stuck)
	acquired, err := controller.Attempts.Begin(ctx, attempt)
	if err != nil {
		return decision, Result{}, fmt.Errorf("begin recovery attempt: %w", err)
	}
	if !acquired {
		result := Result{AttemptID: attempt.ID, Suppressed: true}
		if err := controller.emit(ctx, Event{At: controller.Now().UTC(), State: "healthy", Reason: "duplicate-recovery-suppressed", AttemptID: attempt.ID, Stuck: attempt.Stuck}); err != nil {
			return decision, result, err
		}
		return decision, result, nil
	}
	if err := controller.emit(ctx, Event{At: controller.Now().UTC(), State: "recovering", Reason: decision.Reason, AttemptID: attempt.ID, Stuck: attempt.Stuck}); err != nil {
		return decision, Result{}, err
	}
	result, recoveryErr := recoverAcquired(ctx, attempt, controller.Attempts, controller.Executor, controller.Now)
	terminal := Event{At: controller.Now().UTC(), State: "recovered", Reason: decision.Reason, AttemptID: attempt.ID, Stuck: result.Remaining}
	if recoveryErr != nil {
		terminal.State = "failed"
		terminal.Error = recoveryErr.Error()
	}
	if err := controller.emit(ctx, terminal); err != nil {
		return decision, result, err
	}
	return decision, result, recoveryErr
}

func (controller Controller) emit(ctx context.Context, event Event) error {
	if err := controller.Events.Emit(ctx, event); err != nil {
		return fmt.Errorf("emit scheduler recovery event: %w", err)
	}
	return nil
}
