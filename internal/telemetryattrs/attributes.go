// Package telemetryattrs owns the stable semantic vocabulary emitted by NDDev
// Drakkars components. Values may differ per operation; keys never do.
package telemetryattrs

const (
	ServiceNamespace = "nddev-drakkars"

	RunnerName                = "runner.name"
	InstanceName              = "instance.name"
	RunnerPool                = "runner.pool"
	RunnerWarmClaimed         = "runner.warm_claimed"
	AdmissionReason           = "admission.reason"
	AdmissionPreemptedWorkers = "admission.preempted_warm_workers"
	OperationOutcome          = "operation.outcome"
	GitHubRepository          = "github.repository"
	GitHubRepositoryID        = "github.repository_id"
	GitHubWorkflowRunID       = "github.workflow_run_id"
	GitHubRunAttempt          = "github.run_attempt"
	GitHubJobName             = "github.job_name"
	GitHubWorkflowRef         = "github.workflow_ref"
	GitHubCommitSHA           = "github.commit_sha"
	OutcomeSuccess            = "success"
	OutcomeError              = "error"
)
