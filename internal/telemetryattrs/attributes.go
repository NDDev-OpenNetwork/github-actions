// Package telemetryattrs owns the stable semantic vocabulary emitted by NDDev
// Drakkars components. Values may differ per operation; keys never do.
package telemetryattrs

const (
	ServiceNamespace = "nddev-drakkars"

	RunnerName                = "runner.name"
	RunnerPool                = "runner.pool"
	RunnerWarmClaimed         = "runner.warm_claimed"
	AdmissionReason           = "admission.reason"
	AdmissionPreemptedWorkers = "admission.preempted_warm_workers"
	OperationOutcome          = "operation.outcome"
	OutcomeSuccess            = "success"
	OutcomeError              = "error"
)
