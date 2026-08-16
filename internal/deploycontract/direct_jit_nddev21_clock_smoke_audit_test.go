package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestDirectJITNDDev21ClockSafeSmokeAudit(t *testing.T) {
	raw, err := os.ReadFile("../../config/direct-jit-nddev21-clock-smoke-audit.json")
	if err != nil {
		t.Fatalf("read clock-safe smoke audit: %v", err)
	}
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Series        struct {
			Repository        string `json:"repository"`
			HeadSHA           string `json:"head_sha"`
			ProviderVersion   string `json:"provider_version"`
			ProviderCommit    string `json:"provider_commit"`
			RequiredSamples   int    `json:"required_samples"`
			LatencyDefinition string `json:"latency_definition"`
		} `json:"series"`
		Samples []struct {
			WorkflowRunID       int64     `json:"workflow_run_id"`
			CreatedAt           time.Time `json:"created_at"`
			StartedAt           time.Time `json:"started_at"`
			AssignmentAt        time.Time `json:"garm_assignment_at"`
			ProviderStartedAt   time.Time `json:"provider_started_at"`
			ProviderCompletedAt time.Time `json:"provider_completed_at"`
			LatencyMS           int64     `json:"latency_milliseconds"`
			PhaseDurations      struct {
				AssignmentToProviderStart    int64 `json:"assignment_to_provider_start_milliseconds"`
				ProviderCreate               int64 `json:"provider_create_milliseconds"`
				AssignmentToProviderComplete int64 `json:"assignment_to_provider_complete_milliseconds"`
				GuestAssignmentSetup         int64 `json:"guest_assignment_setup_milliseconds"`
			} `json:"phase_durations"`
			GuestClock struct {
				AssignmentStarted int64 `json:"assignment_script_started_unix_ns"`
				RunnerExec        int64 `json:"runner_exec_unix_ns"`
			} `json:"guest_phase_clock"`
			PhysicalInstance string `json:"physical_instance"`
			Replacement      string `json:"replacement_instance"`
			DiagnosticSHA    string `json:"diagnostic_sha256"`
			TokenMatches     int    `json:"diagnostic_token_shape_matches"`
			Postcondition    struct {
				Healthy           bool `json:"healthy"`
				Claims            int  `json:"claims"`
				Orphans           int  `json:"orphan_instances"`
				Missing           int  `json:"missing_instances"`
				LegacyListeners   int  `json:"legacy_listeners"`
				DiagnosticPending int  `json:"diagnostic_pending"`
				ExamplePlatform   bool `json:"example_platform_healthy"`
				Captcha           bool `json:"captcha_healthy"`
			} `json:"postcondition"`
		} `json:"samples"`
		Statistics struct {
			Samples int  `json:"samples"`
			P95     int  `json:"p95_milliseconds"`
			Target  int  `json:"target_milliseconds_exclusive"`
			Passed  bool `json:"passed"`
		} `json:"statistics"`
		Verdict struct {
			Complete bool `json:"complete"`
			Gate     bool `json:"warm_queue_to_online_p95_gate_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatalf("decode clock-safe smoke audit: %v", err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 2 || audit.Series.Repository != "example-user/github-actions" ||
		audit.Series.ProviderVersion != "v0.1.5-nddev.21" ||
		!hex40.MatchString(audit.Series.HeadSHA) || audit.Series.ProviderCommit != audit.Series.HeadSHA ||
		audit.Series.RequiredSamples != 1 || audit.Series.LatencyDefinition != "github-job-created-to-started-observed" ||
		len(audit.Samples) != 1 {
		t.Fatalf("invalid clock-safe series identity: %#v", audit.Series)
	}
	sample := audit.Samples[0]
	if sample.WorkflowRunID == 0 || sample.LatencyMS != sample.StartedAt.Sub(sample.CreatedAt).Milliseconds() ||
		sample.LatencyMS <= 0 || sample.AssignmentAt.After(sample.ProviderStartedAt) ||
		sample.ProviderStartedAt.After(sample.ProviderCompletedAt) {
		t.Fatalf("invalid authoritative or host phase chronology: %#v", sample)
	}
	if got := sample.ProviderStartedAt.Sub(sample.AssignmentAt).Milliseconds(); got != sample.PhaseDurations.AssignmentToProviderStart {
		t.Fatalf("assignment-to-provider duration = %d, want %d", sample.PhaseDurations.AssignmentToProviderStart, got)
	}
	if got := sample.ProviderCompletedAt.Sub(sample.AssignmentAt).Milliseconds(); got != sample.PhaseDurations.AssignmentToProviderComplete {
		t.Fatalf("assignment-to-provider-complete duration = %d, want %d", sample.PhaseDurations.AssignmentToProviderComplete, got)
	}
	if got := (sample.GuestClock.RunnerExec - sample.GuestClock.AssignmentStarted) / int64(time.Millisecond); got != sample.PhaseDurations.GuestAssignmentSetup || got < 0 {
		t.Fatalf("guest-local setup duration = %d, want %d", sample.PhaseDurations.GuestAssignmentSetup, got)
	}
	if sample.PhaseDurations.ProviderCreate < 0 || sample.PhysicalInstance == sample.Replacement ||
		!hex64.MatchString(sample.DiagnosticSHA) || sample.TokenMatches != 0 {
		t.Fatalf("invalid lifecycle or diagnostic evidence: %#v", sample)
	}
	if !sample.Postcondition.Healthy || sample.Postcondition.Claims != 0 || sample.Postcondition.Orphans != 0 ||
		sample.Postcondition.Missing != 0 || sample.Postcondition.LegacyListeners != 12 ||
		sample.Postcondition.DiagnosticPending != 0 || !sample.Postcondition.ExamplePlatform || !sample.Postcondition.Captcha {
		t.Fatalf("invalid postcondition: %#v", sample.Postcondition)
	}
	if !audit.Verdict.Complete || audit.Verdict.Gate || audit.Statistics.Samples != 1 ||
		audit.Statistics.Passed || audit.Statistics.P95 < audit.Statistics.Target {
		t.Fatalf("smoke must complete while leaving the statistical gate open: statistics=%#v verdict=%#v", audit.Statistics, audit.Verdict)
	}
}
