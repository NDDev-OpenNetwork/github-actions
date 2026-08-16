package benchmarkevidence

import "time"

const SchemaVersion = 1

type Evidence struct {
	SchemaVersion int              `json:"schema_version"`
	CollectedAt   time.Time        `json:"collected_at"`
	Repository    string           `json:"repository"`
	Workflow      WorkflowEvidence `json:"workflow"`
	Sample        SampleEvidence   `json:"sample"`
	Jobs          []JobEvidence    `json:"jobs"`
	Summary       SummaryEvidence  `json:"summary"`
}

type WorkflowEvidence struct {
	RunID      int64     `json:"run_id"`
	RunAttempt int64     `json:"run_attempt"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Event      string    `json:"event"`
	HeadSHA    string    `json:"head_sha"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type SampleEvidence struct {
	Environment string `json:"environment"`
	CacheMode   string `json:"cache_mode"`
	CacheHit    string `json:"cache_hit"`
	Iteration   string `json:"iteration"`
}

type JobEvidence struct {
	Workload       string           `json:"workload"`
	Name           string           `json:"name"`
	RunnerName     string           `json:"runner_name"`
	Labels         []string         `json:"labels"`
	StartedAt      time.Time        `json:"started_at"`
	CompletedAt    time.Time        `json:"completed_at"`
	QueueToStartMS int64            `json:"queue_to_start_ms"`
	JobDurationMS  int64            `json:"job_duration_ms"`
	Steps          []StepEvidence   `json:"steps"`
	Metrics        BenchmarkRecord  `json:"metrics"`
	Artifact       ArtifactEvidence `json:"artifact"`
}

type StepEvidence struct {
	Number      int       `json:"number"`
	Name        string    `json:"name"`
	Conclusion  string    `json:"conclusion"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at"`
	DurationMS  int64     `json:"duration_ms"`
}

type ArtifactEvidence struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	SizeBytes int64     `json:"size_bytes"`
	Digest    string    `json:"digest"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type BenchmarkRecord struct {
	SchemaVersion   int    `json:"schema_version"`
	Workload        string `json:"workload"`
	Environment     string `json:"environment"`
	CacheMode       string `json:"cache_mode"`
	Iteration       string `json:"iteration"`
	CacheHit        string `json:"cache_hit"`
	Toolchain       string `json:"toolchain"`
	Commit          string `json:"commit"`
	RunID           int64  `json:"run_id"`
	RunAttempt      int64  `json:"run_attempt"`
	MachineIDSHA256 string `json:"machine_id_sha256"`
	StartTimeNS     int64  `json:"start_time_ns"`
	FinishTimeNS    int64  `json:"finish_time_ns"`
	ElapsedNS       int64  `json:"elapsed_ns"`
	NetworkRXBytes  int64  `json:"network_rx_bytes"`
}

type SummaryEvidence struct {
	JobCount              int   `json:"job_count"`
	UniqueRunnerNames     int   `json:"unique_runner_names"`
	UniqueMachineIDHashes int   `json:"unique_machine_id_hashes"`
	RunDurationMS         int64 `json:"run_duration_ms"`
	MaximumQueueToStartMS int64 `json:"maximum_queue_to_start_ms"`
	TotalArtifactBytes    int64 `json:"total_artifact_bytes"`
}
