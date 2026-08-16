package deploycontract

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// The cancellation canary is the proof that an explicitly cancelled job still
// releases its worker: post-action completes, diagnostics leave the VM, the VM
// is destroyed within the lease, it never returns to the pool, and a different
// VM replaces it. Its record was the only audit in config/ that nothing read,
// so the milestone it supports rested on a file no check would have noticed
// going stale.
func TestWarmPoolCancellationAudit(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../config/warm-pool-cancellation-audit.json")
	if err != nil {
		t.Fatalf("read warm-pool cancellation audit: %v", err)
	}
	var audit struct {
		SchemaVersion int    `json:"schema_version"`
		Host          string `json:"host"`
		Workflow      struct {
			Conclusion          string    `json:"conclusion"`
			Label               string    `json:"label"`
			CancelledDuringStep string    `json:"cancelled_during_step"`
			PostActionCompleted bool      `json:"post_action_completed"`
			StartedAt           time.Time `json:"started_at"`
			CompletedAt         time.Time `json:"completed_at"`
		} `json:"workflow"`
		ExecutedWorker struct {
			InstanceName            string    `json:"instance_name"`
			DiagnosticsSHA256       string    `json:"diagnostics_sha256"`
			DiagnosticsExportedAt   time.Time `json:"diagnostics_exported_at"`
			LeaseAndClaimAbsentBy   time.Time `json:"instance_lease_and_claim_absent_by"`
			MaximumTeardownSeconds  int       `json:"maximum_observed_teardown_seconds"`
			ReturnedToWarmPool      bool      `json:"returned_to_warm_pool"`
			ProviderDeleteRequested time.Time `json:"provider_delete_requested_at"`
		} `json:"executed_worker"`
		Replacement struct {
			InstanceName             string `json:"instance_name"`
			DifferentFromExecuted    bool   `json:"different_from_executed_worker"`
			MaximumAdmissionToReadyS int    `json:"maximum_observed_admission_to_ready_seconds"`
		} `json:"replacement"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatalf("decode warm-pool cancellation audit: %v", err)
	}

	if audit.SchemaVersion != 1 || audit.Host == "" {
		t.Fatalf("unexpected audit identity: schema %d host %q", audit.SchemaVersion, audit.Host)
	}
	if audit.Workflow.Conclusion != "cancelled" {
		t.Fatalf("conclusion is %q; a run that was not cancelled proves nothing here", audit.Workflow.Conclusion)
	}
	if audit.Workflow.CancelledDuringStep == "" || !audit.Workflow.PostActionCompleted {
		t.Fatal("cancellation must land inside a step and still run post-actions")
	}
	if audit.Workflow.Label == "" || !audit.Workflow.CompletedAt.After(audit.Workflow.StartedAt) {
		t.Fatalf("incomplete workflow record: %#v", audit.Workflow)
	}

	worker := audit.ExecutedWorker
	if worker.InstanceName == "" || len(worker.DiagnosticsSHA256) < 32 {
		t.Fatalf("executed worker lacks an identity or a diagnostic digest: %#v", worker)
	}
	if !worker.DiagnosticsExportedAt.After(worker.ProviderDeleteRequested) {
		t.Fatal("diagnostics must be exported after deletion is requested, not before it")
	}
	if worker.ReturnedToWarmPool {
		t.Fatal("a cancelled worker was returned to the pool; one job per VM is the invariant")
	}
	if worker.MaximumTeardownSeconds <= 0 || worker.MaximumTeardownSeconds > 60 {
		t.Fatalf("teardown bound is %d seconds; cancellation must remain bounded", worker.MaximumTeardownSeconds)
	}
	if !worker.LeaseAndClaimAbsentBy.After(worker.ProviderDeleteRequested) {
		t.Fatal("the lease and claim must be observed absent after deletion, not assumed")
	}

	replacement := audit.Replacement
	if replacement.InstanceName == "" || replacement.InstanceName == worker.InstanceName || !replacement.DifferentFromExecuted {
		t.Fatalf("replacement %q does not prove a fresh VM followed %q", replacement.InstanceName, worker.InstanceName)
	}
	if replacement.MaximumAdmissionToReadyS <= 0 {
		t.Fatal("replenishment must record how long admission to ready took")
	}
}
