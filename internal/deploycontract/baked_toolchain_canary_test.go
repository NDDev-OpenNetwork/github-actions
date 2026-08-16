package deploycontract

import (
	"strings"
	"testing"
)

// TestBakedToolchainCanary checks the runtime proof that ADR 0030 turned
// per-job toolchain installation into a no-op. It deliberately verifies only
// the Install toolchain step, because that is the one controlled comparison the
// canary makes; the surrounding steps differ by cache mode, host contention and
// repository growth, so a total-job-time claim would not be honest.
func TestBakedToolchainCanary(t *testing.T) {
	var audit struct {
		HeadSHA string `json:"head_sha"`
		Server  string `json:"server"`
		Image   struct {
			StandardAlias       string `json:"standard_alias"`
			StandardFingerprint string `json:"standard_fingerprint"`
		} `json:"image"`
		MeasuredStep string `json:"measured_step"`
		Comparison   map[string]struct {
			BeforeSeconds int    `json:"before_seconds"`
			AfterSeconds  int    `json:"after_seconds"`
			BeforeRun     int64  `json:"before_run"`
			AfterRun      int64  `json:"after_run"`
			BeforeLog     string `json:"before_log"`
			AfterLog      string `json:"after_log"`
			Mechanism     string `json:"mechanism"`
		} `json:"comparison"`
		Samples map[string]struct {
			WorkflowRunID int64  `json:"workflow_run_id"`
			RunnerName    string `json:"runner_name"`
			Conclusion    string `json:"conclusion"`
			Steps         []struct {
				Name    string `json:"name"`
				Seconds int    `json:"seconds"`
			} `json:"steps"`
		} `json:"samples"`
		Postconditions struct {
			ObserverHealthy   bool `json:"observer_healthy"`
			WarmReady         int  `json:"warm_ready"`
			Claims            int  `json:"claims"`
			QueueActive       int  `json:"queue_active"`
			OrphanInstances   int  `json:"orphan_instances"`
			MissingInstances  int  `json:"missing_instances"`
			DiagnosticPending int  `json:"diagnostics_pending"`
			LegacyListeners   int  `json:"legacy_listeners"`
			ExamplePlatform           bool `json:"example_platform_healthy"`
			Captcha           bool `json:"captcha_healthy"`
			FailedUnits       int  `json:"failed_systemd_units"`
			Registrations     int  `json:"github_runner_registrations"`
		} `json:"postconditions"`
	}
	readJSON(t, "../../config/baked-toolchain-canary-audit.json", &audit)

	if audit.Server != "server-example-legacy" || len(audit.HeadSHA) != 40 ||
		audit.MeasuredStep != "Install toolchain" ||
		audit.Image.StandardAlias != "nddev-ubuntu-24.04-amd64-runner-2.336.0-r20260801-b9" ||
		len(audit.Image.StandardFingerprint) != 64 {
		t.Fatalf("invalid canary identity: %+v", audit.Image)
	}
	if len(audit.Comparison) != 2 || len(audit.Samples) != 2 {
		t.Fatalf("expected a Go and a Rust sample, got %d comparisons and %d samples",
			len(audit.Comparison), len(audit.Samples))
	}

	for workload, comparison := range audit.Comparison {
		sample, ok := audit.Samples[workload]
		if !ok {
			t.Fatalf("comparison %q has no sample", workload)
		}
		if sample.Conclusion != "success" || sample.WorkflowRunID != comparison.AfterRun ||
			!strings.HasPrefix(sample.RunnerName, "nddev-") {
			t.Errorf("%s sample is not a successful disposable-worker run: %+v", workload, sample)
		}
		// Each canary ran against a different disposable worker than the other.
		for other, otherSample := range audit.Samples {
			if other != workload && otherSample.RunnerName == sample.RunnerName {
				t.Errorf("%s and %s reused runner %q", workload, other, sample.RunnerName)
			}
		}
		measured := -1
		for _, step := range sample.Steps {
			if step.Name == audit.MeasuredStep {
				measured = step.Seconds
			}
		}
		if measured < 0 {
			t.Fatalf("%s sample has no %q step", workload, audit.MeasuredStep)
		}
		if measured != comparison.AfterSeconds {
			t.Errorf("%s recorded %ds but its sample step took %ds",
				workload, comparison.AfterSeconds, measured)
		}
		// The claim is a no-op. GitHub reports step boundaries at one-second
		// resolution, so a genuine no-op reads as one or two seconds of step
		// overhead rather than zero. Require both: an absolute floor that only
		// a no-op can meet, and a large absolute saving against the baseline.
		if comparison.AfterSeconds > 3 {
			t.Errorf("%s toolchain install takes %ds, which is not a no-op",
				workload, comparison.AfterSeconds)
		}
		if comparison.BeforeSeconds-comparison.AfterSeconds < 15 {
			t.Errorf("%s toolchain install went %ds to %ds, too small a saving to claim the install was removed",
				workload, comparison.BeforeSeconds, comparison.AfterSeconds)
		}
		if comparison.BeforeRun == comparison.AfterRun || comparison.Mechanism == "" {
			t.Errorf("%s comparison must name two runs and the mechanism: %+v", workload, comparison)
		}
	}

	// The Go proof is textual as well as numeric: setup-go must report a cache
	// hit at the baked path rather than a download.
	goComparison := audit.Comparison["go"]
	if !strings.Contains(goComparison.AfterLog, "Found in cache @ /home/runner/actions-runner/_work/_tool/go/") ||
		strings.Contains(goComparison.AfterLog, "Attempting to download") {
		t.Errorf("setup-go did not report a tool cache hit: %q", goComparison.AfterLog)
	}
	if !strings.Contains(goComparison.BeforeLog, "Attempting to download") {
		t.Errorf("the baseline must show the download this change removes: %q", goComparison.BeforeLog)
	}

	post := audit.Postconditions
	if !post.ObserverHealthy || post.WarmReady != 1 || post.Claims != 0 || post.QueueActive != 0 ||
		post.OrphanInstances != 0 || post.MissingInstances != 0 || post.DiagnosticPending != 0 ||
		post.LegacyListeners != 12 || !post.ExamplePlatform || !post.Captcha ||
		post.FailedUnits != 0 || post.Registrations != 0 {
		t.Fatalf("the canary did not converge: %+v", post)
	}
}
