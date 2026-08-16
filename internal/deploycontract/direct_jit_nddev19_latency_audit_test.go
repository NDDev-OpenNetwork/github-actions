package deploycontract

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDirectJITNDDev19LatencyAudit(t *testing.T) {
	raw, err := os.ReadFile("../../config/direct-jit-nddev19-latency-audit.json")
	if err != nil {
		t.Fatal(err)
	}
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Series        struct {
			Repository        string  `json:"repository"`
			Workflow          string  `json:"workflow"`
			Ref               string  `json:"ref"`
			HeadSHA           string  `json:"head_sha"`
			RunnerLabel       string  `json:"runner_label"`
			Server            string  `json:"server"`
			ProviderVersion   string  `json:"provider_version"`
			ProviderCommit    string  `json:"provider_commit"`
			Required          int     `json:"required_samples"`
			MaxLoad           float64 `json:"nominal_preflight_max_load_1"`
			LatencyDefinition string  `json:"latency_definition"`
			QuantileMethod    string  `json:"quantile_method"`
			Target            int     `json:"target_p95_milliseconds_exclusive"`
		} `json:"series"`
		Samples []struct {
			Index           int       `json:"index"`
			RunID           int64     `json:"workflow_run_id"`
			JobID           int64     `json:"job_id"`
			OpaqueJobID     string    `json:"opaque_job_id"`
			DispatchAt      time.Time `json:"dispatch_at"`
			CreatedAt       time.Time `json:"created_at"`
			StartedAt       time.Time `json:"started_at"`
			CompletedAt     time.Time `json:"completed_at"`
			AssignmentAt    time.Time `json:"garm_assignment_at"`
			RunnerSessionAt string    `json:"runner_session_at"`
			LatencyMS       int       `json:"latency_milliseconds"`
			Preflight       struct {
				Load          float64 `json:"load_1"`
				LegacyWorkers int     `json:"legacy_workers"`
			} `json:"preflight"`
			Runner            string `json:"runner_name"`
			Physical          string `json:"physical_instance"`
			Replacement       string `json:"replacement_instance"`
			DiagnosticArchive string `json:"diagnostic_archive"`
			DiagnosticSHA     string `json:"diagnostic_sha256"`
			TokenMatches      int    `json:"diagnostic_token_shape_matches"`
			DiagnosticsBefore int    `json:"diagnostics_before"`
			DiagnosticsAfter  int    `json:"diagnostics_after"`
			Post              struct {
				Healthy           bool   `json:"healthy"`
				QueueActive       int    `json:"queue_active"`
				QueueInFlight     int    `json:"queue_in_flight"`
				UncoveredRunning  int    `json:"queue_uncovered_running"`
				Claims            int    `json:"claims"`
				WarmReady         int    `json:"warm_ready"`
				Visible           int    `json:"visible_instances"`
				Orphans           int    `json:"orphan_instances"`
				Missing           int    `json:"missing_instances"`
				DiagnosticSync    string `json:"diagnostic_sync"`
				DiagnosticPending int    `json:"diagnostic_pending"`
				LegacyListeners   int    `json:"legacy_listeners"`
				ExamplePlatform   bool   `json:"example_platform_healthy"`
				Captcha           bool   `json:"captcha_healthy"`
				FailedUnits       int    `json:"failed_systemd_units"`
				Registrations     int    `json:"github_runner_registrations"`
			} `json:"postcondition"`
		} `json:"samples"`
		Statistics struct {
			Samples int  `json:"samples"`
			Minimum int  `json:"minimum_milliseconds"`
			Maximum int  `json:"maximum_milliseconds"`
			Median  int  `json:"median_milliseconds"`
			P95     int  `json:"p95_milliseconds"`
			Target  int  `json:"target_milliseconds_exclusive"`
			Passed  bool `json:"passed"`
		} `json:"statistics"`
		Postconditions struct {
			ObserverHealthy    bool `json:"observer_healthy"`
			QueueActive        int  `json:"queue_active"`
			QueueInFlight      int  `json:"queue_in_flight"`
			UncoveredRunning   int  `json:"queue_uncovered_running"`
			Claims             int  `json:"claims"`
			WarmReady          int  `json:"warm_ready"`
			Visible            int  `json:"visible_instances"`
			Orphans            int  `json:"orphan_instances"`
			Missing            int  `json:"missing_instances"`
			DiagnosticsSource  int  `json:"diagnostics_source"`
			DiagnosticsExport  int  `json:"diagnostics_exported"`
			DiagnosticsPending int  `json:"diagnostics_pending"`
			LegacyListeners    int  `json:"legacy_listeners"`
			ExamplePlatform    bool `json:"example_platform_healthy"`
			Captcha            bool `json:"captcha_healthy"`
			FailedUnits        int  `json:"failed_systemd_units"`
			Registrations      int  `json:"github_runner_registrations"`
		} `json:"postconditions"`
		Verdict struct {
			Complete bool `json:"complete"`
			P95Gate  bool `json:"warm_queue_to_online_p95_gate_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.SchemaVersion != 1 || audit.Series.Repository != "example-user/github-actions" ||
		audit.Series.Workflow != "self-hosted-canary.yml" || audit.Series.Ref != "main" ||
		audit.Series.HeadSHA != "9688c62ed225037f761acf4bcd14dbab79cb9e02" ||
		audit.Series.RunnerLabel != "nddev-linux-standard" || audit.Series.Server != "server-example-legacy" ||
		audit.Series.ProviderVersion != "v0.1.5-nddev.19" || audit.Series.ProviderCommit != "531c1c774fc071e534971bda9cadd46ceae39e26" ||
		audit.Series.Required != 20 || audit.Series.MaxLoad != 4 ||
		audit.Series.LatencyDefinition != "garm-assignment-to-runner-session-observed" || audit.Series.QuantileMethod != "nearest-rank" || audit.Series.Target != 5000 {
		t.Fatalf("series identity is invalid: %#v", audit.Series)
	}
	if len(audit.Samples) != audit.Series.Required {
		t.Fatalf("samples=%d, want %d", len(audit.Samples), audit.Series.Required)
	}

	runs := map[int64]struct{}{}
	jobs := map[int64]struct{}{}
	opaqueJobs := map[string]struct{}{}
	runners := map[string]struct{}{}
	physical := map[string]struct{}{}
	replacements := map[string]struct{}{}
	archives := map[string]struct{}{}
	digests := map[string]struct{}{}
	latencies := make([]int, 0, len(audit.Samples))
	for index, sample := range audit.Samples {
		sessionAt, err := time.Parse("2006-01-02 15:04:05Z", sample.RunnerSessionAt)
		if err != nil {
			t.Fatalf("sample %d runner session timestamp: %v", index+1, err)
		}
		observedLatency := int(sessionAt.Sub(sample.AssignmentAt) / time.Millisecond)
		if sample.Index != index+1 || sample.RunID <= 0 || sample.JobID <= 0 || sample.OpaqueJobID == "" ||
			!sample.DispatchAt.Before(sample.CreatedAt) || !sample.CreatedAt.Before(sample.AssignmentAt) ||
			!sample.AssignmentAt.Before(sessionAt) || !sessionAt.Before(sample.StartedAt) || !sample.StartedAt.Before(sample.CompletedAt) ||
			sample.LatencyMS != observedLatency || sample.Preflight.Load > audit.Series.MaxLoad || sample.Preflight.LegacyWorkers < 0 ||
			!strings.HasPrefix(sample.Runner, "nddev-") || !strings.HasPrefix(sample.Physical, "warm-standard-") ||
			!strings.HasPrefix(sample.Replacement, "warm-standard-") || sample.Physical == sample.Replacement ||
			!strings.HasPrefix(sample.DiagnosticArchive, "runner-diagnostics-v1-"+sample.Physical+"-") ||
			len(sample.DiagnosticSHA) != 64 || strings.Trim(sample.DiagnosticSHA, "0123456789abcdef") != "" || sample.TokenMatches != 0 ||
			sample.DiagnosticsAfter != sample.DiagnosticsBefore+1 {
			t.Fatalf("sample %d identity, chronology or evidence is invalid: %#v", index+1, sample)
		}
		if !sample.Post.Healthy || sample.Post.QueueActive != 0 || sample.Post.QueueInFlight != 0 || sample.Post.UncoveredRunning != 0 ||
			sample.Post.Claims != 0 || sample.Post.WarmReady != 1 || sample.Post.Visible != 1 || sample.Post.Orphans != 0 || sample.Post.Missing != 0 ||
			sample.Post.DiagnosticSync != "synchronized" || sample.Post.DiagnosticPending != 0 || sample.Post.LegacyListeners != 12 ||
			!sample.Post.ExamplePlatform || !sample.Post.Captcha || sample.Post.FailedUnits != 0 || sample.Post.Registrations != 0 {
			t.Fatalf("sample %d postcondition is not converged: %#v", index+1, sample.Post)
		}
		for label, present := range map[string]bool{
			"run": addUniqueInt64(runs, sample.RunID), "job": addUniqueInt64(jobs, sample.JobID),
			"opaque job": addUniqueString(opaqueJobs, sample.OpaqueJobID), "runner": addUniqueString(runners, sample.Runner),
			"physical": addUniqueString(physical, sample.Physical), "replacement": addUniqueString(replacements, sample.Replacement),
			"archive": addUniqueString(archives, sample.DiagnosticArchive), "digest": addUniqueString(digests, sample.DiagnosticSHA),
		} {
			if !present {
				t.Fatalf("sample %d reuses %s identity", index+1, label)
			}
		}
		if index > 0 {
			previous := audit.Samples[index-1]
			if previous.Replacement != sample.Physical || previous.DiagnosticsAfter != sample.DiagnosticsBefore {
				t.Fatalf("sample %d does not continue the clean replacement chain", index+1)
			}
		}
		latencies = append(latencies, sample.LatencyMS)
	}
	slices.Sort(latencies)
	median := latencies[9]
	p95 := latencies[18]
	if audit.Statistics.Samples != 20 || audit.Statistics.Minimum != latencies[0] || audit.Statistics.Maximum != latencies[19] ||
		audit.Statistics.Median != median || audit.Statistics.P95 != p95 || audit.Statistics.Target != audit.Series.Target ||
		audit.Statistics.Passed || median != 5320 || p95 != 6700 {
		t.Fatalf("nearest-rank statistics are invalid: %#v sorted=%v", audit.Statistics, latencies)
	}
	post := audit.Postconditions
	if !post.ObserverHealthy || post.QueueActive != 0 || post.QueueInFlight != 0 || post.UncoveredRunning != 0 || post.Claims != 0 ||
		post.WarmReady != 1 || post.Visible != 1 || post.Orphans != 0 || post.Missing != 0 ||
		post.DiagnosticsSource != 149 || post.DiagnosticsExport != post.DiagnosticsSource || post.DiagnosticsPending != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatform || !post.Captcha || post.FailedUnits != 0 || post.Registrations != 0 {
		t.Fatalf("final postconditions are invalid: %#v", post)
	}
	if !audit.Verdict.Complete || audit.Verdict.P95Gate {
		t.Fatalf("baseline verdict must preserve the failed performance gate: %#v", audit.Verdict)
	}
}

func addUniqueInt64(values map[int64]struct{}, value int64) bool {
	if _, exists := values[value]; exists {
		return false
	}
	values[value] = struct{}{}
	return true
}

func addUniqueString(values map[string]struct{}, value string) bool {
	if _, exists := values[value]; exists {
		return false
	}
	values[value] = struct{}{}
	return true
}
