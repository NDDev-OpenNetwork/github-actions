package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const garmRestartRecoveryAuditPath = "../../config/garm-restart-recovery-audit.json"

func TestGARMRestartRecoveryEvidence(t *testing.T) {
	raw, err := os.ReadFile(garmRestartRecoveryAuditPath)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion    int    `json:"schema_version"`
		Host             string `json:"host"`
		RepositoryCommit string `json:"repository_commit"`
		Provider         struct {
			Version      string `json:"version"`
			Commit       string `json:"commit"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"provider"`
		IdleRestart struct {
			PIDBefore               int    `json:"pid_before"`
			PIDAfter                int    `json:"pid_after"`
			InstanceBefore          string `json:"instance_before"`
			InstanceAfter           string `json:"instance_after"`
			WarmReadyBefore         int    `json:"warm_ready_before"`
			WarmReadyAfter          int    `json:"warm_ready_after"`
			ClaimsBefore            int    `json:"claims_before"`
			ClaimsAfter             int    `json:"claims_after"`
			DuplicateInstances      int    `json:"duplicate_instances"`
			UnexpectedInstanceChurn int    `json:"unexpected_instance_churn"`
		} `json:"idle_restart"`
		PostIdleRestartCanary struct {
			RunID                     int64   `json:"run_id"`
			JobID                     int64   `json:"job_id"`
			HeadSHA                   string  `json:"head_sha"`
			Conclusion                string  `json:"conclusion"`
			Instance                  string  `json:"instance"`
			Destroyed                 bool    `json:"destroyed"`
			ReturnedToWarmPool        bool    `json:"returned_to_warm_pool"`
			DiagnosticSHA256          string  `json:"diagnostic_sha256"`
			DiagnosticsExported       bool    `json:"diagnostics_exported_to_rustfs"`
			AssignmentToListeningSecs float64 `json:"assignment_to_listening_upper_bound_seconds"`
		} `json:"post_idle_restart_canary"`
		ActiveJobRestart struct {
			RunID                         int64  `json:"run_id"`
			JobID                         int64  `json:"job_id"`
			HeadSHA                       string `json:"head_sha"`
			WorkflowConclusion            string `json:"workflow_conclusion"`
			Instance                      string `json:"instance"`
			PIDBefore                     int    `json:"pid_before"`
			PIDAfter                      int    `json:"pid_after"`
			ClaimsBefore                  int    `json:"claims_before"`
			ClaimsAfterRestart            int    `json:"claims_after_restart"`
			MatchingInstancesAfterRestart int    `json:"matching_instances_after_restart"`
			InstanceDestroyed             bool   `json:"instance_destroyed"`
			ClaimReleased                 bool   `json:"claim_released"`
			PostActionCompleted           bool   `json:"post_action_completed"`
			DiagnosticSHA256              string `json:"diagnostic_sha256"`
			RustFSObjectKey               string `json:"rustfs_object_key"`
		} `json:"active_job_restart"`
		AmbiguousProviderOperationContract struct {
			TestMode                              string `json:"test_mode"`
			RaceRepetitions                       int    `json:"race_repetitions"`
			CreateTest                            string `json:"create_test"`
			CreateCallsAfterRetry                 int    `json:"create_calls_after_retry"`
			DeleteTest                            string `json:"delete_test"`
			DeleteCallsAfterRetry                 int    `json:"delete_calls_after_retry"`
			ExternalRuntimeFaultInjectionComplete bool   `json:"external_runtime_fault_injection_complete"`
		} `json:"ambiguous_provider_operation_contract"`
		Postconditions struct {
			SystemdState              string `json:"systemd_state"`
			FailedUnits               int    `json:"failed_units"`
			ObserverHealthy           bool   `json:"observer_healthy"`
			ObserverFresh             bool   `json:"observer_fresh"`
			JournalWarmReady          int    `json:"journal_warm_ready"`
			JournalClaims             int    `json:"journal_claims"`
			IncusVisibleInstances     int    `json:"incus_visible_instances"`
			IncusOrphans              int    `json:"incus_orphans"`
			IncusMissingInstances     int    `json:"incus_missing_instances"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			LegacyListeners           int    `json:"legacy_listeners"`
			ExamplePlatformHealthy    bool   `json:"example_platform_containers_healthy"`
			CaptchaHealthy            bool   `json:"captcha_containers_healthy"`
		} `json:"postconditions"`
		Verdict struct {
			IdleManagerRestartRecoveryComplete      bool `json:"idle_manager_restart_recovery_complete"`
			ActiveJobManagerRestartRecoveryComplete bool `json:"active_job_manager_restart_recovery_complete"`
			CancellationCleanupComplete             bool `json:"cancellation_cleanup_complete"`
			DuplicateInstancePreventionComplete     bool `json:"duplicate_instance_prevention_complete"`
			AmbiguousProviderRetryContractComplete  bool `json:"ambiguous_provider_retry_contract_complete"`
			HostRebootRecoveryComplete              bool `json:"host_reboot_recovery_complete"`
			FaultInjectionSuiteComplete             bool `json:"fault_injection_suite_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryCommit) ||
		audit.Provider.Version != "v0.1.5-nddev.6" || !hex40.MatchString(audit.Provider.Commit) || !hex64.MatchString(audit.Provider.BinarySHA256) {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	idle := audit.IdleRestart
	if idle.PIDBefore <= 0 || idle.PIDAfter <= 0 || idle.PIDBefore == idle.PIDAfter || idle.InstanceBefore == "" ||
		idle.InstanceBefore != idle.InstanceAfter || idle.WarmReadyBefore != 1 || idle.WarmReadyAfter != 1 ||
		idle.ClaimsBefore != 0 || idle.ClaimsAfter != 0 || idle.DuplicateInstances != 0 || idle.UnexpectedInstanceChurn != 0 {
		t.Fatalf("idle restart did not preserve exactly one warm lease: %#v", idle)
	}
	canary := audit.PostIdleRestartCanary
	if canary.RunID <= 0 || canary.JobID <= 0 || canary.HeadSHA != audit.RepositoryCommit || canary.Conclusion != "success" ||
		canary.Instance != idle.InstanceAfter || !canary.Destroyed || canary.ReturnedToWarmPool || !hex64.MatchString(canary.DiagnosticSHA256) ||
		!canary.DiagnosticsExported || canary.AssignmentToListeningSecs <= 0 || canary.AssignmentToListeningSecs >= 5 {
		t.Fatalf("post-restart canary is incomplete: %#v", canary)
	}
	active := audit.ActiveJobRestart
	if active.RunID <= 0 || active.JobID <= 0 || active.HeadSHA != audit.RepositoryCommit || active.WorkflowConclusion != "cancelled" ||
		active.Instance == "" || active.PIDBefore <= 0 || active.PIDAfter <= 0 || active.PIDBefore == active.PIDAfter ||
		active.ClaimsBefore != 1 || active.ClaimsAfterRestart != 1 || active.MatchingInstancesAfterRestart != 1 ||
		!active.InstanceDestroyed || !active.ClaimReleased || !active.PostActionCompleted || !hex64.MatchString(active.DiagnosticSHA256) ||
		!strings.Contains(active.RustFSObjectKey, active.DiagnosticSHA256) {
		t.Fatalf("active-job restart recovery is incomplete: %#v", active)
	}
	contract := audit.AmbiguousProviderOperationContract
	if contract.TestMode != "deterministic mocked Incus operation boundary" || contract.RaceRepetitions < 20 ||
		contract.CreateTest != "TestCreateInstanceRetryAdoptsVMCreatedBeforeAmbiguousOperationTimeout" || contract.CreateCallsAfterRetry != 1 ||
		contract.DeleteTest != "TestDeleteInstanceRetryReleasesVMDeletedBeforeAmbiguousOperationTimeout" || contract.DeleteCallsAfterRetry != 1 ||
		contract.ExternalRuntimeFaultInjectionComplete {
		t.Fatalf("ambiguous provider operation contract overclaims or is incomplete: %#v", contract)
	}
	post := audit.Postconditions
	if post.SystemdState != "running" || post.FailedUnits != 0 || !post.ObserverHealthy || !post.ObserverFresh ||
		post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusVisibleInstances != 1 || post.IncusOrphans != 0 ||
		post.IncusMissingInstances != 0 || post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatformHealthy || !post.CaptchaHealthy {
		t.Fatalf("postconditions are incomplete: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.IdleManagerRestartRecoveryComplete || !verdict.ActiveJobManagerRestartRecoveryComplete ||
		!verdict.CancellationCleanupComplete || !verdict.DuplicateInstancePreventionComplete ||
		!verdict.AmbiguousProviderRetryContractComplete || verdict.HostRebootRecoveryComplete || verdict.FaultInjectionSuiteComplete {
		t.Fatalf("verdict overclaims or omits proven recovery: %#v", verdict)
	}
}
