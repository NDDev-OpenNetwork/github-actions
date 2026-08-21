package benchmarkevidence

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	DefaultAPIBaseURL   = "https://api.github.com"
	githubAPIVersion    = "2026-03-10"
	workflowName        = "Representative runner benchmark"
	workflowPath        = ".github/workflows/representative-benchmark.yml"
	maximumJSONBytes    = 2 << 20
	maximumArchiveBytes = 1 << 20
	maximumRecordBytes  = 64 << 10
	maximumRedirects    = 3
)

var (
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	shaPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern     = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	machinePattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	iterationPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

var workloads = []string{"bun-next", "docker", "go", "python-uv", "rust"}

var workloadJobNames = map[string]string{
	"bun-next":  "Bun and Next build and test",
	"docker":    "Docker and Compose integration",
	"go":        "Go build and test",
	"python-uv": "Python and uv build and test",
	"rust":      "Rust build and test",
}

var requiredPhases = []string{
	"Check out source",
	"Start benchmark metrics",
	"Restore dependency cache",
	"Resolve dependencies",
	"Build workload",
	"Test workload",
	"Finish benchmark metrics",
	"Upload benchmark record",
}

type Collector struct {
	HTTPClient            *http.Client
	APIBaseURL            string
	Token                 string
	Now                   func() time.Time
	allowInsecureForTests bool
}

type Options struct {
	Repository string
	RunID      int64
}

type artifactRecord struct {
	evidence ArtifactEvidence
	record   BenchmarkRecord
}

func (c Collector) Collect(ctx context.Context, options Options) (Evidence, error) {
	baseURL, client, token, now, err := c.validate(options)
	if err != nil {
		return Evidence{}, err
	}

	var run runResponse
	if err := c.getJSON(ctx, client, baseURL, token, apiPath(options.Repository, "actions", "runs", options.RunID), &run); err != nil {
		return Evidence{}, fmt.Errorf("get workflow run: %w", err)
	}
	if err := validateRun(run, options); err != nil {
		return Evidence{}, err
	}

	var jobs jobsResponse
	if err := c.getJSON(ctx, client, baseURL, token, apiPath(options.Repository, "actions", "runs", options.RunID, "jobs")+"?per_page=100", &jobs); err != nil {
		return Evidence{}, fmt.Errorf("list workflow jobs: %w", err)
	}
	if jobs.TotalCount != len(workloads) || len(jobs.Jobs) != len(workloads) {
		return Evidence{}, fmt.Errorf("workflow run has %d jobs, expected %d", jobs.TotalCount, len(workloads))
	}

	var artifacts artifactsResponse
	if err := c.getJSON(ctx, client, baseURL, token, apiPath(options.Repository, "actions", "runs", options.RunID, "artifacts")+"?per_page=100", &artifacts); err != nil {
		return Evidence{}, fmt.Errorf("list workflow artifacts: %w", err)
	}
	if artifacts.TotalCount != len(workloads) || len(artifacts.Artifacts) != len(workloads) {
		return Evidence{}, fmt.Errorf("workflow run has %d artifacts, expected %d", artifacts.TotalCount, len(workloads))
	}

	records := make(map[string]artifactRecord, len(workloads))
	for _, artifact := range artifacts.Artifacts {
		if artifact.WorkflowRun.ID != run.ID || artifact.WorkflowRun.HeadSHA != run.HeadSHA {
			return Evidence{}, fmt.Errorf("artifact %q does not belong to the requested workflow run", artifact.Name)
		}
		archive, err := c.downloadArtifact(ctx, client, baseURL, token, options.Repository, artifact)
		if err != nil {
			return Evidence{}, fmt.Errorf("download artifact %q: %w", artifact.Name, err)
		}
		record, err := decodeRecord(archive)
		if err != nil {
			return Evidence{}, fmt.Errorf("decode artifact %q: %w", artifact.Name, err)
		}
		if _, duplicate := records[record.Workload]; duplicate {
			return Evidence{}, fmt.Errorf("duplicate artifact workload %q", record.Workload)
		}
		records[record.Workload] = artifactRecord{
			evidence: ArtifactEvidence{
				ID:        artifact.ID,
				Name:      artifact.Name,
				SizeBytes: artifact.SizeInBytes,
				Digest:    artifact.Digest,
				CreatedAt: artifact.CreatedAt,
				ExpiresAt: artifact.ExpiresAt,
			},
			record: record,
		}
	}

	sample, err := validateRecords(run, records)
	if err != nil {
		return Evidence{}, err
	}
	jobsByWorkload, err := validateJobs(run, sample, jobs.Jobs)
	if err != nil {
		return Evidence{}, err
	}

	evidence := Evidence{
		SchemaVersion: SchemaVersion,
		CollectedAt:   now().UTC(),
		Repository:    options.Repository,
		Workflow: WorkflowEvidence{
			RunID:      run.ID,
			RunAttempt: run.RunAttempt,
			Name:       run.Name,
			Path:       run.Path,
			Event:      run.Event,
			HeadSHA:    run.HeadSHA,
			HTMLURL:    run.HTMLURL,
			CreatedAt:  run.CreatedAt,
			UpdatedAt:  run.UpdatedAt,
		},
		Sample: sample,
		Jobs:   make([]JobEvidence, 0, len(workloads)),
	}
	runnerNames := make(map[string]struct{}, len(workloads))
	machineHashes := make(map[string]struct{}, len(workloads))
	for _, workload := range workloads {
		job := jobsByWorkload[workload]
		artifact := records[workload]
		steps := make([]StepEvidence, 0, len(job.Steps))
		for _, step := range job.Steps {
			steps = append(steps, StepEvidence{
				Number:      step.Number,
				Name:        step.Name,
				Conclusion:  step.Conclusion,
				StartedAt:   step.StartedAt,
				CompletedAt: step.CompletedAt,
				DurationMS:  durationMilliseconds(step.StartedAt, step.CompletedAt),
			})
		}
		evidence.Jobs = append(evidence.Jobs, JobEvidence{
			Workload:       workload,
			Name:           job.Name,
			RunnerName:     job.RunnerName,
			Labels:         slices.Clone(job.Labels),
			StartedAt:      job.StartedAt,
			CompletedAt:    job.CompletedAt,
			QueueToStartMS: durationMilliseconds(run.CreatedAt, job.StartedAt),
			JobDurationMS:  durationMilliseconds(job.StartedAt, job.CompletedAt),
			Steps:          steps,
			Metrics:        artifact.record,
			Artifact:       artifact.evidence,
		})
		runnerNames[job.RunnerName] = struct{}{}
		machineHashes[artifact.record.MachineIDSHA256] = struct{}{}
		evidence.Summary.TotalArtifactBytes += artifact.evidence.SizeBytes
		if queue := durationMilliseconds(run.CreatedAt, job.StartedAt); queue > evidence.Summary.MaximumQueueToStartMS {
			evidence.Summary.MaximumQueueToStartMS = queue
		}
	}
	evidence.Summary.JobCount = len(evidence.Jobs)
	evidence.Summary.UniqueRunnerNames = len(runnerNames)
	evidence.Summary.UniqueMachineIDHashes = len(machineHashes)
	evidence.Summary.RunDurationMS = durationMilliseconds(run.CreatedAt, run.UpdatedAt)
	return evidence, nil
}

func (c Collector) validate(options Options) (*url.URL, *http.Client, string, func() time.Time, error) {
	if !repositoryPattern.MatchString(options.Repository) || strings.Contains(options.Repository, "..") {
		return nil, nil, "", nil, fmt.Errorf("repository must be one bounded owner/name pair")
	}
	if options.RunID <= 0 {
		return nil, nil, "", nil, fmt.Errorf("run ID must be positive")
	}
	token := c.Token
	if token == "" || token != strings.TrimSpace(token) || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n") {
		return nil, nil, "", nil, fmt.Errorf("GitHub token must be a bounded non-whitespace value")
	}
	rawBaseURL := c.APIBaseURL
	if rawBaseURL == "" {
		rawBaseURL = DefaultAPIBaseURL
	}
	baseURL, err := url.Parse(rawBaseURL)
	if err != nil {
		return nil, nil, "", nil, fmt.Errorf("invalid GitHub API base URL")
	}
	validScheme := baseURL.Scheme == "https" || (c.allowInsecureForTests && baseURL.Scheme == "http")
	if baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" || !validScheme {
		return nil, nil, "", nil, fmt.Errorf("invalid GitHub API base URL")
	}
	baseURL.Path = strings.TrimSuffix(baseURL.Path, "/")
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	now := c.Now
	if now == nil {
		now = time.Now
	}
	return baseURL, client, token, now, nil
}

