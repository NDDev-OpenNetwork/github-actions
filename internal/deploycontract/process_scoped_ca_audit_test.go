package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const processScopedCAAuditPath = "../../config/process-scoped-ca-canary-audit.json"

func TestProcessScopedCACanaryEvidence(t *testing.T) {
	raw, err := os.ReadFile(processScopedCAAuditPath)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int    `json:"schema_version"`
		Host                  string `json:"host"`
		RepositoryMergeCommit string `json:"repository_merge_commit"`
		Provider              struct {
			Version                              string `json:"version"`
			Commit                               string `json:"commit"`
			BinarySHA256                         string `json:"binary_sha256"`
			ProcessScopedCA                      bool   `json:"process_scoped_ca"`
			GlobalTrustStoreMutatedPerAssignment bool   `json:"global_trust_store_mutated_per_assignment"`
			IncusAssignmentFiles                 int    `json:"incus_assignment_files"`
		} `json:"provider"`
		DiagnosticExporter struct {
			Version      string `json:"version"`
			Commit       string `json:"commit"`
			BinarySHA256 string `json:"binary_sha256"`
		} `json:"diagnostic_exporter"`
		RolloutRecovery struct {
			DiagnosticSHA256             string `json:"diagnostic_sha256"`
			InitialExportFailure         string `json:"initial_export_failure"`
			RustFSObjectKey              string `json:"rustfs_object_key"`
			AdmissionBypassed            bool   `json:"admission_bypassed"`
			EvidenceDeletedOrQuarantined bool   `json:"evidence_deleted_or_quarantined"`
			SystemdReturnedToRunning     bool   `json:"systemd_returned_to_running"`
		} `json:"rollout_recovery"`
		Workflow struct {
			RunID      int64  `json:"run_id"`
			JobID      int64  `json:"job_id"`
			HeadSHA    string `json:"head_sha"`
			Conclusion string `json:"conclusion"`
			RunnerName string `json:"runner_name"`
			Label      string `json:"label"`
		} `json:"workflow"`
		Latency struct {
			AssignmentToMetadata          float64 `json:"garm_assignment_to_first_metadata"`
			AssignmentToListening         float64 `json:"garm_assignment_to_runner_listening_upper_bound"`
			PreviousAssignmentToMetadata  float64 `json:"previous_garm_assignment_to_first_metadata"`
			PreviousAssignmentToListening float64 `json:"previous_garm_assignment_to_runner_listening_upper_bound"`
			MetadataImprovementPercent    float64 `json:"metadata_path_improvement_percent"`
		} `json:"latency_seconds"`
		Worker struct {
			InstanceName                       string `json:"instance_name"`
			DestroyedAfterJob                  bool   `json:"destroyed_after_job"`
			ReturnedToWarmPool                 bool   `json:"returned_to_warm_pool"`
			DiagnosticsSHA256                  string `json:"diagnostics_sha256"`
			DiagnosticsJWTShapeMatches         int    `json:"diagnostics_jwt_shape_matches"`
			DiagnosticsGitHubTokenShapeMatches int    `json:"diagnostics_github_token_shape_matches"`
			RustFSObjectKey                    string `json:"rustfs_object_key"`
		} `json:"worker"`
		Replacement struct {
			InstanceName    string `json:"instance_name"`
			ProviderVersion string `json:"provider_version"`
			ProviderCommit  string `json:"provider_commit"`
			Lifecycle       string `json:"lifecycle"`
			WarmReady       bool   `json:"warm_ready"`
			Claims          int    `json:"claims"`
		} `json:"replacement"`
		Postconditions struct {
			SystemdState                     string   `json:"systemd_state"`
			FailedUnits                      int      `json:"failed_units"`
			ObserverHealthy                  bool     `json:"observer_healthy"`
			ObserverFresh                    bool     `json:"observer_fresh"`
			JournalWarmReady                 int      `json:"journal_warm_ready"`
			JournalClaims                    int      `json:"journal_claims"`
			IncusOrphans                     int      `json:"incus_orphans"`
			DiagnosticBundles                int      `json:"diagnostic_bundles"`
			DiagnosticExportedBundles        int      `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles         int      `json:"diagnostic_pending_bundles"`
			LegacyListeners                  int      `json:"legacy_listeners"`
			ExamplePlatformContainersHealthy bool     `json:"example_platform_containers_healthy"`
			CaptchaContainersHealthy         bool     `json:"captcha_containers_healthy"`
			DeploymentStagingRemoved         bool     `json:"deployment_staging_removed"`
			ServicesActive                   []string `json:"services_active"`
		} `json:"postconditions"`
		Verdict struct {
			ProcessScopedCAOperational         bool `json:"process_scoped_ca_operational"`
			TLSVerificationRetained            bool `json:"tls_verification_retained"`
			SingleIncusAssignmentInjection     bool `json:"single_incus_assignment_injection"`
			WorkerDestroyed                    bool `json:"worker_destroyed"`
			DiagnosticsExported                bool `json:"diagnostics_exported"`
			UnassignedWarmDiagnosticsSeparated bool `json:"unassigned_warm_diagnostics_separated"`
			SingleSampleImproved               bool `json:"single_sample_improved"`
			WarmQueueToOnlineP95GateComplete   bool `json:"warm_queue_to_online_p95_gate_complete"`
			ProductionReliabilityGateComplete  bool `json:"production_reliability_gate_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryMergeCommit) {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	if audit.Provider.Version != "v0.1.5-nddev.6" || audit.Provider.Commit != audit.RepositoryMergeCommit ||
		!hex64.MatchString(audit.Provider.BinarySHA256) || !audit.Provider.ProcessScopedCA ||
		audit.Provider.GlobalTrustStoreMutatedPerAssignment || audit.Provider.IncusAssignmentFiles != 1 {
		t.Fatalf("provider rollout is not proven: %#v", audit.Provider)
	}
	if audit.DiagnosticExporter.Version != "v0.1.1" || audit.DiagnosticExporter.Commit != audit.RepositoryMergeCommit ||
		!hex64.MatchString(audit.DiagnosticExporter.BinarySHA256) {
		t.Fatalf("diagnostic exporter rollout is not proven: %#v", audit.DiagnosticExporter)
	}
	if !hex64.MatchString(audit.RolloutRecovery.DiagnosticSHA256) || audit.RolloutRecovery.InitialExportFailure != "bundle-verify" ||
		!strings.Contains(audit.RolloutRecovery.RustFSObjectKey, "/unassigned-warm/") || audit.RolloutRecovery.AdmissionBypassed ||
		audit.RolloutRecovery.EvidenceDeletedOrQuarantined || !audit.RolloutRecovery.SystemdReturnedToRunning {
		t.Fatalf("rollout recovery is not fail-closed: %#v", audit.RolloutRecovery)
	}
	if audit.Workflow.RunID <= 0 || audit.Workflow.JobID <= 0 || audit.Workflow.HeadSHA != audit.RepositoryMergeCommit ||
		audit.Workflow.Conclusion != "success" || audit.Workflow.RunnerName == "" || audit.Workflow.Label != "nddev-linux-standard" {
		t.Fatalf("workflow proof is incomplete: %#v", audit.Workflow)
	}
	if audit.Latency.AssignmentToMetadata >= audit.Latency.PreviousAssignmentToMetadata ||
		audit.Latency.AssignmentToListening >= audit.Latency.PreviousAssignmentToListening ||
		audit.Latency.MetadataImprovementPercent < 50 || audit.Latency.AssignmentToListening <= 5 {
		t.Fatalf("latency result or still-open gate is inconsistent: %#v", audit.Latency)
	}
	if audit.Worker.InstanceName == "" || !audit.Worker.DestroyedAfterJob || audit.Worker.ReturnedToWarmPool ||
		!hex64.MatchString(audit.Worker.DiagnosticsSHA256) || audit.Worker.DiagnosticsJWTShapeMatches != 0 ||
		audit.Worker.DiagnosticsGitHubTokenShapeMatches != 0 || !strings.Contains(audit.Worker.RustFSObjectKey, "/repository/example-user/github-actions/") {
		t.Fatalf("executed worker proof is incomplete: %#v", audit.Worker)
	}
	if audit.Replacement.InstanceName == "" || audit.Replacement.InstanceName == audit.Worker.InstanceName ||
		audit.Replacement.ProviderVersion != audit.Provider.Version || audit.Replacement.ProviderCommit != audit.Provider.Commit ||
		audit.Replacement.Lifecycle != "warm-unregistered" || !audit.Replacement.WarmReady || audit.Replacement.Claims != 0 {
		t.Fatalf("replacement proof is incomplete: %#v", audit.Replacement)
	}
	post := audit.Postconditions
	if post.SystemdState != "running" || post.FailedUnits != 0 || !post.ObserverHealthy || !post.ObserverFresh ||
		post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusOrphans != 0 ||
		post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatformContainersHealthy || !post.CaptchaContainersHealthy ||
		!post.DeploymentStagingRemoved || len(post.ServicesActive) != 7 {
		t.Fatalf("postconditions are incomplete: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.ProcessScopedCAOperational || !verdict.TLSVerificationRetained || !verdict.SingleIncusAssignmentInjection ||
		!verdict.WorkerDestroyed || !verdict.DiagnosticsExported || !verdict.UnassignedWarmDiagnosticsSeparated ||
		!verdict.SingleSampleImproved || verdict.WarmQueueToOnlineP95GateComplete || verdict.ProductionReliabilityGateComplete {
		t.Fatalf("verdict overclaims or omits runtime proof: %#v", verdict)
	}
}
