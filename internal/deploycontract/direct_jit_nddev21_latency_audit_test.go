package deploycontract

import (
	"encoding/json"
	"os"
	"regexp"
	"testing"
	"time"
)

func TestDirectJITNDDev21LatencyAudit(t *testing.T) {
	raw, err := os.ReadFile("../../config/direct-jit-nddev21-latency-audit.json")
	if err != nil {
		t.Fatalf("read nddev21 latency audit: %v", err)
	}
	var audit struct {
		SchemaVersion int `json:"schema_version"`
		Series        struct {
			HeadSHA         string `json:"head_sha"`
			ProviderVersion string `json:"provider_version"`
			ProviderCommit  string `json:"provider_commit"`
			RequiredSamples int    `json:"required_samples"`
			Definition      string `json:"latency_definition"`
		} `json:"series"`
		Samples []struct {
			Index        int       `json:"index"`
			CreatedAt    time.Time `json:"created_at"`
			StartedAt    time.Time `json:"started_at"`
			LatencyMS    int64     `json:"latency_milliseconds"`
			Physical     string    `json:"physical_instance"`
			Replacement  string    `json:"replacement_instance"`
			Diagnostic   string    `json:"diagnostic_sha256"`
			TokenMatches int       `json:"diagnostic_token_shape_matches"`
			Post         struct {
				Healthy       bool `json:"healthy"`
				Claims        int  `json:"claims"`
				Orphans       int  `json:"orphan_instances"`
				Missing       int  `json:"missing_instances"`
				Pending       int  `json:"diagnostic_pending"`
				Registrations int  `json:"github_runner_registrations"`
				Legacy        int  `json:"legacy_listeners"`
				ExamplePlatform       bool `json:"example_platform_healthy"`
				Captcha       bool `json:"captcha_healthy"`
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
			Healthy       bool `json:"observer_healthy"`
			QueueActive   int  `json:"queue_active"`
			QueueInFlight int  `json:"queue_in_flight"`
			Uncovered     int  `json:"queue_uncovered_running"`
			Claims        int  `json:"claims"`
			WarmReady     int  `json:"warm_ready"`
			Visible       int  `json:"visible_instances"`
			Orphans       int  `json:"orphan_instances"`
			Missing       int  `json:"missing_instances"`
			Source        int  `json:"diagnostics_source"`
			Exported      int  `json:"diagnostics_exported"`
			Pending       int  `json:"diagnostics_pending"`
			Legacy        int  `json:"legacy_listeners"`
			ExamplePlatform       bool `json:"example_platform_healthy"`
			Captcha       bool `json:"captcha_healthy"`
			FailedUnits   int  `json:"failed_systemd_units"`
			Registrations int  `json:"github_runner_registrations"`
		} `json:"postconditions"`
		Verdict struct {
			Complete bool `json:"complete"`
			Gate     bool `json:"warm_queue_to_online_p95_gate_complete"`
		} `json:"verdict"`
	}
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatalf("decode nddev21 latency audit: %v", err)
	}
	hex40 := regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if audit.SchemaVersion != 2 || !hex40.MatchString(audit.Series.HeadSHA) ||
		!hex40.MatchString(audit.Series.ProviderCommit) || audit.Series.HeadSHA == audit.Series.ProviderCommit ||
		audit.Series.ProviderVersion != "v0.1.5-nddev.21" || audit.Series.RequiredSamples != 20 ||
		audit.Series.Definition != "github-job-created-to-started-observed" || len(audit.Samples) != 20 {
		t.Fatalf("invalid series identity: %#v", audit.Series)
	}
	physical := make(map[string]bool, 20)
	for i, sample := range audit.Samples {
		if sample.Index != i+1 || sample.LatencyMS != sample.StartedAt.Sub(sample.CreatedAt).Milliseconds() ||
			sample.Physical == sample.Replacement || physical[sample.Physical] ||
			!hex64.MatchString(sample.Diagnostic) || sample.TokenMatches != 0 || !sample.Post.Healthy ||
			sample.Post.Claims != 0 || sample.Post.Orphans != 0 || sample.Post.Missing != 0 ||
			sample.Post.Pending != 0 || sample.Post.Registrations != 0 || sample.Post.Legacy != 12 ||
			!sample.Post.ExamplePlatform || !sample.Post.Captcha {
			t.Fatalf("invalid sample %d: %#v", i+1, sample)
		}
		physical[sample.Physical] = true
	}
	if audit.Statistics.Samples != 20 || audit.Statistics.Minimum != 6000 ||
		audit.Statistics.Maximum != 8000 || audit.Statistics.Median != 7000 ||
		audit.Statistics.P95 != 7000 || audit.Statistics.Target != 5000 || audit.Statistics.Passed ||
		!audit.Verdict.Complete || audit.Verdict.Gate {
		t.Fatalf("invalid honest performance verdict: statistics=%#v verdict=%#v", audit.Statistics, audit.Verdict)
	}
	post := audit.Postconditions
	if !post.Healthy || post.QueueActive != 0 || post.QueueInFlight != 0 || post.Uncovered != 0 ||
		post.Claims != 0 || post.WarmReady != 1 || post.Visible != 1 || post.Orphans != 0 ||
		post.Missing != 0 || post.Source != post.Exported || post.Pending != 0 || post.Legacy != 12 ||
		!post.ExamplePlatform || !post.Captcha || post.FailedUnits != 0 || post.Registrations != 0 {
		t.Fatalf("invalid final postconditions: %#v", post)
	}
}
