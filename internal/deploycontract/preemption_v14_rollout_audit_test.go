package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestPreemptionV14RolloutEvidence(t *testing.T) {
	raw, err := os.ReadFile("../../config/preemption-v14-rollout-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion         int    `json:"schema_version"`
		Kind                  string `json:"kind"`
		CapturedAt            string `json:"captured_at"`
		Host                  string `json:"host"`
		Repository            string `json:"repository"`
		RepositoryMergeCommit string `json:"repository_merge_commit"`
		GDSContext            struct {
			Result               string `json:"result"`
			Finding              string `json:"finding"`
			ExpectedSourceCommit string `json:"expected_source_commit"`
			ObservedSourceCommit string `json:"observed_source_commit"`
		} `json:"gds_context"`
		Deployment struct {
			GARMVersion              string `json:"garm_version"`
			GARMSHA256               string `json:"garm_sha256"`
			ControllerCommit         string `json:"controller_commit"`
			ControllerSHA256         string `json:"controller_sha256"`
			ProviderVersion          string `json:"provider_version"`
			ProviderCommit           string `json:"provider_commit"`
			ProviderSHA256           string `json:"provider_sha256"`
			ObserverVersion          string `json:"observer_version"`
			ObserverCommit           string `json:"observer_commit"`
			ObserverSHA256           string `json:"observer_sha256"`
			PlatformSHA256           string `json:"platform_sha256"`
			PlatformFingerprint      string `json:"platform_fingerprint"`
			JournalSchemaBefore      int    `json:"journal_schema_before"`
			JournalSchemaAfter       int    `json:"journal_schema_after"`
			RetainedRollback         string `json:"retained_rollback"`
			RollbackChecksumVerified bool   `json:"retained_rollback_checksum_verified"`
			RetainedRollbackCount    int    `json:"retained_rollback_count"`
			DeploymentStagingRemoved bool   `json:"deployment_staging_removed"`
		} `json:"deployment"`
		Parity struct {
			RunID       int64  `json:"run_id"`
			HeadSHA     string `json:"head_sha"`
			CreatedAt   string `json:"created_at"`
			CompletedAt string `json:"completed_at"`
			Conclusion  string `json:"conclusion"`
			Jobs        []struct {
				Name              string `json:"name"`
				JobID             int64  `json:"job_id"`
				InstanceName      string `json:"instance_name"`
				StartedAt         string `json:"started_at"`
				CompletedAt       string `json:"completed_at"`
				Conclusion        string `json:"conclusion"`
				DiagnosticArchive string `json:"diagnostic_archive"`
				DiagnosticSHA256  string `json:"diagnostic_sha256"`
			} `json:"jobs"`
			DistinctInstances          int `json:"distinct_instances"`
			ExecutedInstancesRemaining int `json:"executed_instances_remaining"`
			RetryWindow                struct {
				Observed             bool   `json:"observed"`
				Reason               string `json:"reason"`
				FailedCreateAttempts int    `json:"failed_create_attempts"`
				FirstAt              string `json:"first_at"`
				LastAt               string `json:"last_at"`
				EventualJobSuccess   bool   `json:"eventual_job_success"`
				OpenSchedulerGap     bool   `json:"open_scheduler_gap"`
			} `json:"preparing_warm_retry_window"`
		} `json:"startup_guard_parity_canary"`
		Counter struct {
			RunID                   int64  `json:"run_id"`
			HeadSHA                 string `json:"head_sha"`
			Mode                    string `json:"mode"`
			JobID                   int64  `json:"job_id"`
			InstanceName            string `json:"instance_name"`
			CreatedAt               string `json:"created_at"`
			StartedAt               string `json:"started_at"`
			CompletedAt             string `json:"completed_at"`
			Conclusion              string `json:"conclusion"`
			VictimInstance          string `json:"victim_instance"`
			ReplacementInstance     string `json:"replacement_instance"`
			ReservationCommittedAt  string `json:"reservation_committed_at"`
			VictimRemovedAt         string `json:"victim_removed_at"`
			CounterBefore           uint64 `json:"counter_before"`
			CounterDuring           uint64 `json:"counter_during"`
			CounterAfter            uint64 `json:"counter_after"`
			CurrentGaugeAfter       int    `json:"current_gauge_after"`
			DiagnosticArchive       string `json:"diagnostic_archive"`
			DiagnosticSHA256        string `json:"diagnostic_sha256"`
			VictimDiagnosticArchive string `json:"victim_diagnostic_archive"`
			VictimDiagnosticSHA256  string `json:"victim_diagnostic_sha256"`
		} `json:"durable_counter_canary"`
		Post struct {
			SnapshotSchemaVersion               int    `json:"snapshot_schema_version"`
			SnapshotHealthy                     bool   `json:"snapshot_healthy"`
			CollectionErrors                    int    `json:"collection_errors"`
			JournalSchemaVersion                int    `json:"journal_schema_version"`
			JournalWarmReady                    int    `json:"journal_warm_ready"`
			JournalClaims                       int    `json:"journal_claims"`
			JournalCurrentPreemptions           int    `json:"journal_current_preemptions"`
			JournalPreemptionsTotal             uint64 `json:"journal_preemptions_total"`
			IncusVisibleInstances               int    `json:"incus_visible_instances"`
			IncusOrphans                        int    `json:"incus_orphans"`
			IncusMissingInstances               int    `json:"incus_missing_instances"`
			DiagnosticBundles                   int    `json:"diagnostic_bundles"`
			DiagnosticExportedBundles           int    `json:"diagnostic_exported_bundles"`
			DiagnosticPendingBundles            int    `json:"diagnostic_pending_bundles"`
			DiagnosticExportConsecutiveFailures int    `json:"diagnostic_export_consecutive_failures"`
			DiagnosticExportState               string `json:"diagnostic_export_state"`
			RootFreePercent                     int    `json:"root_free_percent"`
			FailedSystemdUnits                  int    `json:"failed_systemd_units"`
			GARMRestarts                        int    `json:"garm_restarts"`
			ObserverRestarts                    int    `json:"observer_restarts"`
			LegacyRunnerListeners               int    `json:"legacy_runner_listeners"`
			ExamplePlatformContainersRunning    bool   `json:"example-platform_containers_running"`
			CaptchaContainersHealthy            bool   `json:"captcha_containers_healthy"`
		} `json:"postconditions"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	archive := regexp.MustCompile(`^runner-diagnostics-v1-[a-z0-9-]+-[0-9]{8}T[0-9]{6}\.[0-9]+Z-[0-9a-f]{12}\.tar\.gz$`)
	parseTime := func(value string) time.Time {
		t.Helper()
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			t.Fatalf("invalid timestamp %q: %v", value, err)
		}
		return parsed
	}
	if audit.SchemaVersion != 1 || audit.Kind != "preemption_v14_rollout_evidence" ||
		audit.Host != "server-example-legacy" || audit.Repository != "example-user/github-actions" ||
		!hex40.MatchString(audit.RepositoryMergeCommit) || parseTime(audit.CapturedAt).IsZero() {
		t.Fatalf("invalid evidence identity: %#v", audit)
	}
	if audit.GDSContext.Result != "not-proven" || audit.GDSContext.Finding != "GDS_CONTEXT_POLICY_SOURCE_COMMIT_MISMATCH" ||
		!hex40.MatchString(audit.GDSContext.ExpectedSourceCommit) || !hex40.MatchString(audit.GDSContext.ObservedSourceCommit) {
		t.Fatalf("GDS provenance gap was not recorded exactly: %#v", audit.GDSContext)
	}
	deployment := audit.Deployment
	if deployment.GARMVersion != "v0.2.1-nddev.2" || deployment.ProviderVersion != "v0.1.5-nddev.14" ||
		deployment.ObserverVersion != "v0.3.0" || deployment.ControllerCommit != audit.RepositoryMergeCommit ||
		deployment.ProviderCommit != audit.RepositoryMergeCommit || deployment.ObserverCommit != audit.RepositoryMergeCommit ||
		!hex64.MatchString(deployment.GARMSHA256) || !hex64.MatchString(deployment.ControllerSHA256) ||
		!hex64.MatchString(deployment.ProviderSHA256) || !hex64.MatchString(deployment.ObserverSHA256) ||
		!hex64.MatchString(deployment.PlatformSHA256) || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(deployment.PlatformFingerprint) ||
		deployment.JournalSchemaBefore != 3 || deployment.JournalSchemaAfter != 4 ||
		deployment.RetainedRollback != "v0.1.5-nddev.13-garm-nddev.2-8ad1b08" || !deployment.RollbackChecksumVerified ||
		deployment.RetainedRollbackCount != 1 || !deployment.DeploymentStagingRemoved {
		t.Fatalf("deployment evidence drifted: %#v", deployment)
	}
	parity := audit.Parity
	if parity.RunID != 31315682132 || !hex40.MatchString(parity.HeadSHA) || parity.Conclusion != "success" ||
		len(parity.Jobs) != 3 || parity.DistinctInstances != 3 || parity.ExecutedInstancesRemaining != 0 ||
		!parseTime(parity.CreatedAt).Before(parseTime(parity.CompletedAt)) {
		t.Fatalf("startup guard parity evidence drifted: %#v", parity)
	}
	instances := make(map[string]struct{}, len(parity.Jobs))
	for _, job := range parity.Jobs {
		if job.JobID <= 0 || job.Conclusion != "success" || job.InstanceName == "" ||
			!archive.MatchString(job.DiagnosticArchive) || !hex64.MatchString(job.DiagnosticSHA256) ||
			!parseTime(job.StartedAt).Before(parseTime(job.CompletedAt)) {
			t.Fatalf("invalid parity job evidence: %#v", job)
		}
		instances[job.InstanceName] = struct{}{}
	}
	if len(instances) != 3 || !parity.RetryWindow.Observed || parity.RetryWindow.Reason != "insufficient-cpu" ||
		parity.RetryWindow.FailedCreateAttempts != 7 || !parity.RetryWindow.EventualJobSuccess || !parity.RetryWindow.OpenSchedulerGap ||
		!parseTime(parity.RetryWindow.FirstAt).Before(parseTime(parity.RetryWindow.LastAt)) {
		t.Fatalf("bounded retry gap was not preserved: %#v", parity.RetryWindow)
	}
	counter := audit.Counter
	if counter.RunID != 31316397001 || counter.HeadSHA != audit.RepositoryMergeCommit || counter.Mode != "network-negative" ||
		counter.JobID <= 0 || counter.InstanceName == "" || counter.VictimInstance == "" || counter.ReplacementInstance == "" ||
		counter.InstanceName == counter.VictimInstance || counter.InstanceName == counter.ReplacementInstance ||
		counter.VictimInstance == counter.ReplacementInstance || counter.Conclusion != "success" ||
		counter.CounterBefore != 0 || counter.CounterDuring != 1 || counter.CounterAfter != 1 || counter.CurrentGaugeAfter != 0 ||
		!archive.MatchString(counter.DiagnosticArchive) || !hex64.MatchString(counter.DiagnosticSHA256) ||
		!archive.MatchString(counter.VictimDiagnosticArchive) || !hex64.MatchString(counter.VictimDiagnosticSHA256) ||
		!parseTime(counter.CreatedAt).Before(parseTime(counter.StartedAt)) || !parseTime(counter.StartedAt).Before(parseTime(counter.CompletedAt)) ||
		!parseTime(counter.ReservationCommittedAt).Before(parseTime(counter.VictimRemovedAt)) {
		t.Fatalf("durable counter canary drifted: %#v", counter)
	}
	post := audit.Post
	if post.SnapshotSchemaVersion != 4 || !post.SnapshotHealthy || post.CollectionErrors != 0 ||
		post.JournalSchemaVersion != 4 || post.JournalWarmReady != 1 || post.JournalClaims != 0 ||
		post.JournalCurrentPreemptions != 0 || post.JournalPreemptionsTotal != counter.CounterAfter ||
		post.IncusVisibleInstances != 1 || post.IncusOrphans != 0 || post.IncusMissingInstances != 0 ||
		post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 ||
		post.DiagnosticExportConsecutiveFailures != 0 || post.DiagnosticExportState != "synchronized" ||
		post.RootFreePercent < 20 || post.FailedSystemdUnits != 0 || post.GARMRestarts != 0 || post.ObserverRestarts != 0 ||
		post.LegacyRunnerListeners != 12 || !post.ExamplePlatformContainersRunning || !post.CaptchaContainersHealthy {
		t.Fatalf("postconditions are incomplete: %#v", post)
	}
}
