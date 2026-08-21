// Package garmderivative is the typed reading of config/garm-derivative.yaml,
// the manifest that describes how the in-tree GARM derivative is built and what
// it must come out as.
//
// It exists because that manifest was the only one under config/ with no Go
// package behind it. Its schema lived in a test, and scripts/build-garm-nddev.sh
// restated every value it declares as a shell literal, so the manifest was
// documentation that a build could contradict. RenderBuildScript makes the
// manifest the input the build actually reads, and FieldDispositions makes the
// mapping exhaustive: a field added here has to be given a consumer or declared
// not to have one, which is what the fifth patch digest never was.
package garmderivative

// Manifest is config/garm-derivative.yaml.
type Manifest struct {
	SchemaVersion     int             `json:"schema_version" yaml:"schema_version"`
	Artifact          string          `json:"artifact" yaml:"artifact"`
	DerivativeVersion string          `json:"derivative_version" yaml:"derivative_version"`
	Upstream          Upstream        `json:"upstream" yaml:"upstream"`
	Patches           []Source        `json:"patches" yaml:"patches"`
	Overlays          []Source        `json:"overlays" yaml:"overlays"`
	Build             Build           `json:"build" yaml:"build"`
	RuntimeContract   RuntimeContract `json:"runtime_contract" yaml:"runtime_contract"`
}

// Upstream is the provenance the derivative is derived from. Commit is what the
// build fetches; Release and ReleaseAssetSHA256 describe the upstream release
// that commit belongs to and are consumed by docs/upstream-baseline.md rather
// than by the build.
type Upstream struct {
	Repository         string `json:"repository" yaml:"repository"`
	Release            string `json:"release" yaml:"release"`
	Commit             string `json:"commit" yaml:"commit"`
	ReleaseAssetSHA256 string `json:"release_asset_sha256" yaml:"release_asset_sha256"`
}

// Source is one patch or one overlay file: where it lives, what it must hash to,
// and why it exists. Purpose is reviewed prose and is deliberately part of the
// manifest -- a digest with no stated reason is a digest nobody can re-approve.
type Source struct {
	Path    string `json:"path" yaml:"path"`
	SHA256  string `json:"sha256" yaml:"sha256"`
	Purpose string `json:"purpose" yaml:"purpose"`
}

// Build is every input that decides the bytes of the artifact, plus the digest
// those inputs must produce. Everything here is enforced by the build script;
// a field that were not would be a claim the build is free to contradict.
type Build struct {
	ContainerImage            string   `json:"container_image" yaml:"container_image"`
	GoVersion                 string   `json:"go_version" yaml:"go_version"`
	CGOEnabled                bool     `json:"cgo_enabled" yaml:"cgo_enabled"`
	TargetOS                  string   `json:"target_os" yaml:"target_os"`
	TargetArch                string   `json:"target_arch" yaml:"target_arch"`
	NetworkDuringTestAndBuild string   `json:"network_during_test_and_build" yaml:"network_during_test_and_build"`
	ModuleMode                string   `json:"module_mode" yaml:"module_mode"`
	Tags                      []string `json:"tags" yaml:"tags"`
	ReproducibleRebuilds      int      `json:"reproducible_rebuilds" yaml:"reproducible_rebuilds"`
	MaximumRequiredGLIBC      string   `json:"maximum_required_glibc" yaml:"maximum_required_glibc"`
	BinarySHA256              string   `json:"binary_sha256" yaml:"binary_sha256"`
}

