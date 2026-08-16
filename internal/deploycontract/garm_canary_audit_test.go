package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

const garmCanaryAuditPath = "../../config/garm-event-driven-canary-audit.json"

type garmCanaryAudit struct {
	SchemaVersion         int       `json:"schema_version"`
	CapturedAt            time.Time `json:"captured_at"`
	Host                  string    `json:"host"`
	RepositoryMergeCommit string    `json:"repository_merge_commit"`
	GARM                  struct {
		Version              string    `json:"version"`
		UpstreamCommit       string    `json:"upstream_commit"`
		PatchSHA256          string    `json:"patch_sha256"`
		BinarySHA256         string    `json:"binary_sha256"`
		MaximumRequiredGLIBC string    `json:"maximum_required_glibc"`
		ServerGLIBC          string    `json:"server_glibc"`
		ServiceStartedAt     time.Time `json:"service_started_at"`
		SystemdRestarts      int       `json:"systemd_restarts"`
		WarningsAfterDeploy  int       `json:"warnings_after_deploy"`
	} `json:"garm"`
	Rollback struct {
		Version      string `json:"version"`
		BinarySHA256 string `json:"binary_sha256"`
		Path         string `json:"path"`
		Retained     bool   `json:"retained"`
	} `json:"rollback"`
	Workflow struct {
		RunID                    int64     `json:"run_id"`
		JobID                    int64     `json:"job_id"`
		HeadSHA                  string    `json:"head_sha"`
		CreatedAt                time.Time `json:"created_at"`
		GARMAssignmentObservedAt time.Time `json:"garm_assignment_observed_at"`
		FirstMetadataRequestAt   time.Time `json:"first_metadata_request_at"`
		RunnerSessionCreatedAt   time.Time `json:"runner_session_created_at"`
		RunnerListeningAt        time.Time `json:"runner_listening_at"`
		StartedAt                time.Time `json:"started_at"`
		CompletedAt              time.Time `json:"completed_at"`
		Conclusion               string    `json:"conclusion"`
		RunnerName               string    `json:"runner_name"`
		RunnerGroup              string    `json:"runner_group"`
		Label                    string    `json:"label"`
	} `json:"workflow"`
	Latency struct {
		CreatedToGARMAssignment              float64 `json:"created_to_garm_assignment"`
		GARMAssignmentToFirstMetadata        float64 `json:"garm_assignment_to_first_metadata"`
		GARMAssignmentToRunnerListeningUpper float64 `json:"garm_assignment_to_runner_listening_upper_bound"`
		RunnerListeningToJobStartUpper       float64 `json:"runner_listening_to_job_start_upper_bound"`
		CreatedToJobStart                    float64 `json:"created_to_job_start"`
		JobExecution                         float64 `json:"job_execution"`
		PreviousCreatedToJobStart            float64 `json:"previous_created_to_job_start"`
		CreatedToJobStartImprovementPercent  float64 `json:"created_to_job_start_improvement_percent"`
	} `json:"latency_seconds"`
	Worker struct {
		InstanceName               string `json:"instance_name"`
		DestroyedAfterJob          bool   `json:"destroyed_after_job"`
		ReturnedToWarmPool         bool   `json:"returned_to_warm_pool"`
		DiagnosticsArchive         string `json:"diagnostics_archive"`
		DiagnosticsSHA256          string `json:"diagnostics_sha256"`
		DiagnosticsJWTShapeMatches int    `json:"diagnostics_jwt_shape_matches"`
	} `json:"worker"`
	Replacement struct {
		InstanceName string    `json:"instance_name"`
		AdmittedAt   time.Time `json:"admitted_at"`
		Lifecycle    string    `json:"lifecycle"`
		WarmReady    bool      `json:"warm_ready"`
		Claims       int       `json:"claims"`
	} `json:"replacement"`
	Postconditions struct {
		ObserverHealthy                  bool     `json:"observer_healthy"`
		ObserverFresh                    bool     `json:"observer_fresh"`
		JournalSchemaVersion             int      `json:"journal_schema_version"`
		JournalWarmReady                 int      `json:"journal_warm_ready"`
		JournalClaims                    int      `json:"journal_claims"`
		IncusVisibleInstances            int      `json:"incus_visible_instances"`
		IncusOrphans                     int      `json:"incus_orphans"`
		IncusMissingInstances            int      `json:"incus_missing_instances"`
		DiagnosticBundles                int      `json:"diagnostic_bundles"`
		DiagnosticExportedBundles        int      `json:"diagnostic_exported_bundles"`
		DiagnosticPendingBundles         int      `json:"diagnostic_pending_bundles"`
		RootFreePercent                  int      `json:"root_free_percent"`
		LegacyListeners                  int      `json:"legacy_listeners"`
		ExamplePlatformContainersHealthy bool     `json:"example_platform_containers_healthy"`
		CaptchaContainersHealthy         bool     `json:"captcha_containers_healthy"`
		ServicesActive                   []string `json:"services_active"`
	} `json:"postconditions"`
	Verdict struct {
		EventDrivenDerivativeOperational bool   `json:"event_driven_derivative_operational"`
		PreviousBinaryRollbackReady      bool   `json:"previous_binary_rollback_ready"`
		SingleSampleImproved             bool   `json:"single_sample_improved"`
		WarmP95GateComplete              bool   `json:"warm_queue_to_online_p95_gate_complete"`
		ReliabilityGateComplete          bool   `json:"production_reliability_gate_complete"`
		NextMeasuredBottleneck           string `json:"next_measured_bottleneck"`
		RemainingGate                    string `json:"remaining_gate"`
	} `json:"verdict"`
}

