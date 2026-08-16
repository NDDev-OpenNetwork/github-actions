package deploycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

func TestControlPlaneLifecycleRolloutAudit(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "config", "control-plane-lifecycle-rollout-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion int    `json:"schema_version"`
		MergeCommit   string `json:"repository_merge_commit"`
		Units         struct {
			GARM          string `json:"garm_sha256"`
			Observer      string `json:"observer_sha256"`
			Verified      bool   `json:"systemd_verify_passed"`
			GARMGateway   bool   `json:"garm_wants_gateway"`
			ObserverTimer bool   `json:"observer_wants_warm_timer"`
		} `json:"units"`
		Rollout struct {
			Attempts        int      `json:"unit_install_attempts"`
			Rollbacks       int      `json:"automatic_rollbacks"`
			BundleSHA256    []string `json:"rollback_bundle_sha256"`
			ExactRestore    bool     `json:"rollback_restored_exact_unit_hashes"`
			WorkerRecreate  bool     `json:"worker_vm_recreated"`
			DatabaseRestore bool     `json:"garm_database_restore_required"`
		} `json:"transactional_rollout"`
		Proof struct {
			OrderedStop        []string `json:"ordered_stop"`
			GatewayRestored    bool     `json:"single_garm_start_restored_gateway"`
			TimerRestored      bool     `json:"single_observer_start_restored_timer"`
			GARMReady          bool     `json:"garm_listener_ready"`
			GatewayReady       bool     `json:"gateway_listener_ready"`
			ProviderCompatible bool     `json:"provider_probe_compatible"`
			CacheReady         bool     `json:"provider_cache_delivery_ready"`
			WarmReconciles     []string `json:"post_restart_warm_reconcile_completed_at"`
			GARMRestarts       int      `json:"garm_systemd_restarts"`
			GatewayRestarts    int      `json:"gateway_systemd_restarts"`
			ObserverRestarts   int      `json:"observer_systemd_restarts"`
		} `json:"accepted_restart_proof"`
		Post struct {
			Healthy            bool `json:"observer_healthy"`
			Fresh              bool `json:"observer_fresh"`
			Claims             int  `json:"provider_claims"`
			QueueActive        int  `json:"queue_active"`
			QueueInFlight      int  `json:"queue_in_flight"`
			Orphans            int  `json:"incus_orphan_instances"`
			Missing            int  `json:"incus_missing_instances"`
			DiagnosticsSource  int  `json:"diagnostic_source_bundles"`
			DiagnosticsExport  int  `json:"diagnostic_exported_bundles"`
			DiagnosticsPending int  `json:"diagnostic_pending_bundles"`
			FailedUnits        int  `json:"failed_systemd_units"`
			RootFree           int  `json:"root_free_percent"`
			LegacyListeners    int  `json:"legacy_listeners"`
			ExamplePlatform    bool `json:"example_platform_containers_healthy"`
			Captcha            bool `json:"captcha_containers_healthy"`
		} `json:"postconditions"`
		Verdict struct {
			Dependency bool `json:"dependency_rollout_accepted"`
			Restart    bool `json:"ordered_restart_recovery_accepted"`
			Legacy     bool `json:"old_runner_fleet_preserved"`
			Apps       bool `json:"retained_applications_preserved"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.MergeCommit != "875b0e77a0a36a52cdd6c9022092e92b11753e4c" ||
		!hex64.MatchString(audit.Units.GARM) || !hex64.MatchString(audit.Units.Observer) ||
		!audit.Units.Verified || !audit.Units.GARMGateway || !audit.Units.ObserverTimer {
		t.Fatalf("invalid merge-bound unit evidence: %#v", audit.Units)
	}
	if audit.Rollout.Attempts != 4 || audit.Rollout.Rollbacks != 3 || len(audit.Rollout.BundleSHA256) != 4 ||
		!audit.Rollout.ExactRestore || audit.Rollout.WorkerRecreate || audit.Rollout.DatabaseRestore {
		t.Fatalf("invalid transactional rollout evidence: %#v", audit.Rollout)
	}
	for _, digest := range audit.Rollout.BundleSHA256 {
		if !hex64.MatchString(digest) {
			t.Fatalf("invalid rollback bundle digest %q", digest)
		}
	}
	wantStop := []string{"gha-fleet-observer.service", "gha-warm-pool.timer", "gha-warm-pool.service", "garm.service"}
	if !equalStrings(audit.Proof.OrderedStop, wantStop) || !audit.Proof.GatewayRestored || !audit.Proof.TimerRestored ||
		!audit.Proof.GARMReady || !audit.Proof.GatewayReady || !audit.Proof.ProviderCompatible || !audit.Proof.CacheReady ||
		len(audit.Proof.WarmReconciles) != 2 || audit.Proof.WarmReconciles[0] == audit.Proof.WarmReconciles[1] ||
		audit.Proof.GARMRestarts != 0 || audit.Proof.GatewayRestarts != 0 || audit.Proof.ObserverRestarts != 0 {
		t.Fatalf("restart proof is incomplete: %#v", audit.Proof)
	}
	post := audit.Post
	if !post.Healthy || !post.Fresh || post.Claims != 0 || post.QueueActive != 0 || post.QueueInFlight != 0 ||
		post.Orphans != 0 || post.Missing != 0 || post.DiagnosticsSource != post.DiagnosticsExport ||
		post.DiagnosticsPending != 0 || post.FailedUnits != 0 || post.RootFree < 20 || post.LegacyListeners != 12 ||
		!post.ExamplePlatform || !post.Captcha {
		t.Fatalf("fleet postconditions are incomplete: %#v", post)
	}
	if !audit.Verdict.Dependency || !audit.Verdict.Restart || !audit.Verdict.Legacy || !audit.Verdict.Apps {
		t.Fatalf("invalid rollout verdict: %#v", audit.Verdict)
	}
}