// RuntimeContract is what the derivative promises to behave like. None of it is
// a build input: it is held by the patch and overlay content assertions in
// internal/deploycontract, which read the same manifest.
type RuntimeContract struct {
	EventDrivenScaleSetWake                  bool `json:"event_driven_scale_set_wake" yaml:"event_driven_scale_set_wake"`
	EventDrivenInstanceWake                  bool `json:"event_driven_instance_wake" yaml:"event_driven_instance_wake"`
	StartupStatesProtected                   bool `json:"startup_states_protected_from_scale_down" yaml:"startup_states_protected_from_scale_down"`
	PeriodicReconciliationSecs               int  `json:"periodic_reconciliation_seconds" yaml:"periodic_reconciliation_seconds"`
	DurableQueueIntentBeforeAcquire          bool `json:"durable_queue_intent_before_acquire" yaml:"durable_queue_intent_before_acquire"`
	JobAssignedProvisionalCapacity           bool `json:"job_assigned_provisional_capacity" yaml:"job_assigned_provisional_capacity"`
	JobAvailableIdentityBindingBeforeAcquire bool `json:"job_available_identity_binding_before_acquire" yaml:"job_available_identity_binding_before_acquire"`
	StartupQueuePromotion                    bool `json:"startup_queue_promotion" yaml:"startup_queue_promotion"`
	DeferredAvailableMessageRetained         bool `json:"deferred_available_message_retained" yaml:"deferred_available_message_retained"`
	GlobalMaxInFlight                        int  `json:"global_max_in_flight" yaml:"global_max_in_flight"`
	WeightedRepositoryFairness               bool `json:"weighted_repository_fairness" yaml:"weighted_repository_fairness"`
	PerRepositoryLimit                       int  `json:"per_repository_limit" yaml:"per_repository_limit"`
	PriorityAgingSeconds                     int  `json:"priority_aging_seconds" yaml:"priority_aging_seconds"`
	FailedScaleSetRegistrationCleanup        bool `json:"failed_scale_set_registration_cleanup" yaml:"failed_scale_set_registration_cleanup"`
	DirectJITProviderHandoff                 bool `json:"direct_jit_provider_handoff" yaml:"direct_jit_provider_handoff"`
	DirectJITPhaseTelemetry                  bool `json:"direct_jit_phase_telemetry" yaml:"direct_jit_phase_telemetry"`
	DurableProviderRetry                     bool `json:"durable_provider_retry" yaml:"durable_provider_retry"`
	ProviderRetryMaximumAttempts             int  `json:"provider_retry_maximum_attempts" yaml:"provider_retry_maximum_attempts"`
	ProviderRetryBackoffCapSeconds           int  `json:"provider_retry_backoff_cap_seconds" yaml:"provider_retry_backoff_cap_seconds"`
	CapacityRetryBackoffCapSeconds           int  `json:"capacity_retry_backoff_cap_seconds" yaml:"capacity_retry_backoff_cap_seconds"`
	CapacityRetryWakeAfterDelete             bool `json:"capacity_retry_wake_after_delete" yaml:"capacity_retry_wake_after_delete"`
	BoundedScaleUpCapacityProbe              bool `json:"bounded_scale_up_capacity_probe" yaml:"bounded_scale_up_capacity_probe"`
	AuthoritativeJobReconciliation           bool `json:"authoritative_job_reconciliation" yaml:"authoritative_job_reconciliation"`
	AuthoritativeQueueIntentReconciliation   bool `json:"authoritative_queue_intent_reconciliation" yaml:"authoritative_queue_intent_reconciliation"`
	AuthoritativeIdleOfflineRunnerReaping    bool `json:"authoritative_idle_offline_runner_reaping" yaml:"authoritative_idle_offline_runner_reaping"`
	AuthoritativeAccessRefusalBackoffSeconds int  `json:"authoritative_access_refusal_backoff_seconds" yaml:"authoritative_access_refusal_backoff_seconds"`
	JobStartedRunnerIdentity                 bool `json:"job_started_runner_identity" yaml:"job_started_runner_identity"`
	WeightedCPUMemoryAdmission               bool `json:"weighted_cpu_memory_admission" yaml:"weighted_cpu_memory_admission"`
	PriorityZeroCapacityReservation          bool `json:"priority_zero_capacity_reservation" yaml:"priority_zero_capacity_reservation"`
	JobReconciliationIntervalSeconds         int  `json:"job_reconciliation_interval_seconds" yaml:"job_reconciliation_interval_seconds"`
	JobReconciliationBatchSize               int  `json:"job_reconciliation_batch_size" yaml:"job_reconciliation_batch_size"`
	OfficialActionsRunnerUnchanged           bool `json:"official_actions_runner_unchanged" yaml:"official_actions_runner_unchanged"`
}

// OverlayTarget is where an overlay file is installed inside the checked-out
// upstream tree. It is derived rather than declared: an overlay replaces the
// file at the same path under the upstream root, and OverlayPrefix is what
// separates the two roots. Declaring it as well would be a second place for the
// same fact to be written down.
const OverlayPrefix = "third_party/garm/overlay/"
