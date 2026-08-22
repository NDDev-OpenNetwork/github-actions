package queuephase

import (
	"context"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

func TestEmitterEmitsOnlyProvenTransitionsAndUsesEnrichedIdentity(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	var phases []Phase
	emitter := NewWithEmitter(func(_ context.Context, phase Phase) { phases = append(phases, phase) })
	assigned := queueintent.Intent{Key: "key", JobID: "job", State: queueintent.StateAssigned, StateEnteredAt: now}
	emitter.Observe(context.Background(), queueintent.Snapshot{Active: []queueintent.Intent{assigned}}, now)
	if len(phases) != 0 {
		t.Fatalf("initial observation invented phases: %#v", phases)
	}
	running := assigned
	running.State = queueintent.StateRunning
	running.StateEnteredAt = now.Add(time.Minute)
	running.WorkflowRunID = 42
	running.GitHubRunnerID = 84
	running.RunnerName = "runner"
	emitter.Observe(context.Background(), queueintent.Snapshot{Active: []queueintent.Intent{running}}, now.Add(time.Minute))
	if len(phases) != 1 || phases[0].Intent.State != queueintent.StateAssigned || phases[0].Intent.WorkflowRunID != 42 || phases[0].End != running.StateEnteredAt {
		t.Fatalf("transition phase = %#v", phases)
	}
	emitter.Observe(context.Background(), queueintent.Snapshot{}, now.Add(2*time.Minute))
	if len(phases) != 2 || phases[1].Intent.State != queueintent.StateRunning || phases[1].Start != running.StateEnteredAt {
		t.Fatalf("terminal phase = %#v", phases)
	}
}

func TestTraceIdentityIsStablePerJob(t *testing.T) {
	if traceID("one") != traceID("one") || traceID("one") == traceID("two") || !traceID("one").IsValid() || !parentSpanID("one").IsValid() {
		t.Fatal("job trace identity is unstable or invalid")
	}
}
