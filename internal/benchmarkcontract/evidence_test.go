package benchmarkcontract

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const pilotEvidencePath = "../../benchmark/evidence/phase0-pilots-2026-08-09.json"

type pilotEvidence struct {
	SchemaVersion        int                 `json:"schema_version"`
	Kind                 string              `json:"kind"`
	RecordedAt           time.Time           `json:"recorded_at"`
	Repository           string              `json:"repository"`
	BenchmarkHeadSHA     string              `json:"benchmark_head_sha"`
	CollectorCommit      string              `json:"collector_commit"`
	CollectorMergeCommit string              `json:"collector_merge_commit"`
	Scope                pilotScope          `json:"scope"`
	ServerPostcondition  serverPostcondition `json:"server_postcondition"`
	Findings             []pilotFinding      `json:"findings"`
	Comparisons          []pilotComparison   `json:"comparisons"`
	Runs                 []pilotRun          `json:"runs"`
}

type pilotScope struct {
	Classification                  string      `json:"classification"`
	PilotProtocolComplete           bool        `json:"pilot_protocol_complete"`
	Phase0StatisticalGateComplete   bool        `json:"phase0_statistical_gate_complete"`
	RequiredRunsPerEnvironment      sampleCount `json:"required_statistical_runs_per_environment"`
	ObservedPilotRunsPerEnvironment pilotCount  `json:"observed_pilot_runs_per_environment"`
	Note                            string      `json:"note"`
}

type sampleCount struct {
	Cold         int `json:"cold"`
	WarmCacheHit int `json:"warm_cache_hit"`
}

type pilotCount struct {
	Cold              int `json:"cold"`
	WarmPrimeExcluded int `json:"warm_prime_excluded"`
	WarmCacheHit      int `json:"warm_cache_hit"`
}

type serverPostcondition struct {
	CapturedAt                          time.Time `json:"captured_at"`
	ObserverHealthy                     bool      `json:"observer_healthy"`
	CollectionErrors                    int       `json:"collection_errors"`
	VisibleInstances                    int       `json:"visible_instances"`
	OrphanInstances                     int       `json:"orphan_instances"`
	MissingInstances                    int       `json:"missing_instances"`
	JournalLeases                       int       `json:"journal_leases"`
	GitHubRegisteredRunners             int       `json:"github_registered_runners"`
	DiagnosticSourceBundles             int       `json:"diagnostic_source_bundles"`
	DiagnosticExportedBundles           int       `json:"diagnostic_exported_bundles"`
	DiagnosticPendingBundles            int       `json:"diagnostic_pending_bundles"`
	DiagnosticExportConsecutiveFailures int       `json:"diagnostic_export_consecutive_failures"`
	RootFilesystemFreePercent           int       `json:"root_filesystem_free_percent"`
	LegacyRunnerListeners               int       `json:"legacy_runner_listeners"`
	LegacyRunnerWorkers                 int       `json:"legacy_runner_workers"`
	FailedSystemdUnits                  int       `json:"failed_systemd_units"`
	ExamplePlatformHTTPStatus                   int       `json:"example_platform_http_status"`
	CaptchaHTTPStatus                   int       `json:"captcha_http_status"`
}

type pilotFinding struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Statement string `json:"statement"`
	Evidence  string `json:"evidence"`
}

type pilotComparison struct {
	Environment      string  `json:"environment"`
	Workload         string  `json:"workload"`
	ColdElapsedNS    int64   `json:"cold_elapsed_ns"`
	WarmHitElapsedNS int64   `json:"warm_hit_elapsed_ns"`
	WarmToColdRatio  float64 `json:"warm_to_cold_ratio"`
}

type pilotRun struct {
	Role                              string        `json:"role"`
	IncludedInPilotComparison         bool          `json:"included_in_pilot_comparison"`
	IncludedInPhase0StatisticalSeries bool          `json:"included_in_phase0_statistical_series"`
	Workflow                          pilotWorkflow `json:"workflow"`
	Sample                            pilotSample   `json:"sample"`
	Summary                           pilotSummary  `json:"summary"`
	Jobs                              []pilotJob    `json:"jobs"`
}

