package deploycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestDirectJITNDDev9RolloutAudit(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "config", "direct-jit-nddev9-rollout-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion int    `json:"schema_version"`
		MergeCommit   string `json:"repository_merge_commit"`
		Artifacts     struct {
			GARMVersion       string `json:"garm_version"`
			GARMBinary        string `json:"garm_binary_sha256"`
			ProviderVersion   string `json:"provider_version"`
			ProviderBinary    string `json:"provider_binary_sha256"`
			ObserverBinary    string `json:"observer_binary_sha256"`
			Reproducible      bool   `json:"reproducible_binary_rebuild_equal"`
			UpstreamRaceSuite bool   `json:"upstream_race_suite_passed"`
		} `json:"artifacts"`
		Rollout struct {
			Attempts         int      `json:"attempts"`
			Rollbacks        int      `json:"automatic_rollbacks"`
			Success          int      `json:"successful_attempt"`
			BundleSHA256     []string `json:"rollback_bundle_sha256"`
			DatabaseRestored bool     `json:"database_restored_exactly_on_rollback"`
			FilesRestored    bool     `json:"binaries_and_config_restored_exactly_on_rollback"`
		} `json:"transactional_rollout"`
		Activation struct {
			ScaleSet        string   `json:"scale_set"`
			Mode            string   `json:"mode"`
			Actions         []string `json:"dry_run_actions"`
			MaxRunners      int      `json:"max_runners"`
			IntegrationMode string   `json:"integration_mode_unchanged"`
		} `json:"activation"`
		Rejected struct {
			RunID            int64  `json:"workflow_run_id"`
			Conclusion       string `json:"conclusion"`
			Steps            int    `json:"workflow_steps_executed"`
			StaleRejected    bool   `json:"stale_provider_commit_rejected"`
			AutomaticReap    bool   `json:"automatic_garm_reap_completed"`
			ProviderDelete   bool   `json:"provider_vm_delete_completed"`
			Diagnostics      bool   `json:"diagnostics_exported"`
			AcceptedAsCanary bool   `json:"accepted_as_canary"`
		} `json:"rejected_contaminated_attempt"`
		Canary struct {
			RunID                  int64             `json:"workflow_run_id"`
			JobID                  int64             `json:"job_id"`
			HeadSHA                string            `json:"head_sha"`
			Conclusion             string            `json:"conclusion"`
			ClaimedWarm            string            `json:"claimed_warm_instance"`
			ReplacementWarm        string            `json:"replacement_warm_instance"`
			QueueSeconds           int               `json:"created_to_job_start_seconds"`
			ExecutionSeconds       int               `json:"job_execution_seconds"`
			InvalidTransitions     int               `json:"garm_invalid_transition_errors"`
			GARMErrors             int               `json:"garm_warning_or_error_events"`
			Destroyed              bool              `json:"worker_destroyed_after_job"`
			ReturnedToWarm         bool              `json:"worker_returned_to_warm_pool"`
			RegistrationRemaining  bool              `json:"github_runner_registration_remaining"`
			DiagnosticSHA256       string            `json:"diagnostic_sha256"`
			DiagnosticTokenMatches int               `json:"diagnostic_token_shape_matches"`
			Semantics              map[string]string `json:"official_runner_semantics"`
		} `json:"accepted_canary"`
		Post struct {
			Healthy            bool     `json:"observer_healthy"`
			Fresh              bool     `json:"observer_fresh"`
			Claims             int      `json:"provider_claims"`
			QueueActive        int      `json:"queue_active"`
			QueueInFlight      int      `json:"queue_in_flight"`
			Orphans            int      `json:"incus_orphan_instances"`
			Missing            int      `json:"incus_missing_instances"`
			DiagnosticsSource  int      `json:"diagnostic_source_bundles"`
			DiagnosticsExport  int      `json:"diagnostic_exported_bundles"`
			DiagnosticsPending int      `json:"diagnostic_pending_bundles"`
			LegacyListeners    int      `json:"legacy_listeners"`
			ExamplePlatform    bool     `json:"example_platform_containers_healthy"`
			Captcha            bool     `json:"captcha_containers_healthy"`
			FailedUnits        int      `json:"failed_systemd_units"`
			Services           []string `json:"services_active"`
		} `json:"postconditions"`
		Verdict struct {
			CanaryAccepted      bool `json:"merge_bound_direct_jit_canary_accepted"`
			LifecycleAccepted   bool `json:"lifecycle_bridge_accepted"`
			RollbackAccepted    bool `json:"transactional_rollback_accepted"`
			P95Complete         bool `json:"warm_queue_to_online_p95_gate_complete"`
			ReliabilityComplete bool `json:"production_reliability_gate_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.MergeCommit != "cdf3e18b3528515266874ca11d6accec37944fab" {
		t.Fatalf("invalid rollout identity: schema=%d commit=%q", audit.SchemaVersion, audit.MergeCommit)
	}
	if audit.Artifacts.GARMVersion != "v0.2.1-nddev.9" || audit.Artifacts.ProviderVersion != "v0.1.5-nddev.18" ||
		!hex64.MatchString(audit.Artifacts.GARMBinary) || !hex64.MatchString(audit.Artifacts.ProviderBinary) ||
		!hex64.MatchString(audit.Artifacts.ObserverBinary) || !audit.Artifacts.Reproducible || !audit.Artifacts.UpstreamRaceSuite {
		t.Fatalf("invalid merge-bound artifacts: %#v", audit.Artifacts)
	}
	if audit.Rollout.Attempts != 3 || audit.Rollout.Rollbacks != 2 || audit.Rollout.Success != 3 ||
		len(audit.Rollout.BundleSHA256) != 3 || !audit.Rollout.DatabaseRestored || !audit.Rollout.FilesRestored {
		t.Fatalf("transactional rollout is incomplete: %#v", audit.Rollout)
	}
	for _, digest := range audit.Rollout.BundleSHA256 {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid rollback digest %q", digest)
		}
	}
	if audit.Activation.ScaleSet != "nddev-linux-standard" || audit.Activation.Mode != "direct-jit" ||
		audit.Activation.MaxRunners != 1 || audit.Activation.IntegrationMode != "metadata" ||
		!equalStrings(audit.Activation.Actions, []string{"disable_and_migrate_scale_set_activation", "enable_verified_scale_set"}) {
		t.Fatalf("invalid activation: %#v", audit.Activation)
	}
	if audit.Rejected.RunID != 31337075129 || audit.Rejected.Conclusion != "cancelled" || audit.Rejected.Steps != 0 ||
		!audit.Rejected.StaleRejected || !audit.Rejected.AutomaticReap || !audit.Rejected.ProviderDelete ||
		!audit.Rejected.Diagnostics || audit.Rejected.AcceptedAsCanary {
		t.Fatalf("contaminated attempt was not rejected cleanly: %#v", audit.Rejected)
	}
	if audit.Canary.RunID != 31337636346 || audit.Canary.JobID != 93305994280 || audit.Canary.HeadSHA != audit.MergeCommit ||
		audit.Canary.Conclusion != "success" || audit.Canary.ClaimedWarm == audit.Canary.ReplacementWarm ||
		audit.Canary.QueueSeconds != 11 || audit.Canary.ExecutionSeconds != 14 || audit.Canary.InvalidTransitions != 0 ||
		audit.Canary.GARMErrors != 0 || !audit.Canary.Destroyed || audit.Canary.ReturnedToWarm ||
		audit.Canary.RegistrationRemaining || !hex64.MatchString(audit.Canary.DiagnosticSHA256) || audit.Canary.DiagnosticTokenMatches != 0 ||
		len(audit.Canary.Semantics) != 7 {
		t.Fatalf("accepted canary is incomplete: %#v", audit.Canary)
	}
	for behavior, result := range audit.Canary.Semantics {
		if result != "passed" {
			t.Fatalf("official runner behavior %q = %q", behavior, result)
		}
	}
	if !audit.Post.Healthy || !audit.Post.Fresh || audit.Post.Claims != 0 || audit.Post.QueueActive != 0 ||
		audit.Post.QueueInFlight != 0 || audit.Post.Orphans != 0 || audit.Post.Missing != 0 ||
		audit.Post.DiagnosticsSource != audit.Post.DiagnosticsExport || audit.Post.DiagnosticsPending != 0 ||
		audit.Post.LegacyListeners != 12 || !audit.Post.ExamplePlatform || !audit.Post.Captcha || audit.Post.FailedUnits != 0 || len(audit.Post.Services) != 7 {
		t.Fatalf("fleet postconditions are incomplete: %#v", audit.Post)
	}
	if !audit.Verdict.CanaryAccepted || !audit.Verdict.LifecycleAccepted || !audit.Verdict.RollbackAccepted ||
		audit.Verdict.P95Complete || audit.Verdict.ReliabilityComplete {
		t.Fatalf("invalid evidence verdict: %#v", audit.Verdict)
	}
}