func validateRun(run runResponse, options Options) error {
	if run.ID != options.RunID || run.Repository.FullName != options.Repository {
		return fmt.Errorf("workflow run identity does not match the requested repository and run")
	}
	if run.Name != workflowName || run.Path != workflowPath || run.Event != "workflow_dispatch" {
		return fmt.Errorf("workflow run is not the manual representative benchmark")
	}
	if run.Status != "completed" || run.Conclusion != "success" {
		return fmt.Errorf("workflow run is not a completed success")
	}
	if run.RunAttempt <= 0 || !shaPattern.MatchString(run.HeadSHA) {
		return fmt.Errorf("workflow run attempt or head SHA is invalid")
	}
	if run.CreatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) {
		return fmt.Errorf("workflow run timestamps are invalid")
	}
	parsedURL, err := url.Parse(run.HTMLURL)
	expectedURL := fmt.Sprintf("https://github.com/%s/actions/runs/%d", options.Repository, options.RunID)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host != "github.com" || run.HTMLURL != expectedURL {
		return fmt.Errorf("workflow run HTML URL is not a canonical GitHub URL")
	}
	return nil
}

func validateRecords(run runResponse, records map[string]artifactRecord) (SampleEvidence, error) {
	if len(records) != len(workloads) {
		return SampleEvidence{}, fmt.Errorf("artifact workload set is incomplete")
	}
	var sample SampleEvidence
	for index, workload := range workloads {
		artifact, exists := records[workload]
		if !exists {
			return SampleEvidence{}, fmt.Errorf("artifact for workload %q is missing", workload)
		}
		record := artifact.record
		if record.SchemaVersion != 1 || record.Workload != workload {
			return SampleEvidence{}, fmt.Errorf("artifact %q has an invalid record identity", artifact.evidence.Name)
		}
		if record.Environment != "github-hosted" && record.Environment != "nddev" {
			return SampleEvidence{}, fmt.Errorf("artifact %q has invalid environment %q", artifact.evidence.Name, record.Environment)
		}
		if record.CacheMode != "cold" && record.CacheMode != "warm" {
			return SampleEvidence{}, fmt.Errorf("artifact %q has invalid cache mode %q", artifact.evidence.Name, record.CacheMode)
		}
		if !iterationPattern.MatchString(record.Iteration) {
			return SampleEvidence{}, fmt.Errorf("artifact %q has invalid iteration", artifact.evidence.Name)
		}
		if record.CacheHit != "true" && record.CacheHit != "false" && record.CacheHit != "disabled" {
			return SampleEvidence{}, fmt.Errorf("artifact %q has invalid cache result", artifact.evidence.Name)
		}
		if record.CacheMode == "cold" && record.CacheHit != "disabled" {
			return SampleEvidence{}, fmt.Errorf("cold artifact %q did not disable cache", artifact.evidence.Name)
		}
		if record.CacheMode == "warm" && record.CacheHit == "disabled" {
			return SampleEvidence{}, fmt.Errorf("warm artifact %q disabled cache", artifact.evidence.Name)
		}
		if record.Commit != run.HeadSHA || record.RunID != run.ID || record.RunAttempt != run.RunAttempt {
			return SampleEvidence{}, fmt.Errorf("artifact %q does not match workflow run identity", artifact.evidence.Name)
		}
		if record.StartTimeNS <= 0 || record.FinishTimeNS < record.StartTimeNS || record.ElapsedNS != record.FinishTimeNS-record.StartTimeNS || record.NetworkRXBytes < 0 {
			return SampleEvidence{}, fmt.Errorf("artifact %q has invalid counters", artifact.evidence.Name)
		}
		if record.Toolchain == "" || len(record.Toolchain) > 256 || strings.ContainsAny(record.Toolchain, "\r\n") {
			return SampleEvidence{}, fmt.Errorf("artifact %q has an invalid toolchain", artifact.evidence.Name)
		}
		if record.MachineIDSHA256 != "unavailable" && !machinePattern.MatchString(record.MachineIDSHA256) {
			return SampleEvidence{}, fmt.Errorf("artifact %q has an invalid machine identity hash", artifact.evidence.Name)
		}
		expectedName := fmt.Sprintf("benchmark-%s-%s-%s-%s", record.Environment, record.CacheMode, record.Iteration, workload)
		if artifact.evidence.Name != expectedName {
			return SampleEvidence{}, fmt.Errorf("artifact %q does not match its record", artifact.evidence.Name)
		}
		if index == 0 {
			sample = SampleEvidence{
				Environment: record.Environment,
				CacheMode:   record.CacheMode,
				CacheHit:    record.CacheHit,
				Iteration:   record.Iteration,
			}
			continue
		}
		if sample.Environment != record.Environment || sample.CacheMode != record.CacheMode || sample.Iteration != record.Iteration {
			return SampleEvidence{}, fmt.Errorf("artifact records do not describe one coherent sample")
		}
		// Cache effectiveness is a workload result, not sample identity. A warm
		// prime can legitimately hit Go's dependency cache while Rust records an
		// explicit miss because no repository-scoped compiler credential exists.
		// Preserve every per-job result and summarize heterogeneous results rather
		// than rejecting otherwise coherent evidence.
		if sample.CacheHit != record.CacheHit {
			sample.CacheHit = "mixed"
		}
	}
	if sample.Environment == "nddev" {
		hashes := make(map[string]struct{}, len(workloads))
		for _, artifact := range records {
			if artifact.record.MachineIDSHA256 == "unavailable" {
				return SampleEvidence{}, fmt.Errorf("NDDev artifact has no machine identity hash")
			}
			hashes[artifact.record.MachineIDSHA256] = struct{}{}
		}
		if len(hashes) != len(workloads) {
			return SampleEvidence{}, fmt.Errorf("NDDev sample reused a machine identity")
		}
	}
	return sample, nil
}