func TestGARMEventDrivenCanaryProvesBoundedDeployment(t *testing.T) {
	source, err := os.ReadFile(garmCanaryAuditPath)
	if err != nil {
		t.Fatalf("read GARM canary audit: %v", err)
	}
	var audit garmCanaryAudit
	if err := json.Unmarshal(source, &audit); err != nil {
		t.Fatalf("decode GARM canary audit: %v", err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 1 || audit.CapturedAt.IsZero() || audit.Host != "server-example-legacy" || !hex40.MatchString(audit.RepositoryMergeCommit) {
		t.Fatalf("invalid audit identity: %#v", audit)
	}
	if audit.GARM.Version != "v0.2.1-nddev.1" || !hex40.MatchString(audit.GARM.UpstreamCommit) || !hex64.MatchString(audit.GARM.PatchSHA256) || !hex64.MatchString(audit.GARM.BinarySHA256) || audit.GARM.MaximumRequiredGLIBC != "2.34" || audit.GARM.ServerGLIBC != "2.39" || audit.GARM.ServiceStartedAt.IsZero() || audit.GARM.SystemdRestarts != 0 || audit.GARM.WarningsAfterDeploy != 0 {
		t.Fatalf("invalid deployed GARM proof: %#v", audit.GARM)
	}
	if audit.Rollback.Version != "v0.2.1" || !hex64.MatchString(audit.Rollback.BinarySHA256) || audit.Rollback.Path == "" || !audit.Rollback.Retained {
		t.Fatalf("rollback is not proven: %#v", audit.Rollback)
	}
	if audit.Workflow.RunID == 0 || audit.Workflow.JobID == 0 || audit.Workflow.HeadSHA != audit.RepositoryMergeCommit || audit.Workflow.Conclusion != "success" || audit.Workflow.RunnerName == "" || audit.Workflow.RunnerGroup != "Default" || audit.Workflow.Label != "nddev-linux-standard" {
		t.Fatalf("workflow identity is incomplete: %#v", audit.Workflow)
	}
	timestamps := []time.Time{audit.Workflow.CreatedAt, audit.Workflow.GARMAssignmentObservedAt, audit.Workflow.FirstMetadataRequestAt, audit.Workflow.RunnerSessionCreatedAt, audit.Workflow.RunnerListeningAt, audit.Workflow.StartedAt, audit.Workflow.CompletedAt}
	for index := 1; index < len(timestamps); index++ {
		if timestamps[index].Before(timestamps[index-1]) {
			t.Fatalf("workflow timestamps are not monotonic: %v", timestamps)
		}
	}
	if audit.Latency.CreatedToJobStart != 12 || audit.Latency.PreviousCreatedToJobStart != 19 || audit.Latency.CreatedToJobStartImprovementPercent <= 0 || audit.Latency.GARMAssignmentToRunnerListeningUpper <= 5 || audit.Latency.JobExecution <= 0 {
		t.Fatalf("latency evidence is inconsistent or overclaims the p95 gate: %#v", audit.Latency)
	}
	if audit.Worker.InstanceName == "" || !audit.Worker.DestroyedAfterJob || audit.Worker.ReturnedToWarmPool || audit.Worker.DiagnosticsArchive == "" || !hex64.MatchString(audit.Worker.DiagnosticsSHA256) || audit.Worker.DiagnosticsJWTShapeMatches != 0 {
		t.Fatalf("used worker teardown is not proven: %#v", audit.Worker)
	}
	if audit.Replacement.InstanceName == "" || audit.Replacement.InstanceName == audit.Worker.InstanceName || audit.Replacement.AdmittedAt.IsZero() || audit.Replacement.Lifecycle != "warm-unregistered" || !audit.Replacement.WarmReady || audit.Replacement.Claims != 0 {
		t.Fatalf("clean replacement is not proven: %#v", audit.Replacement)
	}
	post := audit.Postconditions
	if !post.ObserverHealthy || !post.ObserverFresh || post.JournalSchemaVersion != 2 || post.JournalWarmReady != 1 || post.JournalClaims != 0 || post.IncusVisibleInstances != 1 || post.IncusOrphans != 0 || post.IncusMissingInstances != 0 || post.DiagnosticBundles != post.DiagnosticExportedBundles || post.DiagnosticPendingBundles != 0 || post.RootFreePercent < 20 || post.LegacyListeners != 12 || !post.ExamplePlatformContainersHealthy || !post.CaptchaContainersHealthy {
		t.Fatalf("fleet postconditions are not healthy: %#v", post)
	}
	for _, service := range []string{"garm", "gha-fleet-gateway", "gha-fleet-observer", "gha-warm-pool.timer", "gha-diagnostic-exporter.timer", "gha-rustfs", "gha-zot"} {
		if !slices.Contains(post.ServicesActive, service) {
			t.Errorf("active-service proof is missing %q", service)
		}
	}
	if !audit.Verdict.EventDrivenDerivativeOperational || !audit.Verdict.PreviousBinaryRollbackReady || !audit.Verdict.SingleSampleImproved || audit.Verdict.WarmP95GateComplete || audit.Verdict.ReliabilityGateComplete || audit.Verdict.NextMeasuredBottleneck == "" || audit.Verdict.RemainingGate == "" {
		t.Fatalf("audit verdict overclaims or omits remaining work: %#v", audit.Verdict)
	}
}
