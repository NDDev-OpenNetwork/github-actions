package vanishedjob

import (
	"strings"
	"testing"
	"time"
)

func testPolicy(t *testing.T) Policy {
	t.Helper()
	policy, err := DecodePolicy(strings.NewReader(`{
  "schema_version":1,
  "missing_runner_grace_seconds":120,
  "scale_sets":{"example-ci":"force-cancel-full-rerun","example-release":"observe"}
}`))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func testJob(now time.Time) Job {
	return Job{
		Repository: "example-org/example-repo", ScaleSet: "example-ci", RunID: 42, JobID: 84,
		RunnerID: 21, RunnerName: "example-runner", JobStatus: "in_progress",
		StartedAt: now.Add(-3 * time.Minute), RunStatus: "in_progress", RunAttempt: 1,
	}
}

func TestEvaluateFullRecoveryLifecycleAndCrashReconstruction(t *testing.T) {
	now := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	policy, job := testPolicy(t), testJob(now)
	detected, err := Evaluate(policy, job, nil, now)
	if err != nil || detected.Action != ActionForceCancel || detected.Record.Stage != StageDetected {
		t.Fatalf("detection = %#v, %v", detected, err)
	}
	cancelled := detected.Record
	cancelled.Stage = StageCancelRequested
	waiting, err := Evaluate(policy, job, &cancelled, now.Add(time.Minute))
	if err != nil || waiting.Action != ActionAwaitCancel {
		t.Fatalf("cancel wait = %#v, %v", waiting, err)
	}
	job.RunStatus, job.RunConclusion = "completed", "cancelled"
	rerun, err := Evaluate(policy, job, &cancelled, now.Add(2*time.Minute))
	if err != nil || rerun.Action != ActionFullRerun {
		t.Fatalf("rerun = %#v, %v", rerun, err)
	}
	// Simulate a crash after GitHub accepted the rerun but before Stage was
	// persisted. The incremented attempt is authoritative and suppresses replay.
	job.RunAttempt, job.RunStatus = 2, "in_progress"
	reconstructed, err := Evaluate(policy, job, &cancelled, now.Add(3*time.Minute))
	if err != nil || reconstructed.Action != ActionAwaitRerun {
		t.Fatalf("reconstructed = %#v, %v", reconstructed, err)
	}
	job.RunStatus, job.RunConclusion = "completed", "success"
	complete, err := Evaluate(policy, job, &cancelled, now.Add(4*time.Minute))
	if err != nil || complete.Action != ActionComplete {
		t.Fatalf("complete = %#v, %v", complete, err)
	}
}

func TestEvaluateSeparatesReleaseAndTransientAbsence(t *testing.T) {
	now := time.Date(2026, 8, 26, 14, 0, 0, 0, time.UTC)
	policy, job := testPolicy(t), testJob(now)
	job.ScaleSet = "example-release"
	decision, err := Evaluate(policy, job, nil, now)
	if err != nil || decision.Action != ActionIncident {
		t.Fatalf("release = %#v, %v", decision, err)
	}
	job.ScaleSet, job.RunnerPresent = "example-ci", true
	decision, err = Evaluate(policy, job, nil, now)
	if err != nil || decision.Action != ActionNone {
		t.Fatalf("present runner = %#v, %v", decision, err)
	}
}