type pilotWorkflow struct {
	RunID      int64     `json:"run_id"`
	RunAttempt int64     `json:"run_attempt"`
	HeadSHA    string    `json:"head_sha"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type pilotSample struct {
	Environment string `json:"environment"`
	CacheMode   string `json:"cache_mode"`
	CacheHit    string `json:"cache_hit"`
	Iteration   string `json:"iteration"`
}

type pilotSummary struct {
	JobCount              int   `json:"job_count"`
	UniqueRunnerNames     int   `json:"unique_runner_names"`
	UniqueMachineIDHashes int   `json:"unique_machine_id_hashes"`
	RunDurationMS         int64 `json:"run_duration_ms"`
	MaximumQueueToStartMS int64 `json:"maximum_queue_to_start_ms"`
	TotalArtifactBytes    int64 `json:"total_artifact_bytes"`
}

type pilotJob struct {
	Workload       string        `json:"workload"`
	RunnerName     string        `json:"runner_name"`
	Labels         []string      `json:"labels"`
	QueueToStartMS int64         `json:"queue_to_start_ms"`
	JobDurationMS  int64         `json:"job_duration_ms"`
	Phases         []pilotPhase  `json:"phases"`
	Metrics        pilotMetrics  `json:"metrics"`
	Artifact       pilotArtifact `json:"artifact"`
}

type pilotPhase struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	DurationMS int64  `json:"duration_ms"`
}

type pilotMetrics struct {
	Toolchain       string `json:"toolchain"`
	MachineIDSHA256 string `json:"machine_id_sha256"`
	ElapsedNS       int64  `json:"elapsed_ns"`
	NetworkRXBytes  int64  `json:"network_rx_bytes"`
}

type pilotArtifact struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func TestPhase0PilotEvidenceIsExactCoherentAndExplicitlyIncomplete(t *testing.T) {
	evidence := readPilotEvidence(t)
	const benchmarkSHA = "ec1e3a1d0e3f6dd8b159c5822e1db6e0d1956544"
	if evidence.SchemaVersion != 1 || evidence.Kind != "phase0_pilot_evidence" ||
		evidence.Repository != "example-user/github-actions" || evidence.BenchmarkHeadSHA != benchmarkSHA {
		t.Fatalf("unexpected pilot evidence identity: %#v", evidence)
	}
	if evidence.CollectorCommit != "01ba7539ab9f0e8bcbb46071bd2b3587a7050638" ||
		evidence.CollectorMergeCommit != "db113334032074c1bf4f691b50c6f9bca5f10c23" || evidence.RecordedAt.IsZero() {
		t.Fatalf("collector provenance is incomplete: %#v", evidence)
	}
	if evidence.Scope.Classification != "pilot" || !evidence.Scope.PilotProtocolComplete ||
		evidence.Scope.Phase0StatisticalGateComplete || evidence.Scope.RequiredRunsPerEnvironment != (sampleCount{Cold: 20, WarmCacheHit: 20}) ||
		evidence.Scope.ObservedPilotRunsPerEnvironment != (pilotCount{Cold: 1, WarmPrimeExcluded: 1, WarmCacheHit: 1}) ||
		!strings.Contains(evidence.Scope.Note, "excluded") {
		t.Fatalf("pilot scope could be mistaken for the statistical gate: %#v", evidence.Scope)
	}

	expectedRuns := map[int64]struct {
		environment string
		cacheMode   string
		cacheHit    string
		role        string
		durationMS  int64
		maxQueueMS  int64
	}{
		31285558405: {"github-hosted", "cold", "disabled", "cold_pilot", 103000, 3000},
		31286086226: {"github-hosted", "warm", "false", "warm_prime", 95000, 4000},
		31286171098: {"github-hosted", "warm", "true", "warm_hit_pilot", 33000, 5000},
		31285673882: {"nddev", "cold", "disabled", "cold_pilot", 606000, 504000},
		31286214729: {"nddev", "warm", "false", "warm_prime", 612000, 573000},
		31286637765: {"nddev", "warm", "true", "warm_hit_pilot", 667000, 568000},
	}
	if len(evidence.Runs) != len(expectedRuns) {
		t.Fatalf("expected six exact pilot runs, got %d", len(evidence.Runs))
	}
	seenRuns := make(map[int64]bool, len(evidence.Runs))
	comparisonValues := make(map[string][2]int64)
	for _, run := range evidence.Runs {
		expected, exists := expectedRuns[run.Workflow.RunID]
		if !exists || seenRuns[run.Workflow.RunID] {
			t.Fatalf("unexpected or duplicate run %d", run.Workflow.RunID)
		}
		seenRuns[run.Workflow.RunID] = true
		if run.Workflow.RunAttempt != 1 || run.Workflow.HeadSHA != benchmarkSHA ||
			run.Workflow.CreatedAt.IsZero() || !run.Workflow.UpdatedAt.After(run.Workflow.CreatedAt) ||
			!strings.HasSuffix(run.Workflow.HTMLURL, fmt.Sprint(run.Workflow.RunID)) {
			t.Errorf("run %d workflow identity is incoherent: %#v", run.Workflow.RunID, run.Workflow)
		}
		if run.Sample.Environment != expected.environment || run.Sample.CacheMode != expected.cacheMode ||
			run.Sample.CacheHit != expected.cacheHit || run.Role != expected.role ||
			run.Summary.RunDurationMS != expected.durationMS || run.Summary.MaximumQueueToStartMS != expected.maxQueueMS {
			t.Errorf("run %d does not match its declared sample: %#v", run.Workflow.RunID, run)
		}
		isPrime := run.Role == "warm_prime"
		if run.IncludedInPilotComparison == isPrime || run.IncludedInPhase0StatisticalSeries {
			t.Errorf("run %d has unsafe inclusion flags", run.Workflow.RunID)
		}
		validatePilotJobs(t, run)
		if run.IncludedInPilotComparison {
			for _, job := range run.Jobs {
				key := run.Sample.Environment + "/" + job.Workload
				value := comparisonValues[key]
				if run.Sample.CacheMode == "cold" {
					value[0] = job.Metrics.ElapsedNS
				} else {
					value[1] = job.Metrics.ElapsedNS
				}
				comparisonValues[key] = value
			}
		}
	}
	validatePilotComparisons(t, evidence.Comparisons, comparisonValues)
	validatePilotPostcondition(t, evidence.ServerPostcondition)
	validatePilotFindings(t, evidence.Findings)
}

func readPilotEvidence(t *testing.T) pilotEvidence {
	t.Helper()
	file, err := os.Open(pilotEvidencePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var evidence pilotEvidence
	if err := decoder.Decode(&evidence); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("pilot evidence contains trailing JSON: %v", err)
	}
	return evidence
}

func validatePilotJobs(t *testing.T, run pilotRun) {
	t.Helper()
	expectedWorkloads := []string{"bun-next", "docker", "go", "python-uv", "rust"}
	if run.Summary.JobCount != 5 || run.Summary.UniqueRunnerNames != 5 || len(run.Jobs) != 5 || run.Summary.TotalArtifactBytes <= 0 {
		t.Errorf("run %d summary is incomplete: %#v", run.Workflow.RunID, run.Summary)
	}
	expectedMachineCount := 1
	if run.Sample.Environment == "nddev" {
		expectedMachineCount = 5
	}
	if run.Summary.UniqueMachineIDHashes != expectedMachineCount {
		t.Errorf("run %d machine uniqueness is %d, expected %d", run.Workflow.RunID, run.Summary.UniqueMachineIDHashes, expectedMachineCount)
	}
	workloads := make([]string, 0, len(run.Jobs))
	runners := make(map[string]bool, len(run.Jobs))
	machines := make(map[string]bool, len(run.Jobs))
	artifactBytes := int64(0)
	for _, job := range run.Jobs {
		workloads = append(workloads, job.Workload)
		if job.RunnerName == "" || runners[job.RunnerName] {
			t.Errorf("run %d contains an empty or reused runner name %q", run.Workflow.RunID, job.RunnerName)
		}
		runners[job.RunnerName] = true
		machines[job.Metrics.MachineIDSHA256] = true
		if job.QueueToStartMS < 0 || job.JobDurationMS <= 0 || job.Metrics.ElapsedNS <= 0 || job.Metrics.NetworkRXBytes < 0 || job.Metrics.Toolchain == "" {
			t.Errorf("run %d/%s has invalid metrics: %#v", run.Workflow.RunID, job.Workload, job)
		}
		label := "ubuntu-24.04"
		if run.Sample.Environment == "nddev" {
			label = "nddev-linux-standard"
			if job.Workload == "docker" {
				label = "nddev-linux-integration"
			}
		}
		if !slices.Contains(job.Labels, label) {
			t.Errorf("run %d/%s is missing label %q: %#v", run.Workflow.RunID, job.Workload, label, job.Labels)
		}
		validatePilotPhases(t, run, job)
		if job.Artifact.ID <= 0 || job.Artifact.SizeBytes <= 0 || job.Artifact.CreatedAt.IsZero() || !job.Artifact.ExpiresAt.After(job.Artifact.CreatedAt) ||
			!regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(job.Artifact.Digest) ||
			!strings.Contains(job.Artifact.Name, run.Sample.Iteration) || !strings.HasSuffix(job.Artifact.Name, job.Workload) {
			t.Errorf("run %d/%s artifact evidence is invalid: %#v", run.Workflow.RunID, job.Workload, job.Artifact)
		}
		artifactBytes += job.Artifact.SizeBytes
	}
	slices.Sort(workloads)
	if !slices.Equal(workloads, expectedWorkloads) || len(runners) != 5 || len(machines) != expectedMachineCount || artifactBytes != run.Summary.TotalArtifactBytes {
		t.Errorf("run %d aggregate evidence drifted: workloads=%v runners=%d machines=%d artifact_bytes=%d", run.Workflow.RunID, workloads, len(runners), len(machines), artifactBytes)
	}
}

func validatePilotPhases(t *testing.T, run pilotRun, job pilotJob) {
	t.Helper()
	required := map[string]bool{
		"Check out source": false, "Restore dependency cache": false, "Resolve dependencies": false,
		"Build workload": false, "Test workload": false, "Upload benchmark record": false,
	}
	seen := make(map[string]bool, len(job.Phases))
	for _, phase := range job.Phases {
		if seen[phase.Name] || phase.DurationMS < 0 || (phase.Conclusion != "success" && phase.Conclusion != "skipped") {
			t.Errorf("run %d/%s has invalid phase %#v", run.Workflow.RunID, job.Workload, phase)
		}
		seen[phase.Name] = true
		if _, exists := required[phase.Name]; exists {
			required[phase.Name] = true
		}
	}
	for name, present := range required {
		if !present {
			t.Errorf("run %d/%s is missing phase %q", run.Workflow.RunID, job.Workload, name)
		}
	}
}

func validatePilotComparisons(t *testing.T, comparisons []pilotComparison, values map[string][2]int64) {
	t.Helper()
	if len(comparisons) != 10 || len(values) != 10 {
		t.Fatalf("expected ten environment/workload comparisons, got %d and %d", len(comparisons), len(values))
	}
	seen := make(map[string]bool, len(comparisons))
	for _, comparison := range comparisons {
		key := comparison.Environment + "/" + comparison.Workload
		value, exists := values[key]
		if !exists || seen[key] || value[0] <= 0 || value[1] <= 0 {
			t.Errorf("unexpected comparison %q: %#v", key, comparison)
			continue
		}
		seen[key] = true
		ratio := math.Round((float64(value[1])/float64(value[0]))*1e6) / 1e6
		if comparison.ColdElapsedNS != value[0] || comparison.WarmHitElapsedNS != value[1] || comparison.WarmToColdRatio != ratio {
			t.Errorf("comparison %q is not derived from the run records: %#v", key, comparison)
		}
	}
}

func validatePilotPostcondition(t *testing.T, post serverPostcondition) {
	t.Helper()
	if post.CapturedAt.IsZero() || !post.ObserverHealthy || post.CollectionErrors != 0 || post.VisibleInstances != 0 ||
		post.OrphanInstances != 0 || post.MissingInstances != 0 || post.JournalLeases != 0 || post.GitHubRegisteredRunners != 0 ||
		post.DiagnosticSourceBundles != 38 || post.DiagnosticExportedBundles != 38 || post.DiagnosticPendingBundles != 0 ||
		post.DiagnosticExportConsecutiveFailures != 0 || post.RootFilesystemFreePercent < 20 || post.LegacyRunnerListeners != 12 ||
		post.FailedSystemdUnits != 0 || post.ExamplePlatformHTTPStatus != 200 || post.CaptchaHTTPStatus != 200 {
		t.Errorf("post-pilot server state is unsafe or incomplete: %#v", post)
	}
}

func validatePilotFindings(t *testing.T, findings []pilotFinding) {
	t.Helper()
	expected := []string{"P0-PILOT-01", "P0-PILOT-02", "P0-PILOT-03", "P0-PILOT-04"}
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.ID)
		if finding.Statement == "" || finding.Evidence == "" || (finding.Status != "measured" && finding.Status != "incomplete") {
			t.Errorf("invalid pilot finding: %#v", finding)
		}
	}
	if !slices.Equal(ids, expected) || findings[len(findings)-1].Status != "incomplete" {
		t.Errorf("pilot findings are incomplete or reordered: %v", ids)
	}
}
