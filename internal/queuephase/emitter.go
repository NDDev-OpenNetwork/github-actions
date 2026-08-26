package queuephase

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"strconv"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Phase struct {
	Intent queueintent.Intent
	Start  time.Time
	End    time.Time
}

type EmitFunc func(context.Context, Phase)

// Emitter turns durable state transitions into one bounded duration span per
// phase. It never invents traffic: a span exists only after the queue journal
// proves either a state transition or disappearance of a previously observed
// active intent.
type Emitter struct {
	active map[string]queueintent.Intent
	emit   EmitFunc
}

func New() *Emitter {
	return &Emitter{active: map[string]queueintent.Intent{}, emit: emitSpan}
}

func NewWithEmitter(emit EmitFunc) *Emitter {
	return &Emitter{active: map[string]queueintent.Intent{}, emit: emit}
}

func (e *Emitter) Observe(ctx context.Context, snapshot queueintent.Snapshot, now time.Time) {
	if e == nil || e.emit == nil || now.IsZero() {
		return
	}
	next := make(map[string]queueintent.Intent, len(snapshot.Active))
	for _, current := range snapshot.Active {
		next[current.Key] = current
		previous, exists := e.active[current.Key]
		if !exists || previous.State == current.State {
			continue
		}
		end := current.StateEnteredAt
		if end.Before(previous.StateEnteredAt) {
			end = now
		}
		// Newer lifecycle messages carry more complete correlation fields, so
		// emit the completed previous phase with the current enriched identity.
		completed := current
		completed.State = previous.State
		e.emit(ctx, Phase{Intent: completed, Start: previous.StateEnteredAt, End: end})
	}
	for key, previous := range e.active {
		if _, exists := next[key]; exists {
			continue
		}
		e.emit(ctx, Phase{Intent: previous, Start: previous.StateEnteredAt, End: now})
	}
	e.active = next
}

func emitSpan(ctx context.Context, phase Phase) {
	if phase.Start.IsZero() || phase.End.Before(phase.Start) {
		return
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID(phase.Intent.JobID),
		SpanID:     parentSpanID(phase.Intent.JobID),
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx = trace.ContextWithRemoteSpanContext(ctx, parent)
	_, span := otel.Tracer("nddev.drakkars.queue").Start(
		ctx, "queue."+string(phase.Intent.State),
		trace.WithTimestamp(phase.Start),
		trace.WithAttributes(
			attribute.String("queue.job_uuid", phase.Intent.JobID),
			attribute.String("queue.state", string(phase.Intent.State)),
			attribute.String("queue.repository", phase.Intent.Repository),
			attribute.String("queue.scale_set", phase.Intent.ScaleSetName),
			attribute.Int("queue.priority", phase.Intent.Priority),
			attribute.Int64("github.workflow_run_id", phase.Intent.WorkflowRunID),
			attribute.String("github.job_name", phase.Intent.JobDisplayName),
			attribute.Int64("github.runner_request_id", phase.Intent.RunnerRequestID),
			attribute.Int64("github.runner_id", phase.Intent.GitHubRunnerID),
			attribute.String("runner.name", phase.Intent.RunnerName),
		),
	)
	span.SetStatus(codes.Ok, "")
	span.End(trace.WithTimestamp(phase.End))
}

func traceID(jobID string) trace.TraceID {
	digest := sha256.Sum256([]byte("nddev-drakkars-job:" + jobID))
	var result trace.TraceID
	copy(result[:], digest[:16])
	return result
}

func parentSpanID(jobID string) trace.SpanID {
	digest := sha256.Sum256([]byte("nddev-drakkars-job-parent:" + jobID))
	var result trace.SpanID
	copy(result[:], digest[:8])
	if binary.BigEndian.Uint64(result[:]) == 0 {
		binary.BigEndian.PutUint64(result[:], uint64(len(jobID))+1)
	}
	return result
}

func CorrelationSummary(intent queueintent.Intent) string {
	return intent.JobID + "/" + strconv.FormatInt(intent.WorkflowRunID, 10) + "/" + strconv.FormatInt(intent.GitHubRunnerID, 10)
}
