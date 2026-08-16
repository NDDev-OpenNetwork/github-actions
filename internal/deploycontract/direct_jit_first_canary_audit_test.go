package deploycontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirectJITFirstCanaryAudit(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file")
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "config", "direct-jit-first-canary-audit.json"))
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Canary        struct {
			WorkflowRunID  int64  `json:"workflow_run_id"`
			JobID          int64  `json:"job_id"`
			Conclusion     string `json:"conclusion"`
			ExecutionSecs  int    `json:"execution_seconds"`
			OfficialRunner struct {
				Immutable string `json:"immutable_boundary"`
				Checkout  string `json:"checkout"`
				RustFS    string `json:"rustfs_one_job_delivery"`
				Composite string `json:"composite_action"`
				Commands  string `json:"command_files"`
				Artifact  string `json:"artifact_upload"`
				Post      string `json:"post_action"`
			} `json:"official_runner_semantics"`
		} `json:"canary"`
		Direct struct {
			ProviderVersion  string `json:"provider_version"`
			FailedCreates    int    `json:"failed_create_attempts"`
			WorkerActivation int    `json:"worker_activation_count"`
			FailureBoundary  string `json:"failure_boundary"`
			SecretLogged     bool   `json:"secret_material_logged"`
			Promoted         bool   `json:"promoted"`
		} `json:"direct_jit_attempt"`
		Rollback struct {
			Mode                 string `json:"activation_mode"`
			DatabaseRestore      bool   `json:"database_restore_required"`
			BinaryRestore        bool   `json:"binary_restore_required"`
			ImageRestore         bool   `json:"image_restore_required"`
			CanaryCompleted      bool   `json:"canary_completed_after_rollback"`
			MetadataFileRequests int    `json:"metadata_install_and_file_requests"`
		} `json:"rollback"`
		Correction struct {
			ProviderVersion string   `json:"provider_version"`
			ExactFileNames  []string `json:"exact_file_names"`
			RejectUndotted  bool     `json:"undotted_shape_rejected_by_regression_test"`
			NewCanary       bool     `json:"direct_mode_requires_new_merge_bound_canary"`
		} `json:"correction"`
		Post struct {
			Healthy            bool `json:"fleet_healthy"`
			Claims             int  `json:"provider_claims"`
			QueueActive        int  `json:"queue_active"`
			QueueInFlight      int  `json:"queue_in_flight"`
			Orphans            int  `json:"incus_orphan_instances"`
			Missing            int  `json:"incus_missing_instances"`
			DiagnosticSource   int  `json:"diagnostic_source_bundles"`
			DiagnosticExported int  `json:"diagnostic_exported_bundles"`
			DiagnosticPending  int  `json:"diagnostic_pending_bundles"`
			LegacyListeners    int  `json:"legacy_listeners"`
			ExamplePlatformHealthy     bool `json:"example_platform_containers_healthy"`
			CaptchaHealthy     bool `json:"captcha_containers_healthy"`
			GARMActive         bool `json:"garm_active"`
			GatewayActive      bool `json:"gateway_active"`
			ObserverActive     bool `json:"observer_active"`
			RustFSActive       bool `json:"rustfs_active"`
			ZotActive          bool `json:"zot_active"`
			WarmTimerActive    bool `json:"warm_timer_active"`
		} `json:"postconditions"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Canary.WorkflowRunID == 0 || audit.Canary.JobID == 0 || audit.Canary.Conclusion != "success" || audit.Canary.ExecutionSecs != 14 {
		t.Fatalf("invalid canary identity or result: %#v", audit.Canary)
	}
	semantics := audit.Canary.OfficialRunner
	if semantics.Immutable != "passed" || semantics.Checkout != "passed" || semantics.RustFS != "passed" || semantics.Composite != "passed" || semantics.Commands != "passed" || semantics.Artifact != "passed" || semantics.Post != "passed" {
		t.Fatalf("official runner behavior is incomplete: %#v", semantics)
	}
	if audit.Direct.ProviderVersion != "v0.1.5-nddev.17" || audit.Direct.FailedCreates != 14 || audit.Direct.WorkerActivation != 0 || audit.Direct.FailureBoundary != "provider-extra-spec-validation" || audit.Direct.SecretLogged || audit.Direct.Promoted {
		t.Fatalf("failed direct-path attempt is not preserved exactly: %#v", audit.Direct)
	}
	if audit.Rollback.Mode != "metadata" || audit.Rollback.DatabaseRestore || audit.Rollback.BinaryRestore || audit.Rollback.ImageRestore || !audit.Rollback.CanaryCompleted || audit.Rollback.MetadataFileRequests != 4 {
		t.Fatalf("metadata rollback is incomplete: %#v", audit.Rollback)
	}
	wantFiles := []string{".runner", ".credentials", ".credentials_rsaparams"}
	if audit.Correction.ProviderVersion != "v0.1.5-nddev.18" || !audit.Correction.RejectUndotted || !audit.Correction.NewCanary || len(audit.Correction.ExactFileNames) != len(wantFiles) {
		t.Fatalf("correction contract is incomplete: %#v", audit.Correction)
	}
	for index, want := range wantFiles {
		if audit.Correction.ExactFileNames[index] != want {
			t.Fatalf("exact JIT file %d = %q, want %q", index, audit.Correction.ExactFileNames[index], want)
		}
	}
	post := audit.Post
	if !post.Healthy || post.Claims != 0 || post.QueueActive != 0 || post.QueueInFlight != 0 || post.Orphans != 0 || post.Missing != 0 || post.DiagnosticSource != post.DiagnosticExported || post.DiagnosticPending != 0 || post.LegacyListeners != 12 || !post.ExamplePlatformHealthy || !post.CaptchaHealthy || !post.GARMActive || !post.GatewayActive || !post.ObserverActive || !post.RustFSActive || !post.ZotActive || !post.WarmTimerActive {
		t.Fatalf("postconditions are not healthy: %#v", post)
	}
}
