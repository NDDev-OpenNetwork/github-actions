package deploycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirectJITLifecycleCanaryAudit(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "config", "direct-jit-lifecycle-canary-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Canary        struct {
			RunID            int64  `json:"workflow_run_id"`
			JobID            int64  `json:"job_id"`
			Conclusion       string `json:"conclusion"`
			ExecutionSeconds int    `json:"execution_seconds"`
			QueueSeconds     int    `json:"queue_to_job_start_seconds"`
			MetadataRequests int    `json:"metadata_install_or_credential_requests"`
			ProviderErrors   int    `json:"provider_create_errors"`
			SemanticsPassed  bool   `json:"official_runner_semantics_passed"`
		} `json:"canary"`
		Finding struct {
			GARMVersion        string `json:"garm_version"`
			ProviderVersion    string `json:"provider_version"`
			InstanceStatus     string `json:"instance_status_after_job"`
			RunnerStatus       string `json:"runner_status_after_job"`
			JobStatus          string `json:"github_job_status_in_garm"`
			InvalidTransition  string `json:"invalid_transition"`
			TransitionErrors   int    `json:"invalid_transition_errors"`
			CompletedReplays   int    `json:"replayed_job_completed_events"`
			AutomaticTeardown  bool   `json:"automatic_teardown_completed"`
			DirectModePromoted bool   `json:"direct_mode_promoted"`
		} `json:"lifecycle_finding"`
		Recovery struct {
			Mode              string `json:"activation_mode"`
			GARMRestart       bool   `json:"garm_restart_performed"`
			DatabaseRestore   bool   `json:"database_restore_required"`
			ImageRestore      bool   `json:"image_restore_required"`
			JournalReconciled bool   `json:"provider_journal_reconciled"`
		} `json:"recovery"`
		Correction struct {
			GARMVersion        string   `json:"garm_version"`
			Pending            []string `json:"direct_pending_transitions"`
			Installing         []string `json:"direct_installing_transitions"`
			Trigger            string   `json:"authoritative_trigger"`
			NonDirectUnchanged bool     `json:"non_direct_behavior_unchanged"`
			NewCanary          bool     `json:"direct_mode_requires_new_merge_bound_canary"`
		} `json:"correction"`
		Post struct {
			Healthy            bool   `json:"fleet_healthy"`
			Mode               string `json:"activation_mode"`
			Claims             int    `json:"provider_claims"`
			QueueActive        int    `json:"queue_active"`
			QueueInFlight      int    `json:"queue_in_flight"`
			Orphans            int    `json:"incus_orphan_instances"`
			Missing            int    `json:"incus_missing_instances"`
			DiagnosticsSource  int    `json:"diagnostic_source_bundles"`
			DiagnosticsExport  int    `json:"diagnostic_exported_bundles"`
			DiagnosticsPending int    `json:"diagnostic_pending_bundles"`
			WarmProvider       string `json:"warm_provider_version"`
			Errors             int    `json:"garm_errors_after_recovery"`
			LegacyListeners    int    `json:"legacy_listeners"`
			GARM               bool   `json:"garm_active"`
			Gateway            bool   `json:"gateway_active"`
			Observer           bool   `json:"observer_active"`
			RustFS             bool   `json:"rustfs_active"`
			Zot                bool   `json:"zot_active"`
			WarmTimer          bool   `json:"warm_timer_active"`
		} `json:"postconditions"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Canary.RunID == 0 || audit.Canary.JobID == 0 || audit.Canary.Conclusion != "success" || audit.Canary.ExecutionSeconds != 12 || audit.Canary.QueueSeconds != 9 || audit.Canary.MetadataRequests != 0 || audit.Canary.ProviderErrors != 0 || !audit.Canary.SemanticsPassed {
		t.Fatalf("direct canary evidence is incomplete: %#v", audit.Canary)
	}
	if audit.Finding.GARMVersion != "v0.2.1-nddev.8" || audit.Finding.ProviderVersion != "v0.1.5-nddev.18" || audit.Finding.InstanceStatus != "running" || audit.Finding.RunnerStatus != "pending" || audit.Finding.JobStatus != "in_progress" || audit.Finding.InvalidTransition != "pending -> active" || audit.Finding.TransitionErrors != 1284 || audit.Finding.CompletedReplays != 1284 || audit.Finding.AutomaticTeardown || audit.Finding.DirectModePromoted {
		t.Fatalf("lifecycle failure was not preserved exactly: %#v", audit.Finding)
	}
	if audit.Recovery.Mode != "metadata" || !audit.Recovery.GARMRestart || audit.Recovery.DatabaseRestore || audit.Recovery.ImageRestore || !audit.Recovery.JournalReconciled {
		t.Fatalf("recovery evidence is incomplete: %#v", audit.Recovery)
	}
	wantPending := []string{"installing", "idle", "active"}
	wantInstalling := []string{"idle", "active"}
	if audit.Correction.GARMVersion != "v0.2.1-nddev.9" || audit.Correction.Trigger != "GitHub JobStarted" || !audit.Correction.NonDirectUnchanged || !audit.Correction.NewCanary || !equalStrings(audit.Correction.Pending, wantPending) || !equalStrings(audit.Correction.Installing, wantInstalling) {
		t.Fatalf("correction contract is incomplete: %#v", audit.Correction)
	}
	post := audit.Post
	if !post.Healthy || post.Mode != "metadata" || post.Claims != 0 || post.QueueActive != 0 || post.QueueInFlight != 0 || post.Orphans != 0 || post.Missing != 0 || post.DiagnosticsSource != post.DiagnosticsExport || post.DiagnosticsPending != 0 || post.WarmProvider != "v0.1.5-nddev.18" || post.Errors != 0 || post.LegacyListeners != 12 || !post.GARM || !post.Gateway || !post.Observer || !post.RustFS || !post.Zot || !post.WarmTimer {
		t.Fatalf("recovered fleet is not healthy: %#v", post)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
