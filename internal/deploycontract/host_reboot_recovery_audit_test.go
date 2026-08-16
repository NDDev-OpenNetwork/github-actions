package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
)

const hostRebootRecoveryAuditPath = "../../config/host-reboot-recovery-audit.json"

func TestHostRebootRecoveryEvidence(t *testing.T) {
	raw, err := os.ReadFile(hostRebootRecoveryAuditPath)
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int    `json:"schema_version"`
		Host                  string `json:"host"`
		RepositoryMergeCommit string `json:"repository_merge_commit"`
		Provider              struct {
			Version        string `json:"version"`
			Commit         string `json:"commit"`
			BinarySHA256   string `json:"binary_sha256"`
			PlatformSHA256 string `json:"platform_sha256"`
		} `json:"provider"`
		Preconditions struct {
			BootID                    string `json:"boot_id"`
			SystemdState              string `json:"systemd_state"`
			FailedUnits               int    `json:"failed_units"`
			ObserverHealthy           bool   `json:"observer_healthy"`
			WarmInstance              string `json:"warm_instance"`
			WarmState                 string `json:"warm_state"`
			WarmRunning               bool   `json:"warm_running"`
			BootAutostart             bool   `json:"boot_autostart"`
			JournalLeases             int    `json:"journal_leases"`
			JournalClaims             int    `json:"journal_claims"`
			LegacyListeners           int    `json:"legacy_listeners"`
			LegacyWorkers             int    `json:"legacy_workers"`
			ExamplePlatformHealthy            bool   `json:"example_platform_containers_healthy"`
			CaptchaHealthy            bool   `json:"captcha_containers_healthy"`
			DiagnosticBundles         int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles  int    `json:"diagnostic_pending_bundles"`
		} `json:"preconditions"`
		Reboot struct {
			DowntimeUpperBoundSeconds int    `json:"downtime_upper_bound_seconds"`
			BootID                    string `json:"boot_id"`
		} `json:"reboot"`
		Recovery struct {
			SameWarmInstance bool   `json:"same_warm_instance"`
			WarmInstance     string `json:"warm_instance"`
			WarmRunning      bool   `json:"warm_running"`
			JournalState     string `json:"journal_state"`
			JournalLeases    int    `json:"journal_leases"`
			JournalClaims    int    `json:"journal_claims"`
			ObserverHealthy  bool   `json:"observer_healthy"`
			SystemdState     string `json:"systemd_state"`
			FailedUnits      int    `json:"failed_units"`
			LegacyListeners  int    `json:"legacy_listeners"`
			ExamplePlatformHealthy   bool   `json:"example_platform_containers_healthy"`
			CaptchaHealthy   bool   `json:"captcha_containers_healthy"`
		} `json:"recovery"`
		PostRebootCanary struct {
			RunID                      int64  `json:"run_id"`
			JobID                      int64  `json:"job_id"`
			HeadSHA                    string `json:"head_sha"`
			DurationSeconds            int    `json:"duration_seconds"`
			Conclusion                 string `json:"conclusion"`
			ConsumedInstance           string `json:"consumed_instance"`
			InstanceDestroyed          bool   `json:"instance_destroyed"`
			ReturnedToWarmPool         bool   `json:"returned_to_warm_pool"`
			DiagnosticSHA256           string `json:"diagnostic_sha256"`
			DiagnosticsExported        bool   `json:"diagnostics_exported_to_rustfs"`
			ReplacementInstance        string `json:"replacement_instance"`
			ReplacementProviderVersion string `json:"replacement_provider_version"`
			ReplacementProviderCommit  string `json:"replacement_provider_commit"`
			ReplacementAutostart       bool   `json:"replacement_autostart"`
		} `json:"post_reboot_canary"`
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
			JournalOwner              string `json:"journal_owner"`
			JournalMode               string `json:"journal_mode"`
			LegacyListeners           int    `json:"legacy_listeners"`
			LegacyWorkers             int    `json:"legacy_workers"`
			ExamplePlatformHealthy            bool   `json:"example_platform_containers_healthy"`
			CaptchaHealthy            bool   `json:"captcha_containers_healthy"`
		} `json:"postconditions"`
		Verdict struct {
			HostRebootRecoveryComplete        bool `json:"host_reboot_recovery_complete"`
			WarmAutostartComplete             bool `json:"warm_autostart_complete"`
			PostRebootOneJobLifecycleComplete bool `json:"post_reboot_one_job_lifecycle_complete"`
			CacheRecoveryComplete             bool `json:"cache_recovery_complete"`
			LegacyRunnersPreserved            bool `json:"legacy_runners_preserved"`
			RetainedApplicationsPreserved     bool `json:"retained_applications_preserved"`
			HighAvailabilityComplete          bool `json:"high_availability_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	uuid := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryMergeCommit) ||
		audit.Provider.Version != "v0.1.5-nddev.8" || audit.Provider.Commit != audit.RepositoryMergeCommit ||
		!hex64.MatchString(audit.Provider.BinarySHA256) || !hex64.MatchString(audit.Provider.PlatformSHA256) {
		t.Fatalf("invalid reboot audit identity: %#v", audit)
	}
	pre := audit.Preconditions
	if !uuid.MatchString(pre.BootID) || pre.SystemdState != "running" || pre.FailedUnits != 0 || !pre.ObserverHealthy ||
		pre.WarmInstance == "" || pre.WarmState != "warm-ready" || !pre.WarmRunning || !pre.BootAutostart ||
		pre.JournalLeases != 1 || pre.JournalClaims != 0 || pre.LegacyListeners != 12 || pre.LegacyWorkers != 0 ||
		!pre.ExamplePlatformHealthy || !pre.CaptchaHealthy || pre.DiagnosticBundles != pre.DiagnosticExportedBundles || pre.DiagnosticPendingBundles != 0 {
		t.Fatalf("unsafe reboot preconditions: %#v", pre)
	}
	if !uuid.MatchString(audit.Reboot.BootID) || audit.Reboot.BootID == pre.BootID || audit.Reboot.DowntimeUpperBoundSeconds <= 0 {
		t.Fatalf("reboot was not proven: %#v", audit.Reboot)
	}
	recovery := audit.Recovery
	if !recovery.SameWarmInstance || recovery.WarmInstance != pre.WarmInstance || !recovery.WarmRunning ||
		recovery.JournalState != "warm-ready" || recovery.JournalLeases != 1 || recovery.JournalClaims != 0 ||
		!recovery.ObserverHealthy || recovery.SystemdState != "running" || recovery.FailedUnits != 0 ||
		recovery.LegacyListeners != 12 || !recovery.ExamplePlatformHealthy || !recovery.CaptchaHealthy {
		t.Fatalf("post-reboot recovery is incomplete: %#v", recovery)
	}
	canary := audit.PostRebootCanary
	if canary.RunID <= 0 || canary.JobID <= 0 || canary.HeadSHA != audit.RepositoryMergeCommit || canary.DurationSeconds <= 0 ||
		canary.Conclusion != "success" || canary.ConsumedInstance != pre.WarmInstance || !canary.InstanceDestroyed ||
		canary.ReturnedToWarmPool || !hex64.MatchString(canary.DiagnosticSHA256) || !canary.DiagnosticsExported ||
		canary.ReplacementInstance == "" || canary.ReplacementInstance == canary.ConsumedInstance ||
		canary.ReplacementProviderVersion != audit.Provider.Version || canary.ReplacementProviderCommit != audit.Provider.Commit ||
		!canary.ReplacementAutostart {
		t.Fatalf("post-reboot one-job lifecycle is incomplete: %#v", canary)
	}
	post := audit.Postconditions
	if post.SystemdState != "running" || post.FailedUnits != 0 || !post.ObserverHealthy || !post.ObserverFresh ||
		post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusVisibleInstances != 1 || post.IncusOrphans != 0 ||
		post.IncusMissingInstances != 0 || post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 ||
		post.JournalOwner != "garm:garm" || post.JournalMode != "0600" || post.LegacyListeners != 12 || post.LegacyWorkers != 0 ||
		!post.ExamplePlatformHealthy || !post.CaptchaHealthy {
		t.Fatalf("post-reboot postconditions are incomplete: %#v", post)
	}
	verdict := audit.Verdict
	if !verdict.HostRebootRecoveryComplete || !verdict.WarmAutostartComplete || !verdict.PostRebootOneJobLifecycleComplete ||
		!verdict.CacheRecoveryComplete || !verdict.LegacyRunnersPreserved || !verdict.RetainedApplicationsPreserved ||
		verdict.HighAvailabilityComplete {
		t.Fatalf("reboot verdict overclaims or omits evidence: %#v", verdict)
	}
}
