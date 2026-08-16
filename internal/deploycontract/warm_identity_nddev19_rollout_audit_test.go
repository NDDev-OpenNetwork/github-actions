package deploycontract

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWarmIdentityNDDev19RolloutAudit(t *testing.T) {
	data, err := os.ReadFile("../../config/warm-identity-nddev19-rollout-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion  int    `json:"schema_version"`
		Implementation string `json:"implementation_merge_commit"`
		WorkflowMerge  string `json:"canary_workflow_merge_commit"`
		Finding        struct {
			RunID             int64     `json:"run_id"`
			Conclusion        string    `json:"conclusion"`
			LogicalRunner     string    `json:"logical_runner"`
			PhysicalInstance  string    `json:"physical_instance"`
			AssignedAt        time.Time `json:"garm_assigned_at"`
			SessionAt         time.Time `json:"runner_session_at"`
			UploadAt          time.Time `json:"worker_artifact_upload_observed_at"`
			DeletedAt         time.Time `json:"provider_deleted_at"`
			DeleteRejectedAt  time.Time `json:"github_delete_rejected_at"`
			JobStartedEventAt time.Time `json:"garm_job_started_event_at"`
			FailureClass      string    `json:"failure_class"`
			RunnerExecuting   bool      `json:"official_runner_was_executing"`
			Recovered         bool      `json:"queue_recovered_without_manual_journal_edit"`
		} `json:"finding"`
		Artifacts struct {
			ProviderVersion string `json:"provider_version"`
			ProviderCommit  string `json:"provider_commit"`
			ProviderSHA     string `json:"provider_sha256"`
			ObserverVersion string `json:"observer_version"`
			ObserverSchema  int    `json:"observer_schema"`
			ObserverCommit  string `json:"observer_commit"`
			ObserverSHA     string `json:"observer_sha256"`
			PlatformSHA     string `json:"platform_sha256"`
			IncusSDK        string `json:"incus_sdk_version"`
		} `json:"deployed_artifacts"`
		Rollout struct {
			Directory        string `json:"rollback_directory"`
			RollbackProvider string `json:"rollback_provider_sha256"`
			RollbackObserver string `json:"rollback_observer_sha256"`
			RollbackPlatform string `json:"rollback_platform_sha256"`
			OldWarm          string `json:"old_warm_instance"`
			NewWarm          string `json:"new_warm_instance"`
			OldDiagnostics   bool   `json:"old_warm_diagnostics_captured"`
			RollbackArmed    bool   `json:"transactional_automatic_rollback_armed"`
			RollbackUsed     bool   `json:"rollback_used"`
		} `json:"rollout"`
		Canary struct {
			RunID              int64     `json:"run_id"`
			JobID              int64     `json:"job_id"`
			HeadSHA            string    `json:"head_sha"`
			Conclusion         string    `json:"conclusion"`
			JobStartedAt       time.Time `json:"job_started_at"`
			JobCompletedAt     time.Time `json:"job_completed_at"`
			AssignmentAt       time.Time `json:"garm_assignment_at"`
			SessionAt          time.Time `json:"runner_session_at"`
			JobStartedEventAt  time.Time `json:"garm_job_started_event_at"`
			LogicalRunner      string    `json:"logical_runner"`
			PhysicalInstance   string    `json:"physical_instance"`
			HoldStartedAt      time.Time `json:"hold_started_at"`
			HoldCompletedAt    time.Time `json:"hold_completed_at"`
			HoldSeconds        int       `json:"hold_seconds"`
			ArtifactDigest     string    `json:"artifact_digest"`
			PostAction         bool      `json:"post_action_completed"`
			ClaimResolved      bool      `json:"claim_resolved_to_exact_physical_instance"`
			MetadataMatched    bool      `json:"provider_metadata_commit_matched"`
			Anomalies          int       `json:"garm_reconciliation_anomalies"`
			InvalidTransitions int       `json:"garm_invalid_transitions"`
			DiagnosticSHA      string    `json:"diagnostic_sha256"`
			TokenMatches       int       `json:"diagnostic_token_shape_matches"`
			Observations       []struct {
				CapturedAt       time.Time `json:"captured_at"`
				QueueState       string    `json:"queue_state"`
				UncoveredRunning int       `json:"queue_uncovered_running"`
				ExecutionLeases  int       `json:"journal_execution_leases"`
				Claims           int       `json:"journal_claims"`
				Visible          int       `json:"visible_instances"`
				Healthy          bool      `json:"healthy"`
			} `json:"identity_observations"`
		} `json:"reconciliation_canary"`
		Post struct {
			Healthy             bool   `json:"healthy"`
			QueueActive         int    `json:"queue_active"`
			QueueInFlight       int    `json:"queue_in_flight"`
			UncoveredRunning    int    `json:"queue_uncovered_running"`
			Claims              int    `json:"claims"`
			WarmReady           int    `json:"warm_ready"`
			Visible             int    `json:"visible_instances"`
			Orphans             int    `json:"orphan_instances"`
			Missing             int    `json:"missing_instances"`
			DiagnosticsSource   int    `json:"diagnostics_source"`
			DiagnosticsExported int    `json:"diagnostics_exported"`
			DiagnosticsPending  int    `json:"diagnostics_pending"`
			Replacement         string `json:"replacement_warm_instance"`
			Registrations       int    `json:"github_runner_registrations"`
			FailedUnits         int    `json:"failed_systemd_units"`
			GARMRestarts        int    `json:"garm_restarts"`
			ObserverRestarts    int    `json:"observer_restarts"`
			LegacyListeners     int    `json:"legacy_runner_listeners"`
			ExamplePlatform     bool   `json:"example_platform_healthy"`
			Captcha             bool   `json:"captcha_healthy"`
			GARM                bool   `json:"garm_active"`
			Gateway             bool   `json:"gateway_active"`
			Observer            bool   `json:"observer_active"`
			WarmTimer           bool   `json:"warm_timer_active"`
			RustFS              bool   `json:"rustfs_active"`
			Zot                 bool   `json:"zot_active"`
		} `json:"postconditions"`
	}
	if err := json.Unmarshal(data, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Implementation != "531c1c774fc071e534971bda9cadd46ceae39e26" || audit.WorkflowMerge != "8acb7044e71ec447da6a61dd7dd57bc9dd3d16bf" {
		t.Fatalf("audit provenance is invalid: %#v", audit)
	}
	if audit.Finding.RunID != 31341001674 || audit.Finding.Conclusion != "cancelled" ||
		audit.Finding.LogicalRunner == audit.Finding.PhysicalInstance || audit.Finding.FailureClass != "physical-logical-provider-identity-mismatch" ||
		!audit.Finding.RunnerExecuting || !audit.Finding.Recovered || !audit.Finding.SessionAt.Before(audit.Finding.UploadAt) ||
		!audit.Finding.UploadAt.Before(audit.Finding.DeleteRejectedAt) || !audit.Finding.DeletedAt.Before(audit.Finding.JobStartedEventAt) {
		t.Fatalf("finding chronology or classification is invalid: %#v", audit.Finding)
	}
	for label, digest := range map[string]string{
		"provider": audit.Artifacts.ProviderSHA, "observer": audit.Artifacts.ObserverSHA,
		"platform": audit.Artifacts.PlatformSHA, "rollback provider": audit.Rollout.RollbackProvider,
		"rollback observer": audit.Rollout.RollbackObserver, "rollback platform": audit.Rollout.RollbackPlatform,
		"diagnostic": audit.Canary.DiagnosticSHA,
	} {
		if len(digest) != 64 || strings.Trim(digest, "0123456789abcdef") != "" {
			t.Fatalf("%s digest is invalid: %q", label, digest)
		}
	}
	if audit.Artifacts.ProviderVersion != "v0.1.5-nddev.19" || audit.Artifacts.ObserverVersion != "v0.6.0" || audit.Artifacts.ObserverSchema != 6 ||
		audit.Artifacts.ProviderCommit != audit.Implementation || audit.Artifacts.ObserverCommit != audit.Implementation || audit.Artifacts.IncusSDK != "v7.3.0" {
		t.Fatalf("deployed artifacts are not merge-bound: %#v", audit.Artifacts)
	}
	if audit.Rollout.Directory != "/root/gha-deploy-531c1c7" || audit.Rollout.OldWarm == audit.Rollout.NewWarm ||
		!audit.Rollout.OldDiagnostics || !audit.Rollout.RollbackArmed || audit.Rollout.RollbackUsed {
		t.Fatalf("rollout or rollback evidence is invalid: %#v", audit.Rollout)
	}
	if audit.Canary.RunID != 31342261697 || audit.Canary.JobID != 93317872151 || audit.Canary.HeadSHA != audit.WorkflowMerge || audit.Canary.Conclusion != "success" ||
		audit.Canary.LogicalRunner == audit.Canary.PhysicalInstance || audit.Canary.HoldSeconds != 45 || !audit.Canary.PostAction || !audit.Canary.ClaimResolved ||
		!audit.Canary.MetadataMatched || audit.Canary.Anomalies != 0 || audit.Canary.InvalidTransitions != 0 || audit.Canary.TokenMatches != 0 ||
		!audit.Canary.AssignmentAt.Before(audit.Canary.SessionAt) || !audit.Canary.SessionAt.Before(audit.Canary.JobStartedEventAt) ||
		audit.Canary.HoldCompletedAt.Sub(audit.Canary.HoldStartedAt) != 45*time.Second || !audit.Canary.JobCompletedAt.After(audit.Canary.HoldCompletedAt) ||
		!strings.HasPrefix(audit.Canary.ArtifactDigest, "sha256:") {
		t.Fatalf("reconciliation canary is invalid: %#v", audit.Canary)
	}
	if len(audit.Canary.Observations) < 3 {
		t.Fatalf("too few in-job identity observations: %d", len(audit.Canary.Observations))
	}
	foundRunning := false
	for _, observation := range audit.Canary.Observations {
		if !observation.Healthy || observation.UncoveredRunning != 0 || observation.ExecutionLeases != 1 || observation.Claims != 1 || observation.Visible != 1 {
			t.Fatalf("identity observation is inconsistent: %#v", observation)
		}
		foundRunning = foundRunning || observation.QueueState == "running"
	}
	if !foundRunning {
		t.Fatal("no observation covered the authoritative running state")
	}
	if !audit.Post.Healthy || audit.Post.QueueActive != 0 || audit.Post.QueueInFlight != 0 || audit.Post.UncoveredRunning != 0 ||
		audit.Post.Claims != 0 || audit.Post.WarmReady != 1 || audit.Post.Visible != 1 || audit.Post.Orphans != 0 || audit.Post.Missing != 0 ||
		audit.Post.DiagnosticsSource != 129 || audit.Post.DiagnosticsExported != 129 || audit.Post.DiagnosticsPending != 0 ||
		audit.Post.Replacement == audit.Canary.PhysicalInstance || audit.Post.Registrations != 0 || audit.Post.FailedUnits != 0 ||
		audit.Post.GARMRestarts != 0 || audit.Post.ObserverRestarts != 0 || audit.Post.LegacyListeners != 12 ||
		!audit.Post.ExamplePlatform || !audit.Post.Captcha || !audit.Post.GARM || !audit.Post.Gateway || !audit.Post.Observer || !audit.Post.WarmTimer || !audit.Post.RustFS || !audit.Post.Zot {
		t.Fatalf("postconditions are not converged: %#v", audit.Post)
	}
}
