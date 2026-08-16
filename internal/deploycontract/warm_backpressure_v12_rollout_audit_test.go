package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestWarmBackpressureV12RolloutEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/warm-backpressure-v12-rollout-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion  int    `json:"schema_version"`
		CapturedAt     string `json:"captured_at"`
		Host           string `json:"host"`
		Repository     string `json:"repository"`
		Implementation struct {
			PullRequest           int    `json:"pull_request"`
			ImplementationCommit  string `json:"implementation_commit"`
			MergeCommit           string `json:"merge_commit"`
			PostMergeCIRunID      int64  `json:"post_merge_ci_run_id"`
			PostMergeCIConclusion string `json:"post_merge_ci_conclusion"`
		} `json:"implementation"`
		Runtime struct {
			ProviderVersion          string `json:"provider_version"`
			ProviderCommit           string `json:"provider_commit"`
			ProviderBinarySHA256     string `json:"provider_binary_sha256"`
			ControllerBinarySHA256   string `json:"controller_binary_sha256"`
			ObserverBinarySHA256     string `json:"observer_binary_sha256"`
			PlatformConfigSHA256     string `json:"platform_config_sha256"`
			PlatformFingerprint      string `json:"platform_fingerprint"`
			StandardImageFingerprint string `json:"standard_image_fingerprint"`
			StagingDirectoryRemoved  bool   `json:"staging_directory_removed"`
		} `json:"installed_runtime"`
		Rollback struct {
			Path                   string `json:"path"`
			ProviderVersion        string `json:"provider_version"`
			ProviderCommit         string `json:"provider_commit"`
			ProviderBinarySHA256   string `json:"provider_binary_sha256"`
			ControllerBinarySHA256 string `json:"controller_binary_sha256"`
			ObserverBinarySHA256   string `json:"observer_binary_sha256"`
			PlatformConfigSHA256   string `json:"platform_config_sha256"`
			Retained               bool   `json:"retained"`
		} `json:"rollback"`
		Drain struct {
			Instance          string `json:"instance"`
			ProviderVersion   string `json:"provider_version"`
			Lifecycle         string `json:"lifecycle"`
			WarmReady         bool   `json:"warm_ready"`
			ClaimsBefore      int    `json:"claims_before"`
			Deleted           bool   `json:"deleted"`
			DiagnosticArchive string `json:"diagnostic_archive"`
			DiagnosticSHA256  string `json:"diagnostic_sha256"`
		} `json:"pre_rollout_drain"`
		Canary struct {
			RunID                     int64  `json:"run_id"`
			JobID                     int64  `json:"job_id"`
			HeadSHA                   string `json:"head_sha"`
			Conclusion                string `json:"conclusion"`
			JobDurationSeconds        int    `json:"job_duration_seconds"`
			ExecutedInstance          string `json:"executed_instance"`
			ExecutedInstanceDestroyed bool   `json:"executed_instance_destroyed"`
			ExecutedInstanceReturned  bool   `json:"executed_instance_returned_to_pool"`
			DiagnosticArchive         string `json:"diagnostic_archive"`
			DiagnosticSHA256          string `json:"diagnostic_sha256"`
			ArtifactID                int64  `json:"artifact_id"`
			ArtifactResultSHA256      string `json:"artifact_result_sha256"`
			MeasurementElapsedNS      int64  `json:"measurement_elapsed_ns"`
			CacheHits                 int    `json:"cache_hits"`
			CacheMisses               int    `json:"cache_misses"`
			CacheErrors               int    `json:"cache_errors"`
			GitHubLogBytesScanned     int    `json:"github_log_bytes_scanned"`
			GitHubLogSecretFindings   int    `json:"github_log_secret_findings"`
			NonSelectedJobsSkipped    int    `json:"non_selected_jobs_skipped"`
		} `json:"backpressure_canary"`
		DeferredEvents []struct {
			ObservedAt         string `json:"observed_at"`
			Source             string `json:"source"`
			Reason             string `json:"reason"`
			Claimed            int    `json:"claimed"`
			ReadyAfter         int    `json:"ready_after"`
			RemainingCPUUnits  int    `json:"remaining_cpu_units"`
			RemainingMemoryMiB int    `json:"remaining_memory_mib"`
			ExitCode           int    `json:"exit_code"`
			SystemdResult      string `json:"systemd_result"`
			InstanceCreated    bool   `json:"instance_created"`
		} `json:"deferred_events"`
		HostUnhealthy struct {
			Finding                    string `json:"finding"`
			RequiredAvailableMemoryMiB int    `json:"required_available_memory_mib"`
			ObservedAvailableMemoryMiB int    `json:"observed_available_memory_mib"`
			ShortfallMiB               int    `json:"shortfall_mib"`
			ObserverHealthy            bool   `json:"observer_healthy_during_finding"`
		} `json:"host_unhealthy_explanation"`
		Postconditions struct {
			BootID                    string `json:"boot_id"`
			ObserverCapturedAt        string `json:"observer_captured_at"`
			ObserverHealthy           bool   `json:"observer_healthy"`
			ObserverFresh             bool   `json:"observer_fresh"`
			ObserverCollectionErrors  int    `json:"observer_collection_errors"`
			FailedUnits               int    `json:"failed_units"`
			GARMActive                bool   `json:"garm_active"`
			GatewayActive             bool   `json:"gateway_active"`
			ObserverActive            bool   `json:"observer_active"`
			RustFSActive              bool   `json:"rustfs_active"`
			ZotActive                 bool   `json:"zot_active"`
			WarmTimerActive           bool   `json:"warm_timer_active"`
			DiagnosticTimerActive     bool   `json:"diagnostic_timer_active"`
			ReplacementInstance       string `json:"replacement_instance"`
			ReplacementProvider       string `json:"replacement_provider_version"`
			ReplacementCommit         string `json:"replacement_provider_commit"`
			ReplacementImage          string `json:"replacement_image_fingerprint"`
			ReplacementWarmReady      bool   `json:"replacement_warm_ready"`
			IncusVisibleInstances     int    `json:"incus_visible_instances"`
			JournalLeases             int    `json:"journal_leases"`
			JournalClaims             int    `json:"journal_claims"`
			IncusOrphans              int    `json:"incus_orphans"`
			IncusMissingInstances     int    `json:"incus_missing_instances"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			DiagnosticExportFailures  int    `json:"diagnostic_export_failures"`
			LegacyListeners           int    `json:"legacy_listeners"`
			ExamplePlatformHealthy    bool   `json:"example_platform_healthy"`
			ExamplePlatformHTTPStatus int    `json:"example_platform_http_status"`
			CaptchaHealthy            bool   `json:"captcha_healthy"`
			CaptchaHTTPStatus         int    `json:"captcha_http_status"`
			RootUsedPercent           int    `json:"root_used_percent"`
		} `json:"postconditions"`
		Verdict struct {
			BackpressureComplete  bool `json:"warm_backpressure_semantics_complete"`
			RolloutComplete       bool `json:"merge_bound_rollout_complete"`
			LifecycleComplete     bool `json:"one_job_lifecycle_complete"`
			CachePassed           bool `json:"cache_regression_passed"`
			SystemdPreserved      bool `json:"systemd_health_preserved"`
			RollbackRetained      bool `json:"rollback_retained"`
			LegacyPreserved       bool `json:"legacy_runners_preserved"`
			ApplicationsPreserved bool `json:"retained_applications_preserved"`
			FairnessComplete      bool `json:"resource_fairness_complete"`
			StarvationResolved    bool `json:"integration_starvation_resolved"`
			StatisticalComplete   bool `json:"statistical_cache_gate_complete"`
			ReliabilityComplete   bool `json:"production_reliability_gate_complete"`
			HAComplete            bool `json:"high_availability_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || audit.Repository != "example-user/github-actions" {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	if _, err := time.Parse(time.RFC3339, audit.CapturedAt); err != nil {
		t.Fatalf("invalid capture time: %v", err)
	}
	implementation := audit.Implementation
	if implementation.PullRequest != 118 || !hex40.MatchString(implementation.ImplementationCommit) ||
		!hex40.MatchString(implementation.MergeCommit) || implementation.PostMergeCIRunID != 31311715607 ||
		implementation.PostMergeCIConclusion != "success" {
		t.Fatalf("invalid implementation evidence: %#v", implementation)
	}
	runtime := audit.Runtime
	for _, digest := range []string{runtime.ProviderBinarySHA256, runtime.ControllerBinarySHA256, runtime.ObserverBinarySHA256,
		runtime.PlatformConfigSHA256, runtime.PlatformFingerprint, runtime.StandardImageFingerprint} {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid runtime digest %q", digest)
		}
	}
	if runtime.ProviderVersion != "v0.1.5-nddev.12" || runtime.ProviderCommit != implementation.MergeCommit || !runtime.StagingDirectoryRemoved {
		t.Fatalf("runtime is not merge-bound: %#v", runtime)
	}
	rollback := audit.Rollback
	for _, digest := range []string{rollback.ProviderBinarySHA256, rollback.ControllerBinarySHA256, rollback.ObserverBinarySHA256, rollback.PlatformConfigSHA256} {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid rollback digest %q", digest)
		}
	}
	if rollback.Path != "/var/lib/gha-fleet/rollback/v0.1.5-nddev.11-48e1a3c" || rollback.ProviderVersion != "v0.1.5-nddev.11" ||
		!hex40.MatchString(rollback.ProviderCommit) || rollback.ProviderCommit == runtime.ProviderCommit || !rollback.Retained {
		t.Fatalf("rollback is incomplete: %#v", rollback)
	}
	drain := audit.Drain
	if drain.Instance == "" || drain.ProviderVersion != rollback.ProviderVersion || drain.Lifecycle != "warm-unregistered" || !drain.WarmReady ||
		drain.ClaimsBefore != 0 || !drain.Deleted || drain.DiagnosticArchive == "" || !hex64.MatchString(drain.DiagnosticSHA256) {
		t.Fatalf("pre-rollout drain is unsafe: %#v", drain)
	}
	canary := audit.Canary
	if canary.RunID != 31311977563 || canary.JobID != 93241050075 || canary.HeadSHA != implementation.MergeCommit ||
		canary.Conclusion != "success" || canary.JobDurationSeconds != 48 || canary.ExecutedInstance == "" ||
		!canary.ExecutedInstanceDestroyed || canary.ExecutedInstanceReturned || canary.DiagnosticArchive == "" ||
		!hex64.MatchString(canary.DiagnosticSHA256) || canary.ArtifactID != 9037638029 || !hex64.MatchString(canary.ArtifactResultSHA256) ||
		canary.MeasurementElapsedNS <= 0 || canary.CacheHits != 57 || canary.CacheMisses != 0 || canary.CacheErrors != 0 ||
		canary.GitHubLogBytesScanned <= 0 || canary.GitHubLogSecretFindings != 0 || canary.NonSelectedJobsSkipped != 4 {
		t.Fatalf("invalid backpressure canary: %#v", canary)
	}
	if len(audit.DeferredEvents) != 2 || audit.DeferredEvents[0].Reason != "pool-saturated" || audit.DeferredEvents[1].Reason != "host-unhealthy" {
		t.Fatalf("missing exact deferred sequence: %#v", audit.DeferredEvents)
	}
	for _, event := range audit.DeferredEvents {
		if _, err := time.Parse(time.RFC3339, event.ObservedAt); err != nil || event.Source != "gha-warm-pool.service" ||
			event.Claimed != 1 || event.ReadyAfter != 0 || event.RemainingCPUUnits != 0 || event.RemainingMemoryMiB <= 0 ||
			event.ExitCode != 0 || event.SystemdResult != "success" || event.InstanceCreated {
			t.Fatalf("invalid deferred event: %#v", event)
		}
	}
	host := audit.HostUnhealthy
	if host.Finding != "insufficient-available-memory" || host.RequiredAvailableMemoryMiB-host.ObservedAvailableMemoryMiB != host.ShortfallMiB ||
		host.ShortfallMiB != 409 || !host.ObserverHealthy {
		t.Fatalf("host-unhealthy reason is not explained: %#v", host)
	}
	post := audit.Postconditions
	if !uuid.MatchString(post.BootID) {
		t.Fatalf("invalid boot ID %q", post.BootID)
	}
	if _, err := time.Parse(time.RFC3339Nano, post.ObserverCapturedAt); err != nil {
		t.Fatalf("invalid observer time: %v", err)
	}
	if !post.ObserverHealthy || !post.ObserverFresh || post.ObserverCollectionErrors != 0 || post.FailedUnits != 0 ||
		!post.GARMActive || !post.GatewayActive || !post.ObserverActive || !post.RustFSActive || !post.ZotActive ||
		!post.WarmTimerActive || !post.DiagnosticTimerActive || post.ReplacementInstance == "" || post.ReplacementInstance == canary.ExecutedInstance ||
		post.ReplacementProvider != runtime.ProviderVersion || post.ReplacementCommit != runtime.ProviderCommit ||
		post.ReplacementImage != runtime.StandardImageFingerprint || !post.ReplacementWarmReady || post.IncusVisibleInstances != 1 ||
		post.JournalLeases != 1 || post.JournalClaims != 0 || post.IncusOrphans != 0 || post.IncusMissingInstances != 0 ||
		post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 || post.DiagnosticExportFailures != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatformHealthy || post.ExamplePlatformHTTPStatus != 200 || !post.CaptchaHealthy ||
		post.CaptchaHTTPStatus != 200 || post.RootUsedPercent >= 80 {
		t.Fatalf("unsafe postconditions: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.BackpressureComplete || !verdict.RolloutComplete || !verdict.LifecycleComplete || !verdict.CachePassed ||
		!verdict.SystemdPreserved || !verdict.RollbackRetained || !verdict.LegacyPreserved || !verdict.ApplicationsPreserved ||
		verdict.FairnessComplete || verdict.StarvationResolved || verdict.StatisticalComplete || verdict.ReliabilityComplete || verdict.HAComplete {
		t.Fatalf("verdict overclaims or omits completed evidence: %#v", verdict)
	}
}
