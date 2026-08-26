package vanishedjob

import (
	"fmt"
	"time"
)

type Stage string

const (
	StageDetected        Stage = "detected"
	StageCancelRequested Stage = "cancel-requested"
	StageRerunRequested  Stage = "rerun-requested"
)

type Action string

const (
	ActionNone        Action = "none"
	ActionIncident    Action = "incident"
	ActionForceCancel Action = "force-cancel"
	ActionAwaitCancel Action = "await-cancel"
	ActionFullRerun   Action = "full-rerun"
	ActionAwaitRerun  Action = "await-rerun"
	ActionComplete    Action = "complete"
)

type Job struct {
	Repository    string    `json:"repository"`
	ScaleSet      string    `json:"scale_set"`
	RunID         int64     `json:"run_id"`
	JobID         int64     `json:"job_id"`
	RunnerID      int64     `json:"runner_id"`
	RunnerName    string    `json:"runner_name"`
	JobStatus     string    `json:"job_status"`
	StartedAt     time.Time `json:"started_at"`
	RunnerPresent bool      `json:"runner_present"`
	RunStatus     string    `json:"run_status"`
	RunConclusion string    `json:"run_conclusion,omitempty"`
	RunAttempt    int       `json:"run_attempt"`
}

type Record struct {
	Repository      string    `json:"repository"`
	RunID           int64     `json:"run_id"`
	JobID           int64     `json:"job_id"`
	RunnerID        int64     `json:"runner_id"`
	RunnerName      string    `json:"runner_name"`
	ScaleSet        string    `json:"scale_set"`
	OriginalAttempt int       `json:"original_attempt"`
	Stage           Stage     `json:"stage"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Decision struct {
	Action Action `json:"action"`
	Reason string `json:"reason"`
	Record Record `json:"record,omitempty"`
}

func Evaluate(policy Policy, job Job, existing *Record, now time.Time) (Decision, error) {
	if err := policy.Validate(); err != nil {
		return Decision{}, err
	}
	mode, configured := policy.ScaleSets[job.ScaleSet]
	if !configured {
		return Decision{Action: ActionNone, Reason: "scale-set-unconfigured"}, nil
	}
	if existing == nil {
		if job.Repository == "" || job.RunID <= 0 || job.JobID <= 0 || job.RunnerID <= 0 || job.RunnerName == "" || job.RunAttempt <= 0 || job.StartedAt.IsZero() {
			return Decision{}, fmt.Errorf("vanished-runner job identity is incomplete")
		}
		if job.JobStatus != "in_progress" || job.RunnerPresent || now.Sub(job.StartedAt) < policy.Grace() {
			return Decision{Action: ActionNone, Reason: "no-aged-vanished-runner"}, nil
		}
		record := Record{
			Repository: job.Repository, RunID: job.RunID, JobID: job.JobID,
			RunnerID: job.RunnerID, RunnerName: job.RunnerName, ScaleSet: job.ScaleSet,
			OriginalAttempt: job.RunAttempt, Stage: StageDetected, UpdatedAt: now.UTC(),
		}
		if mode == ModeObserve {
			return Decision{Action: ActionIncident, Reason: "side-effecting-workflow-requires-policy", Record: record}, nil
		}
		return Decision{Action: ActionForceCancel, Reason: "aged-job-runner-absent", Record: record}, nil
	}
	if existing.Repository != job.Repository || existing.RunID != job.RunID || existing.JobID != job.JobID || existing.RunnerID != job.RunnerID || existing.RunnerName != job.RunnerName || existing.ScaleSet != job.ScaleSet || existing.OriginalAttempt < 1 {
		return Decision{}, fmt.Errorf("vanished-runner recovery identity changed")
	}
	// A crash after rerun but before journal update is reconstructed from the
	// authoritative attempt number, so the same run can never be rerun twice.
	if job.RunAttempt > existing.OriginalAttempt {
		if job.RunStatus == "completed" {
			return Decision{Action: ActionComplete, Reason: "replacement-attempt-terminal", Record: *existing}, nil
		}
		return Decision{Action: ActionAwaitRerun, Reason: "replacement-attempt-active", Record: *existing}, nil
	}
	switch existing.Stage {
	case StageDetected:
		if job.RunStatus == "completed" {
			return Decision{Action: ActionFullRerun, Reason: "force-cancel-became-terminal", Record: *existing}, nil
		}
		return Decision{Action: ActionForceCancel, Reason: "resume-force-cancel", Record: *existing}, nil
	case StageCancelRequested:
		if job.RunStatus == "completed" {
			return Decision{Action: ActionFullRerun, Reason: "cancel-terminal", Record: *existing}, nil
		}
		return Decision{Action: ActionAwaitCancel, Reason: "cancel-in-progress", Record: *existing}, nil
	case StageRerunRequested:
		return Decision{Action: ActionAwaitRerun, Reason: "rerun-attempt-not-visible", Record: *existing}, nil
	default:
		return Decision{}, fmt.Errorf("vanished-runner recovery stage is invalid")
	}
}
