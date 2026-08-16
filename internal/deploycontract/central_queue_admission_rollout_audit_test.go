package deploycontract

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestCentralQueueAdmissionRolloutIsBoundedAndHonest(t *testing.T) {
	source, err := os.ReadFile("../../config/central-queue-admission-rollout-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int       `json:"schema_version"`
		CapturedAt            time.Time `json:"captured_at"`
		Host                  string    `json:"host"`
		RepositoryMergeCommit string    `json:"repository_merge_commit"`
		Artifacts             struct {
			GARMVersion      string `json:"garm_version"`
			GARMBinary       string `json:"garm_binary_sha256"`
			ControllerBinary string `json:"controller_binary_sha256"`
			ProviderVersion  string `json:"provider_version"`
			ProviderBinary   string `json:"provider_binary_sha256"`
			ObserverVersion  string `json:"observer_version"`
			ObserverBinary   string `json:"observer_binary_sha256"`
		} `json:"artifacts"`
		ProtocolFindings []struct {
			Derivative         string `json:"derivative"`
			RunID              int64  `json:"run_id"`
			HeadSHA            string `json:"head_sha"`
			JobID              string `json:"job_id"`
			Boundary           string `json:"boundary"`
			FailedClosed       bool   `json:"failed_closed"`
			WorkerAssigned     bool   `json:"worker_assigned"`
			WorkflowConclusion string `json:"workflow_conclusion"`
			RollbackComplete   bool   `json:"rollback_completed"`
		} `json:"protocol_findings"`
		Reconciliation struct {
			RunID                     int64  `json:"run_id"`
			JobID                     string `json:"job_id"`
			GitHubConclusion          string `json:"github_conclusion"`
			ProviderClaimsBefore      int    `json:"provider_claims_before"`
			IncusInstancesBefore      int    `json:"incus_instances_before"`
			WritersStopped            bool   `json:"writers_stopped"`
			ExactKeyChecked           bool   `json:"exact_key_checked"`
			FlockHeld                 bool   `json:"flock_held"`
			TemporaryFileFsynced      bool   `json:"temporary_file_fsynced"`
			DirectoryFsynced          bool   `json:"directory_fsynced"`
			GenerationBefore          uint64 `json:"journal_generation_before"`
			GenerationAfter           uint64 `json:"journal_generation_after"`
			RemovedIntents            int    `json:"removed_intents"`
			RepositoryStridePreserved bool   `json:"repository_stride_preserved"`
		} `json:"terminal_reconciliation"`
		Canary struct {
			RunID                         int64     `json:"run_id"`
			JobID                         int64     `json:"job_id"`
			HeadSHA                       string    `json:"head_sha"`
			CreatedAt                     time.Time `json:"created_at"`
			StartedAt                     time.Time `json:"started_at"`
			CompletedAt                   time.Time `json:"completed_at"`
			Conclusion                    string    `json:"conclusion"`
			Label                         string    `json:"label"`
			RunnerName                    string    `json:"runner_name"`
			OpaqueJobID                   string    `json:"opaque_job_id"`
			CreatedToJobStartSeconds      int       `json:"created_to_job_start_seconds"`
			JobDurationSeconds            int       `json:"job_duration_seconds"`
			MixedStartedCompletedObserved bool      `json:"mixed_started_completed_observed"`
			ImmutableBoundaryPassed       bool      `json:"immutable_boundary_passed"`
			RustFSCacheDeliveryPassed     bool      `json:"rustfs_cache_delivery_passed"`
			CompositeActionsPassed        bool      `json:"composite_actions_passed"`
			CommandFilesPassed            bool      `json:"command_files_passed"`
			ArtifactUploadPassed          bool      `json:"artifact_upload_passed"`
			PostActionPassed              bool      `json:"post_action_passed"`
		} `json:"accepted_canary"`
		Worker struct {
			InstanceName                string `json:"instance_name"`
			DestroyedAfterJob           bool   `json:"destroyed_after_job"`
			ReturnedToWarmPool          bool   `json:"returned_to_warm_pool"`
			DiagnosticsArchive          string `json:"diagnostics_archive"`
			DiagnosticsSHA256           string `json:"diagnostics_sha256"`
			DiagnosticsBytes            int64  `json:"diagnostics_bytes"`
			DiagnosticsExportedToRustFS bool   `json:"diagnostics_exported_to_rustfs"`
		} `json:"worker"`
		Replacement struct {
			InstanceName string    `json:"instance_name"`
			CreatedAt    time.Time `json:"created_at"`
			Lifecycle    string    `json:"lifecycle"`
			Ready        bool      `json:"ready"`
			Claims       int       `json:"claims"`
		} `json:"replacement"`
		Postconditions struct {
			GARMSystemdRestarts          int  `json:"garm_systemd_restarts"`
			FailedUnits                  int  `json:"failed_units"`
			ObserverHealthy              bool `json:"observer_healthy"`
			ObserverFresh                bool `json:"observer_fresh"`
			QueueSchemaVersion           int  `json:"queue_schema_version"`
			QueueGeneration              int  `json:"queue_generation"`
			QueueIntents                 int  `json:"queue_intents"`
			ProviderJournalSchemaVersion int  `json:"provider_journal_schema_version"`
			ProviderClaims               int  `json:"provider_claims"`
			IncusVisibleInstances        int  `json:"incus_visible_instances"`
			DiagnosticBundles            int  `json:"diagnostic_bundles"`
			DiagnosticExportedBundles    int  `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles     int  `json:"diagnostic_pending_bundles"`
			RootFreePercent              int  `json:"root_free_percent"`
			LegacyListeners              int  `json:"legacy_listeners"`
			ExamplePlatformContainersHealthy     bool `json:"example_platform_containers_healthy"`
			CaptchaContainersHealthy     bool `json:"captcha_containers_healthy"`
			RustFSActive                 bool `json:"rustfs_active"`
			ZotActive                    bool `json:"zot_active"`
		} `json:"postconditions"`
		Verdict struct {
			CentralAdmissionOperational bool   `json:"central_pre_capacity_admission_operational"`
			SparseIdentityOperational   bool   `json:"sparse_assigned_identity_operational"`
			MixedTerminalConverges      bool   `json:"mixed_terminal_batch_converges"`
			OneJobVMOperational         bool   `json:"one_job_vm_lifecycle_operational"`
			WarmReplacementOperational  bool   `json:"warm_replacement_operational"`
			SingleSampleGateComplete    bool   `json:"single_sample_gate_complete"`
			ReliabilityGateComplete     bool   `json:"production_reliability_gate_complete"`
			RemainingGate               string `json:"remaining_gate"`
		} `json:"verdict"`
	}
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if audit.SchemaVersion != 1 || audit.CapturedAt.IsZero() || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryMergeCommit) {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	artifacts := audit.Artifacts
	if artifacts.GARMVersion != "v0.2.1-nddev.6" || artifacts.ProviderVersion != "v0.1.5-nddev.16" || artifacts.ObserverVersion != "v0.5.0" {
		t.Fatalf("unexpected artifact versions: %#v", artifacts)
	}
	for _, digest := range []string{artifacts.GARMBinary, artifacts.ControllerBinary, artifacts.ProviderBinary, artifacts.ObserverBinary} {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid artifact digest %q", digest)
		}
	}
	if len(audit.ProtocolFindings) != 3 {
		t.Fatalf("protocol findings = %d", len(audit.ProtocolFindings))
	}
	for _, finding := range audit.ProtocolFindings {
		if finding.RunID == 0 || !hex40.MatchString(finding.HeadSHA) || !uuid.MatchString(finding.JobID) || finding.Boundary == "" || !finding.FailedClosed || !finding.RollbackComplete {
			t.Fatalf("invalid protocol finding: %#v", finding)
		}
	}
	if audit.ProtocolFindings[0].WorkerAssigned || audit.ProtocolFindings[1].WorkerAssigned {
		t.Fatal("pre-capacity failures assigned a worker")
	}
	if audit.ProtocolFindings[2].WorkflowConclusion != "success" {
		t.Fatal("mixed terminal boundary did not retain successful workflow execution")
	}
	reconcile := audit.Reconciliation
	if reconcile.RunID != 31325617638 || !uuid.MatchString(reconcile.JobID) || reconcile.GitHubConclusion != "success" || reconcile.ProviderClaimsBefore != 0 || reconcile.IncusInstancesBefore != 0 || !reconcile.WritersStopped || !reconcile.ExactKeyChecked || !reconcile.FlockHeld || !reconcile.TemporaryFileFsynced || !reconcile.DirectoryFsynced || reconcile.GenerationBefore != 2 || reconcile.GenerationAfter != 3 || reconcile.RemovedIntents != 1 || !reconcile.RepositoryStridePreserved {
		t.Fatalf("terminal reconciliation was not bounded: %#v", reconcile)
	}
	canary := audit.Canary
	if canary.RunID != 31326743839 || canary.JobID == 0 || canary.HeadSHA != audit.RepositoryMergeCommit || !uuid.MatchString(canary.OpaqueJobID) || canary.Conclusion != "success" || canary.Label != "nddev-linux-standard" || canary.RunnerName == "" || canary.CreatedToJobStartSeconds != 12 || canary.JobDurationSeconds != 13 || canary.CreatedAt.After(canary.StartedAt) || canary.StartedAt.After(canary.CompletedAt) || !canary.MixedStartedCompletedObserved || !canary.ImmutableBoundaryPassed || !canary.RustFSCacheDeliveryPassed || !canary.CompositeActionsPassed || !canary.CommandFilesPassed || !canary.ArtifactUploadPassed || !canary.PostActionPassed {
		t.Fatalf("accepted canary is incomplete: %#v", canary)
	}
	worker := audit.Worker
	if worker.InstanceName == "" || !worker.DestroyedAfterJob || worker.ReturnedToWarmPool || worker.DiagnosticsArchive == "" || !hex64.MatchString(worker.DiagnosticsSHA256) || worker.DiagnosticsBytes <= 0 || !worker.DiagnosticsExportedToRustFS {
		t.Fatalf("worker teardown is not proven: %#v", worker)
	}
	if audit.Replacement.InstanceName == "" || audit.Replacement.InstanceName == worker.InstanceName || audit.Replacement.CreatedAt.IsZero() || audit.Replacement.Lifecycle != "warm-unregistered" || !audit.Replacement.Ready || audit.Replacement.Claims != 0 {
		t.Fatalf("replacement is not proven: %#v", audit.Replacement)
	}
	post := audit.Postconditions
	if post.GARMSystemdRestarts != 0 || post.FailedUnits != 0 || !post.ObserverHealthy || !post.ObserverFresh || post.QueueSchemaVersion != 1 || post.QueueGeneration != 5 || post.QueueIntents != 0 || post.ProviderJournalSchemaVersion != 4 || post.ProviderClaims != 0 || post.IncusVisibleInstances != 1 || post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 || post.RootFreePercent < 20 || post.LegacyListeners != 12 || !post.ExamplePlatformContainersHealthy || !post.CaptchaContainersHealthy || !post.RustFSActive || !post.ZotActive {
		t.Fatalf("unhealthy fleet postconditions: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.CentralAdmissionOperational || !verdict.SparseIdentityOperational || !verdict.MixedTerminalConverges || !verdict.OneJobVMOperational || !verdict.WarmReplacementOperational || !verdict.SingleSampleGateComplete || verdict.ReliabilityGateComplete || verdict.RemainingGate == "" {
		t.Fatalf("audit verdict overclaims or omits work: %#v", verdict)
	}
}