func validateJobs(run runResponse, sample SampleEvidence, jobs []jobResponse) (map[string]jobResponse, error) {
	jobsByWorkload := make(map[string]jobResponse, len(workloads))
	runnerNames := make(map[string]struct{}, len(workloads))
	for _, job := range jobs {
		workload := workloadForJobName(job.Name)
		if workload == "" {
			return nil, fmt.Errorf("unexpected workflow job %q", job.Name)
		}
		if _, duplicate := jobsByWorkload[workload]; duplicate {
			return nil, fmt.Errorf("duplicate workflow job %q", job.Name)
		}
		if job.Status != "completed" || job.Conclusion != "success" || strings.TrimSpace(job.RunnerName) == "" || len(job.RunnerName) > 256 {
			return nil, fmt.Errorf("workflow job %q is not a completed success with a runner identity", job.Name)
		}
		expectedLabel := expectedRunnerLabel(sample.Environment, workload)
		if len(job.Labels) != 1 || job.Labels[0] != expectedLabel {
			return nil, fmt.Errorf("workflow job %q ran on labels %v, expected %q", job.Name, job.Labels, expectedLabel)
		}
		if job.StartedAt.Before(run.CreatedAt) || job.CompletedAt.Before(job.StartedAt) || run.UpdatedAt.Before(job.CompletedAt) {
			return nil, fmt.Errorf("workflow job %q has invalid timestamps", job.Name)
		}
		if err := validateSteps(sample.Environment, sample.CacheMode, workload, job); err != nil {
			return nil, err
		}
		jobsByWorkload[workload] = job
		runnerNames[job.RunnerName] = struct{}{}
	}
	if len(jobsByWorkload) != len(workloads) {
		return nil, fmt.Errorf("workflow job set is incomplete")
	}
	if len(runnerNames) != len(workloads) {
		return nil, fmt.Errorf("sample reused a runner identity")
	}
	return jobsByWorkload, nil
}

