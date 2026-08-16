package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestGARMNDDev7FaultRecoveryAuditIsExactAndIncompleteOnlyForRemainingGates(t *testing.T) {
	file, err := os.Open("../../config/garm-nddev7-fault-recovery-audit.json")
	if err != nil {
		t.Fatalf("open nddev7 fault recovery audit: %v", err)
	}
	defer file.Close()

	var audit struct {
		SchemaVersion int    `json:"schema_version"`
		CapturedAt    string `json:"captured_at"`
		Host          string `json:"host"`
		Repository    struct {
			MergeCommit          string `json:"merge_commit"`
			ImplementationCommit string `json:"implementation_commit"`
			PullRequest          int    `json:"pull_request"`
			MergeMethod          string `json:"merge_method"`
		} `json:"repository"`
		Artifacts struct {
			GARMVersion          string `json:"garm_version"`
			GARMBinarySHA256     string `json:"garm_binary_sha256"`
			CleanupPatchSHA256   string `json:"cleanup_patch_sha256"`
			ProviderVersion      string `json:"provider_version"`
			ProviderBinarySHA256 string `json:"provider_binary_sha256"`
			ObserverVersion      string `json:"observer_version"`
			ObserverBinarySHA256 string `json:"observer_binary_sha256"`
			ProviderConfigSHA256 string `json:"provider_config_sha256"`
			RollbackBinarySHA256 string `json:"rollback_binary_sha256"`
		} `json:"artifacts"`
		AcquisitionFailure struct {
			WorkflowRunID                                int64  `json:"workflow_run_id"`
			WorkflowJobID                                int64  `json:"workflow_job_id"`
			HeadSHA                                      string `json:"head_sha"`
			OpaqueJobID                                  string `json:"opaque_job_id"`
			FaultKind                                    string `json:"fault_kind"`
			FaultConfigSHA256                            string `json:"fault_config_sha256"`
			FaultStartedAt                               string `json:"fault_started_at"`
			JobAssignedAt                                string `json:"job_assigned_at"`
			ProviderFailureAt                            string `json:"provider_failure_at"`
			FailedRunnerName                             string `json:"failed_runner_name"`
			RealIncusInstancesCreatedByFailure           int    `json:"real_incus_instances_created_by_failure"`
			ProviderClaimsCreatedByFailure               int    `json:"provider_claims_created_by_failure"`
			GitHubRunnerFirstEmptyObservationAt          string `json:"github_runner_first_empty_observation_at"`
			GitHubRunnerEmptyObservations                int    `json:"github_runner_empty_observations"`
			FailedRegistrationAbsentWhileProviderRetried bool   `json:"failed_registration_absent_while_provider_teardown_retried"`
			ConfigRestoredAt                             string `json:"config_restored_at"`
			RecoveryRunnerName                           string `json:"recovery_runner_name"`
			RecoveryInstance                             string `json:"recovery_instance"`
			WorkflowStartedAt                            string `json:"workflow_started_at"`
			WorkflowCompletedAt                          string `json:"workflow_completed_at"`
			WorkflowConclusion                           string `json:"workflow_conclusion"`
			PostActionCompleted                          bool   `json:"post_action_completed"`
			ExecutedInstanceDestroyed                    bool   `json:"executed_instance_destroyed"`
			DiagnosticSHA256                             string `json:"diagnostic_sha256"`
			ReplacementInstance                          string `json:"replacement_instance"`
		} `json:"acquisition_failure"`
		ActiveManagerRestart struct {
			WorkflowRunID             int64  `json:"workflow_run_id"`
			WorkflowJobID             int64  `json:"workflow_job_id"`
			HeadSHA                   string `json:"head_sha"`
			OpaqueJobID               string `json:"opaque_job_id"`
			RunnerName                string `json:"runner_name"`
			Instance                  string `json:"instance"`
			PIDBefore                 int    `json:"pid_before"`
			RestartStartedAt          string `json:"restart_started_at"`
			ServiceOnlineAt           string `json:"service_online_at"`
			PIDAfter                  int    `json:"pid_after"`
			IntentPreserved           bool   `json:"intent_preserved"`
			ClaimPreserved            bool   `json:"claim_preserved"`
			InstancePreserved         bool   `json:"instance_preserved"`
			DuplicateInstances        int    `json:"duplicate_instances"`
			WorkflowConclusion        string `json:"workflow_conclusion"`
			PostActionCompleted       bool   `json:"post_action_completed"`
			CompletedAt               string `json:"completed_at"`
			ExecutedInstanceDestroyed bool   `json:"executed_instance_destroyed"`
			DiagnosticSHA256          string `json:"diagnostic_sha256"`
			ReplacementInstance       string `json:"replacement_instance"`
		} `json:"active_manager_restart"`
		Postconditions struct {
			QueueSchemaVersion                  int  `json:"queue_schema_version"`
			QueueGeneration                     int  `json:"queue_generation"`
			QueueIntents                        int  `json:"queue_intents"`
			ProviderSchemaVersion               int  `json:"provider_schema_version"`
			ProviderGeneration                  int  `json:"provider_generation"`
			ProviderClaims                      int  `json:"provider_claims"`
			WarmReady                           int  `json:"warm_ready"`
			IncusVisibleInstances               int  `json:"incus_visible_instances"`
			IncusOrphans                        int  `json:"incus_orphans"`
			GitHubRepositoryRunnerRegistrations int  `json:"github_repository_runner_registrations"`
			ObserverHealthy                     bool `json:"observer_healthy"`
			ObserverFresh                       bool `json:"observer_fresh"`
			DiagnosticBundles                   int  `json:"diagnostic_bundles"`
			DiagnosticExportedBundles           int  `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles            int  `json:"diagnostic_pending_bundles"`
			RootDiskUsedPercent                 int  `json:"root_disk_used_percent"`
			SwapUsedBytes                       int  `json:"swap_used_bytes"`
			FailedSystemdUnits                  int  `json:"failed_systemd_units"`
			LegacyAndExamplePlatformRunnerListeners     int  `json:"legacy_and_example-platform_runner_listeners"`
			ExamplePlatformContainersHealthy            bool `json:"example_platform_containers_healthy"`
			CaptchaContainersHealthy            bool `json:"captcha_containers_healthy"`
		} `json:"postconditions"`
		Verdict struct {
			FailedJITRegistrationCleanupComplete       bool   `json:"failed_jit_registration_cleanup_complete"`
			ProviderAcquisitionFailureRecoveryComplete bool   `json:"provider_acquisition_failure_recovery_complete"`
			ActiveManagerRestartRecoveryComplete       bool   `json:"active_manager_restart_recovery_complete"`
			CancellationCleanupComplete                bool   `json:"cancellation_cleanup_complete"`
			ProductionReliabilityGateComplete          bool   `json:"production_reliability_gate_complete"`
			RemainingGate                              string `json:"remaining_gate"`
		} `json:"verdict"`
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&audit); err != nil {
		t.Fatalf("decode nddev7 fault recovery audit: %v", err)
	}

	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || audit.CapturedAt == "" ||
		!hex40.MatchString(audit.Repository.MergeCommit) || !hex40.MatchString(audit.Repository.ImplementationCommit) ||
		audit.Repository.PullRequest != 130 || audit.Repository.MergeMethod != "merge" {
		t.Fatalf("invalid audit identity: %#v", audit.Repository)
	}
	if audit.Artifacts.GARMVersion != "v0.2.1-nddev.7" || audit.Artifacts.ProviderVersion != "v0.1.5-nddev.16" ||
		audit.Artifacts.ObserverVersion != "v0.5.0" || !hex64.MatchString(audit.Artifacts.GARMBinarySHA256) ||
		!hex64.MatchString(audit.Artifacts.CleanupPatchSHA256) || !hex64.MatchString(audit.Artifacts.ProviderBinarySHA256) ||
		!hex64.MatchString(audit.Artifacts.ObserverBinarySHA256) || !hex64.MatchString(audit.Artifacts.ProviderConfigSHA256) ||
		!hex64.MatchString(audit.Artifacts.RollbackBinarySHA256) {
		t.Fatalf("invalid deployed artifacts: %#v", audit.Artifacts)
	}
	failure := audit.AcquisitionFailure
	if failure.WorkflowRunID <= 0 || failure.WorkflowJobID <= 0 || failure.HeadSHA != audit.Repository.MergeCommit ||
		!uuid.MatchString(failure.OpaqueJobID) || failure.FaultKind == "" || !hex64.MatchString(failure.FaultConfigSHA256) ||
		failure.FailedRunnerName == "" || failure.RealIncusInstancesCreatedByFailure != 0 || failure.ProviderClaimsCreatedByFailure != 0 ||
		failure.GitHubRunnerEmptyObservations < 2 || !failure.FailedRegistrationAbsentWhileProviderRetried ||
		failure.WorkflowConclusion != "success" || !failure.PostActionCompleted || !failure.ExecutedInstanceDestroyed ||
		!hex64.MatchString(failure.DiagnosticSHA256) || failure.RecoveryRunnerName == "" || failure.RecoveryInstance == failure.ReplacementInstance {
		t.Fatalf("invalid acquisition recovery evidence: %#v", failure)
	}
	restart := audit.ActiveManagerRestart
	if restart.WorkflowRunID <= 0 || restart.WorkflowJobID <= 0 || restart.HeadSHA != audit.Repository.MergeCommit ||
		!uuid.MatchString(restart.OpaqueJobID) || restart.PIDBefore <= 0 || restart.PIDAfter <= 0 || restart.PIDBefore == restart.PIDAfter ||
		!restart.IntentPreserved || !restart.ClaimPreserved || !restart.InstancePreserved || restart.DuplicateInstances != 0 ||
		restart.WorkflowConclusion != "cancelled" || !restart.PostActionCompleted || !restart.ExecutedInstanceDestroyed ||
		!hex64.MatchString(restart.DiagnosticSHA256) || restart.Instance == restart.ReplacementInstance {
		t.Fatalf("invalid active restart evidence: %#v", restart)
	}
	post := audit.Postconditions
	if post.QueueSchemaVersion != 1 || post.QueueIntents != 0 || post.ProviderSchemaVersion != 4 || post.ProviderClaims != 0 ||
		post.WarmReady != 1 || post.IncusVisibleInstances != 1 || post.IncusOrphans != 0 || post.GitHubRepositoryRunnerRegistrations != 0 ||
		!post.ObserverHealthy || !post.ObserverFresh || post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 ||
		post.RootDiskUsedPercent >= 80 || post.SwapUsedBytes != 0 || post.FailedSystemdUnits != 0 ||
		post.LegacyAndExamplePlatformRunnerListeners != 13 || !post.ExamplePlatformContainersHealthy || !post.CaptchaContainersHealthy {
		t.Fatalf("invalid postconditions: %#v", post)
	}
	if !audit.Verdict.FailedJITRegistrationCleanupComplete || !audit.Verdict.ProviderAcquisitionFailureRecoveryComplete ||
		!audit.Verdict.ActiveManagerRestartRecoveryComplete || !audit.Verdict.CancellationCleanupComplete ||
		audit.Verdict.ProductionReliabilityGateComplete || audit.Verdict.RemainingGate == "" {
		t.Fatalf("invalid verdict: %#v", audit.Verdict)
	}
}
