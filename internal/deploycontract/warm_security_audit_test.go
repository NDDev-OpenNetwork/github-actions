package deploycontract

import (
	"encoding/json"
	"math"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

const warmSecurityAuditPath = "../../config/warm-bootstrap-xtrace-audit.json"

type warmSecurityAudit struct {
	SchemaVersion    int       `json:"schema_version"`
	CapturedAt       time.Time `json:"captured_at"`
	Host             string    `json:"host"`
	RepositoryCommit string    `json:"repository_commit"`
	Provider         struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
	} `json:"provider"`
	ImageFingerprint string `json:"image_fingerprint"`
	Workflow         struct {
		RunID       int64     `json:"run_id"`
		JobID       int64     `json:"job_id"`
		HeadSHA     string    `json:"head_sha"`
		CreatedAt   time.Time `json:"created_at"`
		StartedAt   time.Time `json:"started_at"`
		CompletedAt time.Time `json:"completed_at"`
		Conclusion  string    `json:"conclusion"`
		RunnerName  string    `json:"runner_name"`
		RunnerGroup string    `json:"runner_group"`
		Label       string    `json:"label"`
	} `json:"workflow"`
	ExecutedWorker struct {
		InstanceName         string    `json:"instance_name"`
		WarmAgentStartedAt   time.Time `json:"warm_agent_started_at"`
		WarmAgentCompletedAt time.Time `json:"warm_agent_completed_at"`
		WarmAgentDuration    float64   `json:"warm_agent_duration_seconds"`
		ReturnedToWarmPool   bool      `json:"returned_to_warm_pool"`
		DestroyedAfterJob    bool      `json:"destroyed_after_job"`
	} `json:"executed_worker"`
	SecretBoundary struct {
		InstallerShell                  string `json:"installer_shell"`
		InheritedXtraceFD               int    `json:"inherited_xtrace_fd"`
		InheritedXtraceSink             string `json:"inherited_xtrace_sink"`
		LiveGuestJournalJWTShapeMatches int    `json:"live_guest_journal_jwt_shape_matches"`
		DiagnosticBundleJWTShapeMatches int    `json:"local_diagnostic_bundle_jwt_shape_matches"`
		LocalDiagnosticBundlesScanned   int    `json:"local_diagnostic_bundles_scanned"`
		ExecutableRegressionTest        string `json:"executable_regression_test"`
		Scope                           string `json:"scope"`
	} `json:"secret_boundary"`
	Diagnostics struct {
		Archive         string `json:"archive"`
		SHA256          string `json:"sha256"`
		SourceBundles   int    `json:"source_bundles"`
		ExportedBundles int    `json:"exported_bundles"`
		PendingBundles  int    `json:"pending_bundles"`
		SourceBytes     int64  `json:"source_bytes"`
		ExportedBytes   int64  `json:"exported_bytes"`
		RustFSSyncState string `json:"rustfs_sync_state"`
	} `json:"diagnostics"`
	Postconditions struct {
		ObserverHealthy       bool     `json:"observer_healthy"`
		ObserverFresh         bool     `json:"observer_fresh"`
		JournalSchemaVersion  int      `json:"journal_schema_version"`
		JournalWarmReady      int      `json:"journal_warm_ready"`
		JournalClaims         int      `json:"journal_claims"`
		IncusVisibleInstances int      `json:"incus_visible_instances"`
		IncusOrphans          int      `json:"incus_orphans"`
		IncusMissingInstances int      `json:"incus_missing_instances"`
		RootFreePercent       int      `json:"root_free_percent"`
		LegacyListeners       int      `json:"legacy_listeners"`
		ServicesActive        []string `json:"services_active"`
	} `json:"postconditions"`
	LatencySample struct {
		Eligible bool   `json:"eligible_for_nominal_latency_statistics"`
		Reason   string `json:"reason"`
	} `json:"latency_sample"`
	Verdict struct {
		InheritedXtraceSuppressed               bool   `json:"inherited_xtrace_suppressed"`
		CallbackCredentialAbsentFromObservation bool   `json:"callback_credential_shape_absent_from_observed_outputs"`
		WorkerDestroyed                         bool   `json:"worker_destroyed"`
		DiagnosticsExported                     bool   `json:"diagnostics_exported"`
		ProductionReliabilityGateComplete       bool   `json:"production_reliability_gate_complete"`
		RemainingGate                           string `json:"remaining_gate"`
	} `json:"verdict"`
}

