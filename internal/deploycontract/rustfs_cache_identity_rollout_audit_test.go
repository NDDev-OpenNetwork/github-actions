package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

func TestRustFSCacheIdentityRolloutEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/rustfs-cache-identity-rollout-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int    `json:"schema_version"`
		Host                  string `json:"host"`
		RepositoryMergeCommit string `json:"repository_merge_commit"`
		Implementation        struct {
			PullRequests              []int  `json:"pull_requests"`
			FeatureMergeCommit        string `json:"feature_merge_commit"`
			S3SigningMergeCommit      string `json:"s3_signing_merge_commit"`
			PolicyEnvelopeMergeCommit string `json:"policy_envelope_merge_commit"`
			ManagedProbeMergeCommit   string `json:"managed_probe_merge_commit"`
		} `json:"implementation"`
		Controller struct {
			Version          string `json:"version"`
			Commit           string `json:"commit"`
			BinarySHA256     string `json:"binary_sha256"`
			ConfigSHA256     string `json:"config_sha256"`
			ReleasePath      string `json:"release_path"`
			RollbackCommit   string `json:"rollback_commit"`
			RollbackRetained bool   `json:"rollback_retained"`
		} `json:"controller"`
		RustFS struct {
			Version                    string `json:"version"`
			BinarySHA256               string `json:"binary_sha256"`
			DeploymentStage            string `json:"deployment_stage"`
			ProductionPromotionAllowed bool   `json:"production_promotion_allowed"`
			Bucket                     string `json:"bucket"`
			QuotaBytes                 int64  `json:"quota_bytes"`
			QuotaType                  string `json:"quota_type"`
			LifecycleRules             int    `json:"lifecycle_rules"`
			IdentityRoles              int    `json:"identity_roles"`
		} `json:"rustfs"`
		Rollout struct {
			InitialLocalState                      string `json:"initial_local_state"`
			InitialRemoteState                     string `json:"initial_remote_state"`
			S3SigningFailureDetectedBeforeApply    bool   `json:"s3_signing_failure_detected_before_apply"`
			S3SigningFailureHTTPStatus             int    `json:"s3_signing_failure_http_status"`
			CorrectedHeadBucketStatus              int    `json:"corrected_head_bucket_status"`
			FirstApplyResourcesCreated             bool   `json:"first_apply_resources_created"`
			FirstApplyPostInspectionConverged      bool   `json:"first_apply_post_inspection_converged"`
			FirstApplyEffectiveProbesExecuted      bool   `json:"first_apply_effective_probes_executed"`
			FirstApplyCredentialsRetained          bool   `json:"first_apply_credentials_retained"`
			FirstApplyFailureReason                string `json:"first_apply_failure_reason"`
			RecoveryRequiredCredentialRegeneration bool   `json:"recovery_required_credential_regeneration"`
			RecoveryRequiredResourceDeletion       bool   `json:"recovery_required_resource_deletion"`
		} `json:"rollout"`
		ManagedApply struct {
			Applied                  bool     `json:"applied"`
			LocalState               string   `json:"local_state"`
			StateBefore              string   `json:"state_before"`
			StateAfter               string   `json:"state_after"`
			Actions                  []string `json:"actions"`
			ResultSHA256             string   `json:"result_sha256"`
			OwnPrefixWriteReadPassed bool     `json:"own_prefix_write_read_passed"`
			ReleaseWriteDenied       bool     `json:"release_write_denied"`
			CrossPrefixWriteDenied   bool     `json:"cross_prefix_write_denied"`
			IdentityDeleteDenied     bool     `json:"identity_delete_denied"`
			RootProbeCleanupPassed   bool     `json:"root_probe_cleanup_passed"`
		} `json:"managed_apply"`
		FinalPlan struct {
			Applied      bool   `json:"applied"`
			LocalState   string `json:"local_state"`
			StateBefore  string `json:"state_before"`
			StateAfter   string `json:"state_after"`
			Actions      int    `json:"actions"`
			ResultSHA256 string `json:"result_sha256"`
		} `json:"final_plan"`
		Storage struct {
			ListHTTPStatus     int    `json:"list_http_status"`
			RemainingObjects   int    `json:"remaining_objects"`
			ListResponseBytes  int    `json:"list_response_bytes"`
			ListResponseSHA256 string `json:"list_response_sha256"`
		} `json:"storage_postcondition"`
		Credentials struct {
			Files                int    `json:"files"`
			Owner                string `json:"owner"`
			Mode                 string `json:"mode"`
			LinkCount            int    `json:"link_count"`
			JournalSecretMatches int    `json:"journal_secret_matches"`
			SecretsInController  bool   `json:"secrets_in_controller_json"`
			SecretsInGoldenImage bool   `json:"secrets_in_golden_image"`
			SecretsInIncusConfig bool   `json:"secrets_in_incus_config"`
		} `json:"credential_postcondition"`
		HostPostcondition struct {
			BootID                    string `json:"boot_id"`
			FailedUnits               int    `json:"failed_units"`
			GARMActive                bool   `json:"garm_active"`
			RustFSActive              bool   `json:"rustfs_active"`
			ZotActive                 bool   `json:"zot_active"`
			ObserverHealthy           bool   `json:"observer_healthy"`
			ObserverFresh             bool   `json:"observer_fresh"`
			ObserverCollectionErrors  int    `json:"observer_collection_errors"`
			IncusVisibleInstances     int    `json:"incus_visible_instances"`
			IncusOrphans              int    `json:"incus_orphans"`
			IncusMissingInstances     int    `json:"incus_missing_instances"`
			WarmInstance              string `json:"warm_instance"`
			WarmRunning               bool   `json:"warm_running"`
			JournalWarmReady          int    `json:"journal_warm_ready"`
			JournalClaims             int    `json:"journal_claims"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
			LegacyListeners           int    `json:"legacy_listeners"`
			LegacyWorkers             int    `json:"legacy_workers"`
			ExamplePlatformHealthy    bool   `json:"example_platform_healthy"`
			CaptchaHealthy            bool   `json:"captcha_healthy"`
			RootUsedPercent           int    `json:"root_used_percent"`
		} `json:"host_postcondition"`
		Verdict struct {
			IdentityPlaneComplete          bool `json:"rustfs_identity_plane_complete"`
			EffectiveBoundariesComplete    bool `json:"effective_trust_boundaries_complete"`
			IdempotentPlanComplete         bool `json:"idempotent_plan_complete"`
			CredentialDistributionComplete bool `json:"credential_distribution_complete"`
			ProductionPromotionComplete    bool `json:"rustfs_production_promotion_complete"`
			LegacyRunnersPreserved         bool `json:"legacy_runners_preserved"`
			RetainedApplicationsPreserved  bool `json:"retained_applications_preserved"`
			HighAvailabilityComplete       bool `json:"high_availability_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryMergeCommit) ||
		len(audit.Implementation.PullRequests) != 4 || audit.Implementation.ManagedProbeMergeCommit != audit.RepositoryMergeCommit ||
		!hex40.MatchString(audit.Implementation.FeatureMergeCommit) || !hex40.MatchString(audit.Implementation.S3SigningMergeCommit) ||
		!hex40.MatchString(audit.Implementation.PolicyEnvelopeMergeCommit) {
		t.Fatalf("invalid rollout identity: %#v", audit)
	}
	for index, expected := range []int{100, 101, 102, 103} {
		if audit.Implementation.PullRequests[index] != expected {
			t.Fatalf("unexpected implementation PR sequence: %#v", audit.Implementation.PullRequests)
		}
	}
	controller := audit.Controller
	if controller.Version != "v0.1.0" || controller.Commit != audit.RepositoryMergeCommit || !hex64.MatchString(controller.BinarySHA256) ||
		!hex64.MatchString(controller.ConfigSHA256) || controller.ReleasePath == "" || !hex40.MatchString(controller.RollbackCommit) ||
		controller.RollbackCommit == controller.Commit || !controller.RollbackRetained {
		t.Fatalf("invalid controller evidence: %#v", controller)
	}
	rustfs := audit.RustFS
	if rustfs.Version != "1.0.0-rc.1" || !hex64.MatchString(rustfs.BinarySHA256) || rustfs.DeploymentStage != "canary-only" ||
		rustfs.ProductionPromotionAllowed || rustfs.Bucket != "github-actions-cache" || rustfs.QuotaBytes != 64*1024*1024*1024 ||
		rustfs.QuotaType != "HARD" || rustfs.LifecycleRules != 3 || rustfs.IdentityRoles != 4 {
		t.Fatalf("invalid RustFS evidence: %#v", rustfs)
	}
	rollout := audit.Rollout
	if rollout.InitialLocalState != "fresh" || rollout.InitialRemoteState != "fresh" || !rollout.S3SigningFailureDetectedBeforeApply ||
		rollout.S3SigningFailureHTTPStatus != 400 || rollout.CorrectedHeadBucketStatus != 404 || !rollout.FirstApplyResourcesCreated ||
		rollout.FirstApplyPostInspectionConverged || rollout.FirstApplyEffectiveProbesExecuted || !rollout.FirstApplyCredentialsRetained ||
		rollout.FirstApplyFailureReason == "" || rollout.RecoveryRequiredCredentialRegeneration || rollout.RecoveryRequiredResourceDeletion {
		t.Fatalf("rollout recovery was not fail-closed: %#v", rollout)
	}
	apply := audit.ManagedApply
	if !apply.Applied || apply.LocalState != "managed" || apply.StateBefore != "managed" || apply.StateAfter != "managed" ||
		len(apply.Actions) != 1 || apply.Actions[0] != "verify_effective_boundaries" || !hex64.MatchString(apply.ResultSHA256) ||
		!apply.OwnPrefixWriteReadPassed || !apply.ReleaseWriteDenied || !apply.CrossPrefixWriteDenied ||
		!apply.IdentityDeleteDenied || !apply.RootProbeCleanupPassed {
		t.Fatalf("effective trust boundaries are incomplete: %#v", apply)
	}
	plan := audit.FinalPlan
	if plan.Applied || plan.LocalState != "managed" || plan.StateBefore != "managed" || plan.StateAfter != "managed" ||
		plan.Actions != 0 || !hex64.MatchString(plan.ResultSHA256) {
		t.Fatalf("final plan is not idempotent: %#v", plan)
	}
	storage := audit.Storage
	if storage.ListHTTPStatus != 200 || storage.RemainingObjects != 0 || storage.ListResponseBytes <= 0 ||
		!hex64.MatchString(storage.ListResponseSHA256) {
		t.Fatalf("probe cleanup is incomplete: %#v", storage)
	}
	credentials := audit.Credentials
	if credentials.Files != 8 || credentials.Owner != "root:garm" || credentials.Mode != "0640" || credentials.LinkCount != 1 ||
		credentials.JournalSecretMatches != 0 || credentials.SecretsInController || credentials.SecretsInGoldenImage || credentials.SecretsInIncusConfig {
		t.Fatalf("credential boundary is incomplete: %#v", credentials)
	}
	host := audit.HostPostcondition
	if !uuid.MatchString(host.BootID) || host.FailedUnits != 0 || !host.GARMActive || !host.RustFSActive || !host.ZotActive ||
		!host.ObserverHealthy || !host.ObserverFresh || host.ObserverCollectionErrors != 0 || host.IncusVisibleInstances != 1 ||
		host.IncusOrphans != 0 || host.IncusMissingInstances != 0 || host.WarmInstance == "" || !host.WarmRunning ||
		host.JournalWarmReady != 1 || host.JournalClaims != 0 || host.DiagnosticBundles != host.DiagnosticExportedBundles ||
		host.DiagnosticPendingBundles != 0 || host.LegacyListeners != 12 || host.LegacyWorkers != 0 || !host.ExamplePlatformHealthy ||
		!host.CaptchaHealthy || host.RootUsedPercent >= 80 {
		t.Fatalf("unsafe host postcondition: %#v", host)
	}
	verdict := audit.Verdict
	if !verdict.IdentityPlaneComplete || !verdict.EffectiveBoundariesComplete || !verdict.IdempotentPlanComplete ||
		verdict.CredentialDistributionComplete || verdict.ProductionPromotionComplete || !verdict.LegacyRunnersPreserved ||
		!verdict.RetainedApplicationsPreserved || verdict.HighAvailabilityComplete {
		t.Fatalf("rollout verdict overclaims or omits evidence: %#v", verdict)
	}
}
