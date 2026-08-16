package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

type sccacheCanaryRun struct {
	RunID                       int64   `json:"run_id"`
	JobID                       int64   `json:"job_id"`
	HeadSHA                     string  `json:"head_sha"`
	Conclusion                  string  `json:"conclusion"`
	JobDurationSeconds          int64   `json:"job_duration_seconds"`
	MeasurementElapsedNS        int64   `json:"measurement_elapsed_ns"`
	CacheHits                   int     `json:"cache_hits"`
	CacheMisses                 int     `json:"cache_misses"`
	CacheHitRate                float64 `json:"cache_hit_rate_percent"`
	CacheReadErrors             int     `json:"cache_read_errors"`
	CacheWriteErrors            int     `json:"cache_write_errors"`
	CacheErrors                 int     `json:"cache_errors"`
	AverageWriteSeconds         float64 `json:"average_write_seconds"`
	AverageReadHitSeconds       float64 `json:"average_read_hit_seconds"`
	ArtifactID                  int64   `json:"artifact_id"`
	ArtifactResultSHA256        string  `json:"artifact_result_sha256"`
	NonSelectedJobsSkipped      int     `json:"non_selected_jobs_skipped"`
	GitHubLogBytesScanned       int64   `json:"github_log_bytes_scanned"`
	GitHubLogSecretFindings     int     `json:"github_log_secret_findings"`
	MeasurementReductionPercent float64 `json:"measurement_reduction_percent"`
}

func TestSccacheRuntimeCanaryEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/sccache-runtime-canary-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion  int    `json:"schema_version"`
		CapturedAt     string `json:"captured_at"`
		Host           string `json:"host"`
		Repository     string `json:"repository"`
		Implementation struct {
			ImageMerge      string `json:"image_merge_commit"`
			SmokeFixMerge   string `json:"smoke_fix_merge_commit"`
			ActivationMerge string `json:"activation_merge_commit"`
			SelectorMerge   string `json:"selector_merge_commit"`
			PullRequests    []int  `json:"pull_requests"`
		} `json:"implementation"`
		Runtime struct {
			ProviderVersion        string `json:"provider_version"`
			ProviderCommit         string `json:"provider_commit"`
			ProviderBinarySHA256   string `json:"provider_binary_sha256"`
			ControllerBinarySHA256 string `json:"controller_binary_sha256"`
			ObserverBinarySHA256   string `json:"observer_binary_sha256"`
			ProviderConfigSHA256   string `json:"provider_config_sha256"`
			StandardAlias          string `json:"standard_image_alias"`
			StandardFingerprint    string `json:"standard_image_fingerprint"`
			StandardPrevious       string `json:"standard_previous_fingerprint"`
			IntegrationAlias       string `json:"integration_image_alias"`
			IntegrationFingerprint string `json:"integration_image_fingerprint"`
			IntegrationPrevious    string `json:"integration_previous_fingerprint"`
			RunnerVersion          string `json:"runner_version"`
			SccacheVersion         string `json:"sccache_version"`
			SccacheBinarySHA256    string `json:"sccache_binary_sha256"`
		} `json:"runtime"`
		Namespace struct {
			Role                 string `json:"role"`
			Mode                 string `json:"mode"`
			Bucket               string `json:"bucket"`
			Prefix               string `json:"prefix"`
			PrefixSHA256         string `json:"prefix_sha256"`
			Toolchain            string `json:"toolchain"`
			DependencyLockSHA256 string `json:"dependency_lock_sha256"`
			Platform             string `json:"platform"`
			Architecture         string `json:"architecture"`
			RefClass             string `json:"ref_class"`
		} `json:"namespace"`
		Prime     sccacheCanaryRun `json:"prime"`
		WarmHit   sccacheCanaryRun `json:"warm_hit"`
		Lifecycle struct {
			ClaimedInstance             string `json:"claimed_instance"`
			ClaimedDestroyed            bool   `json:"claimed_instance_destroyed"`
			ClaimedReturned             bool   `json:"claimed_instance_returned_to_pool"`
			DiagnosticArchive           string `json:"diagnostic_archive"`
			DiagnosticSHA256            string `json:"diagnostic_sha256"`
			ReplacementInstance         string `json:"replacement_instance"`
			ReplacementImageFingerprint string `json:"replacement_image_fingerprint"`
			ReplacementWarmReady        bool   `json:"replacement_warm_ready"`
			ReplacementClaims           int    `json:"replacement_claims"`
		} `json:"one_job_lifecycle"`
		Backpressure struct {
			PoolSaturatedFailures    int  `json:"pool_saturated_failures"`
			HostUnhealthyFailures    int  `json:"host_unhealthy_cascade_failures"`
			TransientFailureObserved bool `json:"transient_systemd_failure_observed"`
			AutomaticRecovery        bool `json:"automatic_recovery_complete"`
			SemanticFixRequired      bool `json:"semantic_fix_required"`
		} `json:"backpressure_observation"`
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
			IncusVisibleInstances     int    `json:"incus_visible_instances"`
			JournalLeases             int    `json:"journal_leases"`
			JournalWarmReady          int    `json:"journal_warm_ready"`
			JournalClaims             int    `json:"journal_claims"`
			IncusOrphans              int    `json:"incus_orphans"`
			IncusMissingInstances     int    `json:"incus_missing_instances"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			DiagnosticExportFailures  int    `json:"diagnostic_export_failures"`
			LegacyListeners           int    `json:"legacy_listeners"`
			ExamplePlatformHealthy            bool   `json:"example_platform_healthy"`
			ExamplePlatformHTTPStatus         int    `json:"example_platform_http_status"`
			CaptchaHealthy            bool   `json:"captcha_healthy"`
			CaptchaHTTPStatus         int    `json:"captcha_http_status"`
			RootUsedPercent           int    `json:"root_used_percent"`
		} `json:"postconditions"`
		Verdict struct {
			PinnedActivationComplete bool `json:"pinned_sccache_activation_complete"`
			PrimeComplete            bool `json:"rustfs_prime_complete"`
			CacheHitComplete         bool `json:"rustfs_cache_hit_complete"`
			RustIntegrationComplete  bool `json:"rust_tool_native_cache_integration_complete"`
			CleanupComplete          bool `json:"one_job_cleanup_complete"`
			LegacyPreserved          bool `json:"legacy_runners_preserved"`
			ApplicationsPreserved    bool `json:"retained_applications_preserved"`
			BackpressureComplete     bool `json:"warm_backpressure_semantics_complete"`
			StatisticalComplete      bool `json:"statistical_cache_gate_complete"`
			RustFSPromotionComplete  bool `json:"rustfs_production_promotion_complete"`
			ReliabilityComplete      bool `json:"production_reliability_gate_complete"`
			HAComplete               bool `json:"high_availability_complete"`
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
	commits := []string{audit.Implementation.ImageMerge, audit.Implementation.SmokeFixMerge, audit.Implementation.ActivationMerge, audit.Implementation.SelectorMerge}
	if len(audit.Implementation.PullRequests) != len(commits) {
		t.Fatalf("incomplete implementation chain: %#v", audit.Implementation)
	}
	for index, commit := range commits {
		if !hex40.MatchString(commit) || audit.Implementation.PullRequests[index] != 113+index {
			t.Fatalf("invalid implementation chain: %#v", audit.Implementation)
		}
	}
	runtime := audit.Runtime
	for _, digest := range []string{runtime.ProviderBinarySHA256, runtime.ControllerBinarySHA256, runtime.ObserverBinarySHA256,
		runtime.ProviderConfigSHA256, runtime.StandardFingerprint, runtime.StandardPrevious, runtime.IntegrationFingerprint,
		runtime.IntegrationPrevious, runtime.SccacheBinarySHA256} {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid runtime digest %q", digest)
		}
	}
	if runtime.ProviderVersion != "v0.1.5-nddev.11" || runtime.ProviderCommit != audit.Implementation.ActivationMerge ||
		runtime.StandardAlias != "nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b7" ||
		runtime.IntegrationAlias != "nddev-ubuntu-24.04-amd64-docker-runner-2.336.0-r20260801-b3" ||
		runtime.StandardFingerprint == runtime.StandardPrevious || runtime.IntegrationFingerprint == runtime.IntegrationPrevious ||
		runtime.RunnerVersion != "2.336.0" || runtime.SccacheVersion != "0.17.0" {
		t.Fatalf("runtime activation is incomplete: %#v", runtime)
	}
	namespace := audit.Namespace
	if namespace.Role != "trusted-writer" || namespace.Mode != "read-write" || namespace.Bucket != "github-actions-cache" ||
		namespace.Prefix != "example-user/github-actions/trust/trusted/linux/amd64/rustc-1.97.1/"+namespace.DependencyLockSHA256+"/benchmark" ||
		!hex64.MatchString(namespace.PrefixSHA256) || !hex64.MatchString(namespace.DependencyLockSHA256) ||
		namespace.Toolchain == "" || namespace.Platform != "linux" || namespace.Architecture != "amd64" || namespace.RefClass != "benchmark" {
		t.Fatalf("invalid cache namespace: %#v", namespace)
	}
	validateSccacheRun(t, "prime", audit.Prime, hex40, hex64)
	validateSccacheRun(t, "warm-hit", audit.WarmHit, hex40, hex64)
	if audit.Prime.RunID != 31309941051 || audit.Prime.JobID != 93236073162 || audit.Prime.HeadSHA != audit.Implementation.ActivationMerge ||
		audit.Prime.CacheHits != 0 || audit.Prime.CacheMisses != 57 || audit.Prime.CacheHitRate != 0 || audit.Prime.AverageWriteSeconds <= 0 {
		t.Fatalf("prime did not populate the exact namespace: %#v", audit.Prime)
	}
	if audit.WarmHit.RunID != 31310848441 || audit.WarmHit.JobID != 93238342317 || audit.WarmHit.HeadSHA != audit.Implementation.SelectorMerge ||
		audit.WarmHit.CacheHits != audit.Prime.CacheMisses || audit.WarmHit.CacheMisses != 0 || audit.WarmHit.CacheHitRate != 100 ||
		audit.WarmHit.AverageReadHitSeconds <= 0 || audit.WarmHit.NonSelectedJobsSkipped != 4 ||
		audit.WarmHit.MeasurementElapsedNS >= audit.Prime.MeasurementElapsedNS || audit.WarmHit.MeasurementReductionPercent <= 0 {
		t.Fatalf("warm run did not prove exact cache reuse: %#v", audit.WarmHit)
	}
	lifecycle := audit.Lifecycle
	if lifecycle.ClaimedInstance == "" || !lifecycle.ClaimedDestroyed || lifecycle.ClaimedReturned ||
		lifecycle.DiagnosticArchive == "" || !hex64.MatchString(lifecycle.DiagnosticSHA256) || lifecycle.ReplacementInstance == "" ||
		lifecycle.ReplacementInstance == lifecycle.ClaimedInstance || lifecycle.ReplacementImageFingerprint != runtime.StandardFingerprint ||
		!lifecycle.ReplacementWarmReady || lifecycle.ReplacementClaims != 0 {
		t.Fatalf("one-job lifecycle is incomplete: %#v", lifecycle)
	}
	backpressure := audit.Backpressure
	if backpressure.PoolSaturatedFailures != 1 || backpressure.HostUnhealthyFailures != 1 || !backpressure.TransientFailureObserved ||
		!backpressure.AutomaticRecovery || !backpressure.SemanticFixRequired {
		t.Fatalf("backpressure defect is not honestly recorded: %#v", backpressure)
	}
	post := audit.Postconditions
	if !uuid.MatchString(post.BootID) {
		t.Fatalf("invalid boot ID: %q", post.BootID)
	}
	if _, err := time.Parse(time.RFC3339Nano, post.ObserverCapturedAt); err != nil {
		t.Fatalf("invalid observer capture time: %v", err)
	}
	if !post.ObserverHealthy || !post.ObserverFresh || post.ObserverCollectionErrors != 0 || post.FailedUnits != 0 ||
		!post.GARMActive || !post.GatewayActive || !post.ObserverActive || !post.RustFSActive || !post.ZotActive ||
		!post.WarmTimerActive || !post.DiagnosticTimerActive || post.IncusVisibleInstances != 1 || post.JournalLeases != 1 ||
		post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusOrphans != 0 || post.IncusMissingInstances != 0 ||
		post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 || post.DiagnosticExportFailures != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatformHealthy || post.ExamplePlatformHTTPStatus != 200 || !post.CaptchaHealthy ||
		post.CaptchaHTTPStatus != 200 || post.RootUsedPercent >= 80 {
		t.Fatalf("unsafe postconditions: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.PinnedActivationComplete || !verdict.PrimeComplete || !verdict.CacheHitComplete || !verdict.RustIntegrationComplete ||
		!verdict.CleanupComplete || !verdict.LegacyPreserved || !verdict.ApplicationsPreserved || verdict.BackpressureComplete ||
		verdict.StatisticalComplete || verdict.RustFSPromotionComplete || verdict.ReliabilityComplete || verdict.HAComplete {
		t.Fatalf("verdict overclaims or omits completed evidence: %#v", verdict)
	}
}

func validateSccacheRun(t *testing.T, name string, run sccacheCanaryRun, hex40, hex64 *regexp.Regexp) {
	t.Helper()
	if run.RunID <= 0 || run.JobID <= 0 || !hex40.MatchString(run.HeadSHA) || run.Conclusion != "success" ||
		run.JobDurationSeconds <= 0 || run.MeasurementElapsedNS <= 0 || run.CacheHits+run.CacheMisses != 57 ||
		run.CacheReadErrors != 0 || run.CacheWriteErrors != 0 || run.CacheErrors != 0 || run.ArtifactID <= 0 ||
		!hex64.MatchString(run.ArtifactResultSHA256) || run.GitHubLogBytesScanned <= 0 || run.GitHubLogSecretFindings != 0 {
		t.Fatalf("invalid %s canary: %#v", name, run)
	}
}