func validateSteps(environment, cacheMode, workload string, job jobResponse) error {
	stepsByName := make(map[string][]stepResponse, len(job.Steps))
	previousNumber := 0
	for _, step := range job.Steps {
		if step.Number <= previousNumber || step.Status != "completed" || step.StartedAt.IsZero() || step.CompletedAt.Before(step.StartedAt) {
			return fmt.Errorf("workflow job %q has invalid step %q", job.Name, step.Name)
		}
		previousNumber = step.Number
		allowedSkip := step.Conclusion == "skipped" && benchmarkStepMaySkip(environment, cacheMode, workload, step.Name)
		if step.Conclusion != "success" && !allowedSkip {
			return fmt.Errorf("workflow job %q step %q concluded %q", job.Name, step.Name, step.Conclusion)
		}
		stepsByName[step.Name] = append(stepsByName[step.Name], step)
	}
	for _, phase := range requiredPhases {
		if len(stepsByName[phase]) != 1 {
			return fmt.Errorf("workflow job %q must contain one %q phase", job.Name, phase)
		}
	}
	restore := stepsByName["Restore dependency cache"][0]
	localRustCache := environment == "nddev" && workload == "rust"
	if cacheMode == "cold" && restore.Conclusion != "skipped" {
		return fmt.Errorf("cold workflow job %q restored a cache", job.Name)
	}
	if cacheMode == "warm" && !localRustCache && restore.Conclusion != "success" {
		return fmt.Errorf("warm workflow job %q did not run its cache phase", job.Name)
	}
	for _, phase := range []string{"Configure NDDev compiler cache", "Inspect NDDev compiler cache"} {
		steps := stepsByName[phase]
		if workload != "rust" {
			continue
		}
		// Historical evidence predates the local-sccache phases. If the current
		// workflow reports them, validate them exactly; absence remains readable
		// so immutable old samples do not become unverifiable.
		if len(steps) == 0 {
			continue
		}
		if len(steps) != 1 {
			return fmt.Errorf("workflow job %q must contain one %q phase", job.Name, phase)
		}
		want := "skipped"
		if cacheMode == "warm" && localRustCache {
			want = "success"
		}
		if steps[0].Conclusion != want {
			return fmt.Errorf("workflow job %q phase %q concluded %q, want %q", job.Name, phase, steps[0].Conclusion, want)
		}
	}
	return nil
}

func benchmarkStepMaySkip(environment, cacheMode, workload, name string) bool {
	if name == "Restore dependency cache" {
		return cacheMode == "cold" || (cacheMode == "warm" && environment == "nddev" && workload == "rust")
	}
	if workload == "rust" && (name == "Configure NDDev compiler cache" || name == "Inspect NDDev compiler cache") {
		return !(environment == "nddev" && cacheMode == "warm")
	}
	return false
}

func workloadForJobName(name string) string {
	for workload, expected := range workloadJobNames {
		if name == expected {
			return workload
		}
	}
	return ""
}

func expectedRunnerLabel(environment, workload string) string {
	if environment == "github-hosted" {
		return "ubuntu-24.04"
	}
	if workload == "docker" {
		return "nddev-linux-integration"
	}
	return "nddev-linux-standard"
}

func durationMilliseconds(start, finish time.Time) int64 {
	return finish.Sub(start).Milliseconds()
}
