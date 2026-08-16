package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestOneJobCacheDeliveryRuntimeEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/one-job-cache-delivery-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int    `json:"schema_version"`
		Host                  string `json:"host"`
		RepositoryMergeCommit string `json:"repository_merge_commit"`
		Implementation        struct {
			PullRequests              []int  `json:"pull_requests"`
			DeliveryMergeCommit       string `json:"delivery_merge_commit"`
			ReadinessProbeMergeCommit string `json:"readiness_probe_merge_commit"`
			PublicTrustAnchorCommit   string `json:"public_trust_anchor_merge_commit"`
			RuntimeCanaryMergeCommit  string `json:"runtime_canary_merge_commit"`
			RequestFixMergeCommit     string `json:"request_fix_merge_commit"`
			DiagnosticFixMergeCommit  string `json:"diagnostic_fix_merge_commit"`
			RunnerIdentityMergeCommit string `json:"runner_identity_merge_commit"`
		} `json:"implementation"`
		Runtime struct {
			ProviderVersion          string `json:"provider_version"`
			ProviderCommit           string `json:"provider_commit"`
			ProviderBinarySHA256     string `json:"provider_binary_sha256"`
			ControllerVersion        string `json:"controller_version"`
			ControllerCommit         string `json:"controller_commit"`
			ControllerBinarySHA256   string `json:"controller_binary_sha256"`
			ObserverBinarySHA256     string `json:"observer_binary_sha256"`
			DiagnosticBinarySHA256   string `json:"diagnostic_exporter_binary_sha256"`
			PlatformConfigSHA256     string `json:"platform_config_sha256"`
			ProviderConfigSHA256     string `json:"provider_config_sha256"`
			StandardImageFingerprint string `json:"standard_image_fingerprint"`
			IntegrationFingerprint   string `json:"integration_image_fingerprint"`
			RunnerUID                int    `json:"runner_uid"`
			RunnerGID                int    `json:"runner_gid"`
			RollbackVersion          string `json:"rollback_version"`
			RollbackCommit           string `json:"rollback_commit"`
			RollbackRetained         bool   `json:"rollback_retained"`
		} `json:"runtime"`
		TrustBoundary struct {
			PublicCAPath                  string `json:"public_ca_path"`
			PublicCASHA256                string `json:"public_ca_sha256"`
			PublicCAOwner                 string `json:"public_ca_owner"`
			PublicCAMode                  string `json:"public_ca_mode"`
			PrivateKeyDirectoryMode       string `json:"private_key_directory_mode"`
			CacheRole                     string `json:"cache_role"`
			CacheMode                     string `json:"cache_mode"`
			PrefixRoot                    string `json:"prefix_root"`
			AssignmentFiles               int    `json:"assignment_files"`
			StagingFilesPresentDuringJob  int    `json:"staging_files_present_during_job"`
			ConsumedMarkerMetadata        string `json:"consumed_marker_metadata"`
			GlobalHostDockerSocketExposed bool   `json:"global_host_docker_socket_exposed"`
		} `json:"trust_boundary"`
		WarmCanary struct {
			RunID                          int64  `json:"run_id"`
			JobID                          int64  `json:"job_id"`
			HeadSHA                        string `json:"head_sha"`
			Conclusion                     string `json:"conclusion"`
			InstanceName                   string `json:"instance_name"`
			DestroyedAfterJob              bool   `json:"destroyed_after_job"`
			ReturnedToWarmPool             bool   `json:"returned_to_warm_pool"`
			DiagnosticSHA256               string `json:"diagnostic_sha256"`
			OwnPrefixPutStatus             int    `json:"own_prefix_put_status"`
			OwnPrefixGetStatus             int    `json:"own_prefix_get_status"`
			CrossTrustPutStatus            int    `json:"cross_trust_put_status"`
			OwnPrefixDeleteStatus          int    `json:"own_prefix_delete_status"`
			RootCleanupDeleteStatus        int    `json:"root_cleanup_delete_status"`
			RootCleanupAfterStatus         int    `json:"root_cleanup_after_status"`
			OfficialRunnerPostActionPassed bool   `json:"official_runner_post_action_passed"`
		} `json:"warm_canary"`
		ColdCanary struct {
			RunID                        int64  `json:"run_id"`
			HeadSHA                      string `json:"head_sha"`
			Conclusion                   string `json:"conclusion"`
			WarmCapacityDrainedBeforeRun bool   `json:"warm_capacity_drained_before_run"`
			Jobs                         []struct {
				Name              string `json:"name"`
				JobID             int64  `json:"job_id"`
				InstanceName      string `json:"instance_name"`
				Conclusion        string `json:"conclusion"`
				DiagnosticArchive string `json:"diagnostic_archive"`
				DiagnosticSHA256  string `json:"diagnostic_sha256"`
			} `json:"jobs"`
			ColdInstancesRemaining int `json:"cold_instances_remaining"`
			ColdLeasesRemaining    int `json:"cold_leases_remaining"`
			ColdClaimsRemaining    int `json:"cold_claims_remaining"`
		} `json:"cold_integration_canary"`
		Secrecy struct {
			GitHubLogBundlesScanned      int  `json:"github_log_bundles_scanned"`
			GitHubLogAccessKeyMatches    int  `json:"github_log_access_key_matches"`
			GitHubLogSecretKeyMatches    int  `json:"github_log_secret_key_matches"`
			ColdDiagnosticBundlesScanned int  `json:"cold_diagnostic_bundles_scanned"`
			ColdDiagnosticAccessMatches  int  `json:"cold_diagnostic_access_key_matches"`
			ColdDiagnosticSecretMatches  int  `json:"cold_diagnostic_secret_key_matches"`
			CredentialsInGoldenImages    bool `json:"credentials_in_golden_images"`
			CredentialsInProviderJournal bool `json:"credentials_in_provider_journal"`
			CredentialsInIncusConfig     bool `json:"credentials_in_incus_config"`
		} `json:"secrecy_audit"`
		Postconditions struct {
			FailedUnits               int    `json:"failed_units"`
			ObserverHealthy           bool   `json:"observer_healthy"`
			ObserverFresh             bool   `json:"observer_fresh"`
			ObserverCollectionErrors  int    `json:"observer_collection_errors"`
			WarmTimerActive           bool   `json:"warm_timer_active"`
			ReplacementInstance       string `json:"replacement_instance"`
			ReplacementRunning        bool   `json:"replacement_running"`
			JournalWarmReady          int    `json:"journal_warm_ready"`
			JournalClaims             int    `json:"journal_claims"`
			IncusOrphans              int    `json:"incus_orphans"`
			IncusMissingInstances     int    `json:"incus_missing_instances"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			LegacyListeners           int    `json:"legacy_listeners"`
			ExamplePlatformHealthy            bool   `json:"example_platform_healthy"`
			ExamplePlatformHTTPStatus         int    `json:"example_platform_http_status"`
			CaptchaHealthy            bool   `json:"captcha_healthy"`
			RootUsedPercent           int    `json:"root_used_percent"`
		} `json:"postconditions"`
		Verdict struct {
			OneJobCacheDeliveryComplete       bool `json:"one_job_cache_delivery_complete"`
			WarmLifecycleComplete             bool `json:"warm_lifecycle_complete"`
			ColdLifecycleComplete             bool `json:"cold_lifecycle_complete"`
			DockerActionParityComplete        bool `json:"docker_action_parity_complete"`
			JobServiceContainerParityComplete bool `json:"job_and_service_container_parity_complete"`
			CredentialSecrecyComplete         bool `json:"credential_secrecy_complete"`
			CleanupComplete                   bool `json:"cleanup_complete"`
			LegacyRunnersPreserved            bool `json:"legacy_runners_preserved"`
			RetainedApplicationsPreserved     bool `json:"retained_applications_preserved"`
			RustFSProductionPromotionComplete bool `json:"rustfs_production_promotion_complete"`
			WarmP95GateComplete               bool `json:"warm_queue_to_online_p95_gate_complete"`
			ProductionReliabilityComplete     bool `json:"production_reliability_gate_complete"`
			HighAvailabilityComplete          bool `json:"high_availability_complete"`
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
	commits := []string{
		audit.Implementation.DeliveryMergeCommit,
		audit.Implementation.ReadinessProbeMergeCommit,
		audit.Implementation.PublicTrustAnchorCommit,
		audit.Implementation.RuntimeCanaryMergeCommit,
		audit.Implementation.RequestFixMergeCommit,
		audit.Implementation.DiagnosticFixMergeCommit,
		audit.Implementation.RunnerIdentityMergeCommit,
	}
	if len(audit.Implementation.PullRequests) != 7 || len(commits) != 7 || audit.Implementation.RunnerIdentityMergeCommit != audit.RepositoryMergeCommit {
		t.Fatalf("incomplete implementation chain: %#v", audit.Implementation)
	}
	for index, commit := range commits {
		if audit.Implementation.PullRequests[index] != 105+index || !hex40.MatchString(commit) {
			t.Fatalf("invalid implementation chain: %#v", audit.Implementation)
		}
	}
	runtime := audit.Runtime
	for _, digest := range []string{runtime.ProviderBinarySHA256, runtime.ControllerBinarySHA256, runtime.ObserverBinarySHA256,
		runtime.DiagnosticBinarySHA256, runtime.PlatformConfigSHA256, runtime.ProviderConfigSHA256,
		runtime.StandardImageFingerprint, runtime.IntegrationFingerprint} {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid runtime digest %q", digest)
		}
	}
	if runtime.ProviderVersion != "v0.1.5-nddev.11" || runtime.ProviderCommit != audit.RepositoryMergeCommit ||
		runtime.ControllerVersion != "v0.1.0" || runtime.ControllerCommit != audit.RepositoryMergeCommit ||
		runtime.RunnerUID != 1001 || runtime.RunnerGID != 1002 || runtime.RollbackVersion != "v0.1.5-nddev.10" ||
		!hex40.MatchString(runtime.RollbackCommit) || runtime.RollbackCommit == runtime.ProviderCommit || !runtime.RollbackRetained {
		t.Fatalf("runtime is not exactly merge-bound with rollback: %#v", runtime)
	}
	trust := audit.TrustBoundary
	if trust.PublicCAPath != "/etc/gha-fleet/trust/rustfs-ca.pem" || !hex64.MatchString(trust.PublicCASHA256) ||
		trust.PublicCAOwner != "root:root" || trust.PublicCAMode != "0644" || trust.PrivateKeyDirectoryMode != "0700" ||
		trust.CacheRole != "trusted-writer" || trust.CacheMode != "read-write" || trust.PrefixRoot != "example-user/github-actions/trust/trusted" ||
		trust.AssignmentFiles != 1 || trust.StagingFilesPresentDuringJob != 0 ||
		trust.ConsumedMarkerMetadata != "1001:1002:600:1:regular file" || trust.GlobalHostDockerSocketExposed {
		t.Fatalf("cache trust boundary is incomplete: %#v", trust)
	}
	warm := audit.WarmCanary
	if warm.RunID != 31306023815 || warm.JobID != 93226442703 || warm.HeadSHA != audit.RepositoryMergeCommit ||
		warm.Conclusion != "success" || warm.InstanceName == "" || !warm.DestroyedAfterJob || warm.ReturnedToWarmPool ||
		!hex64.MatchString(warm.DiagnosticSHA256) || warm.OwnPrefixPutStatus != 200 || warm.OwnPrefixGetStatus != 200 ||
		warm.CrossTrustPutStatus != 403 || warm.OwnPrefixDeleteStatus != 403 || warm.RootCleanupDeleteStatus != 204 ||
		warm.RootCleanupAfterStatus != 404 || !warm.OfficialRunnerPostActionPassed {
		t.Fatalf("warm cache canary is incomplete: %#v", warm)
	}
	cold := audit.ColdCanary
	if cold.RunID != 31306125176 || cold.HeadSHA != audit.RepositoryMergeCommit || cold.Conclusion != "success" ||
		!cold.WarmCapacityDrainedBeforeRun || len(cold.Jobs) != 3 || cold.ColdInstancesRemaining != 0 ||
		cold.ColdLeasesRemaining != 0 || cold.ColdClaimsRemaining != 0 {
		t.Fatalf("cold integration canary is incomplete: %#v", cold)
	}
	expectedJobs := []string{"VM-local Docker boundary", "Local Docker container action", "Job and service containers"}
	seenInstances := map[string]bool{}
	for index, job := range cold.Jobs {
		if job.Name != expectedJobs[index] || job.JobID <= 0 || job.InstanceName == "" || seenInstances[job.InstanceName] ||
			job.Conclusion != "success" || job.DiagnosticArchive == "" || !hex64.MatchString(job.DiagnosticSHA256) {
			t.Fatalf("invalid cold job evidence: %#v", job)
		}
		seenInstances[job.InstanceName] = true
	}
	secrecy := audit.Secrecy
	if secrecy.GitHubLogBundlesScanned != 2 || secrecy.GitHubLogAccessKeyMatches != 0 || secrecy.GitHubLogSecretKeyMatches != 0 ||
		secrecy.ColdDiagnosticBundlesScanned != 3 || secrecy.ColdDiagnosticAccessMatches != 0 || secrecy.ColdDiagnosticSecretMatches != 0 ||
		secrecy.CredentialsInGoldenImages || secrecy.CredentialsInProviderJournal || secrecy.CredentialsInIncusConfig {
		t.Fatalf("credential secrecy is not proven: %#v", secrecy)
	}
	post := audit.Postconditions
	if post.FailedUnits != 0 || !post.ObserverHealthy || !post.ObserverFresh || post.ObserverCollectionErrors != 0 ||
		!post.WarmTimerActive || post.ReplacementInstance == "" || post.ReplacementInstance == warm.InstanceName || !post.ReplacementRunning ||
		post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusOrphans != 0 || post.IncusMissingInstances != 0 ||
		post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 || post.LegacyListeners != 12 ||
		!post.ExamplePlatformHealthy || post.ExamplePlatformHTTPStatus != 200 || !post.CaptchaHealthy || post.RootUsedPercent >= 80 {
		t.Fatalf("unsafe postconditions: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.OneJobCacheDeliveryComplete || !verdict.WarmLifecycleComplete || !verdict.ColdLifecycleComplete ||
		!verdict.DockerActionParityComplete || !verdict.JobServiceContainerParityComplete || !verdict.CredentialSecrecyComplete ||
		!verdict.CleanupComplete || !verdict.LegacyRunnersPreserved || !verdict.RetainedApplicationsPreserved ||
		verdict.RustFSProductionPromotionComplete || verdict.WarmP95GateComplete || verdict.ProductionReliabilityComplete ||
		verdict.HighAvailabilityComplete {
		t.Fatalf("verdict overclaims or omits completed evidence: %#v", verdict)
	}
}