func TestWarmBootstrapXtraceAuditProvesSecretBoundary(t *testing.T) {
	source, err := os.ReadFile(warmSecurityAuditPath)
	if err != nil {
		t.Fatalf("read warm security audit: %v", err)
	}
	var audit warmSecurityAudit
	if err := json.Unmarshal(source, &audit); err != nil {
		t.Fatalf("decode warm security audit: %v", err)
	}

	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.Host != "server-example-legacy" || audit.CapturedAt.IsZero() {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	if !hex40.MatchString(audit.RepositoryCommit) || audit.Provider.Commit != audit.RepositoryCommit || audit.Workflow.HeadSHA != audit.RepositoryCommit {
		t.Fatalf("audit is not bound to one exact merge commit: repo=%q provider=%q workflow=%q", audit.RepositoryCommit, audit.Provider.Commit, audit.Workflow.HeadSHA)
	}
	if audit.Provider.Version != "v0.1.5-nddev.5" || !hex64.MatchString(audit.ImageFingerprint) {
		t.Fatalf("provider/image provenance is incomplete: version=%q image=%q", audit.Provider.Version, audit.ImageFingerprint)
	}
	if audit.Workflow.RunID == 0 || audit.Workflow.JobID == 0 || audit.Workflow.Conclusion != "success" || audit.Workflow.RunnerName == "" || audit.Workflow.RunnerGroup != "Default" || audit.Workflow.Label != "nddev-linux-standard" {
		t.Fatalf("workflow proof is incomplete: %#v", audit.Workflow)
	}
	if audit.Workflow.CreatedAt.After(audit.Workflow.StartedAt) || audit.Workflow.StartedAt.After(audit.Workflow.CompletedAt) {
		t.Fatalf("workflow timestamps are not monotonic: %#v", audit.Workflow)
	}
	observedDuration := audit.ExecutedWorker.WarmAgentCompletedAt.Sub(audit.ExecutedWorker.WarmAgentStartedAt).Seconds()
	if audit.ExecutedWorker.InstanceName == "" || observedDuration <= 0 || math.Abs(observedDuration-audit.ExecutedWorker.WarmAgentDuration) > 0.001 || audit.ExecutedWorker.ReturnedToWarmPool || !audit.ExecutedWorker.DestroyedAfterJob {
		t.Fatalf("one-way worker lifecycle is not proven: %#v", audit.ExecutedWorker)
	}
	if audit.SecretBoundary.InstallerShell != "/bin/bash" || audit.SecretBoundary.InheritedXtraceFD != 19 || audit.SecretBoundary.InheritedXtraceSink != "/dev/null" || audit.SecretBoundary.LiveGuestJournalJWTShapeMatches != 0 || audit.SecretBoundary.DiagnosticBundleJWTShapeMatches != 0 || audit.SecretBoundary.LocalDiagnosticBundlesScanned == 0 || audit.SecretBoundary.ExecutableRegressionTest != "TestRenderWarmAssignmentSuppressesInheritedXtrace" || audit.SecretBoundary.Scope == "" {
		t.Fatalf("xtrace secret boundary is not proven: %#v", audit.SecretBoundary)
	}
	if audit.Diagnostics.Archive == "" || !hex64.MatchString(audit.Diagnostics.SHA256) || audit.Diagnostics.SourceBundles == 0 || audit.Diagnostics.SourceBundles != audit.Diagnostics.ExportedBundles || audit.Diagnostics.PendingBundles != 0 || audit.Diagnostics.SourceBytes == 0 || audit.Diagnostics.SourceBytes != audit.Diagnostics.ExportedBytes || audit.Diagnostics.RustFSSyncState != "synchronized" {
		t.Fatalf("diagnostic durability is not proven: %#v", audit.Diagnostics)
	}
	requiredServices := []string{"garm", "gha-fleet-gateway", "gha-fleet-observer", "gha-warm-pool.timer", "gha-diagnostic-exporter.timer", "gha-rustfs", "gha-zot"}
	if !audit.Postconditions.ObserverHealthy || !audit.Postconditions.ObserverFresh || audit.Postconditions.JournalSchemaVersion != 2 || audit.Postconditions.JournalWarmReady != 1 || audit.Postconditions.JournalClaims != 0 || audit.Postconditions.IncusVisibleInstances != 1 || audit.Postconditions.IncusOrphans != 0 || audit.Postconditions.IncusMissingInstances != 0 || audit.Postconditions.RootFreePercent < 20 || audit.Postconditions.LegacyListeners != 12 {
		t.Fatalf("fleet postconditions are not healthy: %#v", audit.Postconditions)
	}
	for _, service := range requiredServices {
		if !slices.Contains(audit.Postconditions.ServicesActive, service) {
			t.Errorf("active-service proof is missing %q", service)
		}
	}
	if audit.LatencySample.Eligible || audit.LatencySample.Reason == "" {
		t.Fatalf("loaded-host security run must not be admitted as nominal latency evidence: %#v", audit.LatencySample)
	}
	if !audit.Verdict.InheritedXtraceSuppressed || !audit.Verdict.CallbackCredentialAbsentFromObservation || !audit.Verdict.WorkerDestroyed || !audit.Verdict.DiagnosticsExported || audit.Verdict.ProductionReliabilityGateComplete || audit.Verdict.RemainingGate == "" {
		t.Fatalf("audit verdict overclaims or omits its remaining gate: %#v", audit.Verdict)
	}
}
