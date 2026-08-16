package deploycontract

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"slices"
	"testing"
	"time"
)

const observerAuditPath = "../../config/observer-v2-convergence-audit.json"

type observerAudit struct {
	SchemaVersion    int                     `json:"schema_version"`
	RecordedAt       time.Time               `json:"recorded_at"`
	RepositoryCommit string                  `json:"repository_commit"`
	PullRequest      int                     `json:"pull_request"`
	CIRunID          int64                   `json:"ci_run_id"`
	Observer         observerArtifact        `json:"observer"`
	Precondition     observerPrecondition    `json:"precondition"`
	CanaryRuns       []observerCanaryRun     `json:"canary_runs"`
	ExporterAnchor   observerExporter        `json:"exporter_anchor"`
	GraceSample      observerGraceSample     `json:"grace_sample"`
	ConvergedSample  observerConvergedSample `json:"converged_sample"`
	Postcondition    observerPostcondition   `json:"postcondition"`
}

type observerArtifact struct {
	Version               string `json:"version"`
	SnapshotSchemaVersion int    `json:"snapshot_schema_version"`
	SHA256                string `json:"sha256"`
	SystemdRestarts       int    `json:"systemd_restarts"`
}

type observerPrecondition struct {
	CapturedAt            time.Time `json:"captured_at"`
	SnapshotSchemaVersion int       `json:"snapshot_schema_version"`
	Healthy               bool      `json:"healthy"`
	DiagnosticBundles     int       `json:"diagnostic_bundles"`
	ExportedBundles       int       `json:"exported_bundles"`
	PendingBundles        int       `json:"pending_bundles"`
	VisibleInstances      int       `json:"visible_instances"`
	JournalLeases         int       `json:"journal_leases"`
	OrphanInstances       int       `json:"orphan_instances"`
	MissingInstances      int       `json:"missing_instances"`
}

type observerCanaryRun struct {
	RunID                      int64     `json:"run_id"`
	Role                       string    `json:"role"`
	HeadSHA                    string    `json:"head_sha"`
	Conclusion                 string    `json:"conclusion"`
	CreatedAt                  time.Time `json:"created_at,omitempty"`
	JobStartedAt               time.Time `json:"job_started_at,omitempty"`
	JobCompletedAt             time.Time `json:"job_completed_at,omitempty"`
	DiagnosticBundleCountAfter int       `json:"diagnostic_bundle_count_after"`
}

type observerExporter struct {
	ObservedAt          time.Time `json:"observed_at"`
	SourceBundles       int       `json:"source_bundles"`
	ExportedBundles     int       `json:"exported_bundles"`
	PendingBundles      int       `json:"pending_bundles"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	SourceBytes         int64     `json:"source_bytes"`
	ExportedBytes       int64     `json:"exported_bytes"`
}

type observerGraceSample struct {
	CapturedAt              time.Time `json:"captured_at"`
	SnapshotSchemaVersion   int       `json:"snapshot_schema_version"`
	Healthy                 bool      `json:"healthy"`
	CollectionErrors        int       `json:"collection_errors"`
	State                   string    `json:"state"`
	GracePeriodSeconds      int64     `json:"grace_period_seconds"`
	GraceRemainingSeconds   int64     `json:"grace_remaining_seconds"`
	LocalBundleDelta        int       `json:"local_bundle_delta"`
	LocalByteDelta          int64     `json:"local_byte_delta"`
	DiagnosticBundles       int       `json:"diagnostic_bundles"`
	DiagnosticBytes         int64     `json:"diagnostic_bytes"`
	ExporterSourceBundles   int       `json:"exporter_source_bundles"`
	ExporterExportedBundles int       `json:"exporter_exported_bundles"`
	ExporterPendingBundles  int       `json:"exporter_pending_bundles"`
	VisibleInstances        int       `json:"visible_instances"`
	JournalLeases           int       `json:"journal_leases"`
	OrphanInstances         int       `json:"orphan_instances"`
	MissingInstances        int       `json:"missing_instances"`
}

type observerConvergedSample struct {
	CapturedAt                  time.Time `json:"captured_at"`
	ExporterObservedAt          time.Time `json:"exporter_observed_at"`
	SnapshotSchemaVersion       int       `json:"snapshot_schema_version"`
	Healthy                     bool      `json:"healthy"`
	CollectionErrors            int       `json:"collection_errors"`
	State                       string    `json:"state"`
	GraceRemainingSeconds       int64     `json:"grace_remaining_seconds"`
	LocalBundleDelta            int       `json:"local_bundle_delta"`
	LocalByteDelta              int64     `json:"local_byte_delta"`
	DiagnosticBundles           int       `json:"diagnostic_bundles"`
	DiagnosticBytes             int64     `json:"diagnostic_bytes"`
	ExporterSourceBundles       int       `json:"exporter_source_bundles"`
	ExporterExportedBundles     int       `json:"exporter_exported_bundles"`
	ExporterPendingBundles      int       `json:"exporter_pending_bundles"`
	ExporterConsecutiveFailures int       `json:"exporter_consecutive_failures"`
	VisibleInstances            int       `json:"visible_instances"`
	JournalLeases               int       `json:"journal_leases"`
	OrphanInstances             int       `json:"orphan_instances"`
	MissingInstances            int       `json:"missing_instances"`
}

type observerPostcondition struct {
	GitHubRegisteredRunners   int      `json:"github_registered_runners"`
	ActivePlatformUnits       []string `json:"active_platform_units"`
	FailedSystemdUnits        int      `json:"failed_systemd_units"`
	RootFilesystemFreePercent int      `json:"root_filesystem_free_percent"`
	LegacyRunnerListeners     int      `json:"legacy_runner_listeners"`
	ExamplePlatformHTTPStatus         int      `json:"example_platform_http_status"`
	CaptchaHTTPStatus         int      `json:"captcha_http_status"`
}

func TestObserverV2ConvergenceAuditProvesBoundedHealthyTransition(t *testing.T) {
	audit := readObserverAudit(t)
	const commit = "bbe83ce3100a1d4faea21c5364b90843850a65fc"
	if audit.SchemaVersion != 1 || audit.RepositoryCommit != commit || audit.PullRequest != 76 ||
		audit.CIRunID != 31288730335 || audit.RecordedAt.IsZero() {
		t.Fatalf("observer audit identity is invalid: %#v", audit)
	}
	if audit.Observer.Version != "v0.1.0" || audit.Observer.SnapshotSchemaVersion != 2 ||
		audit.Observer.SystemdRestarts != 0 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(audit.Observer.SHA256) {
		t.Fatalf("observer artifact identity is invalid: %#v", audit.Observer)
	}
	pre := audit.Precondition
	if pre.SnapshotSchemaVersion != 1 || !pre.Healthy || pre.DiagnosticBundles != 38 ||
		pre.ExportedBundles != 38 || pre.PendingBundles != 0 || !emptyInventory(pre.VisibleInstances, pre.JournalLeases, pre.OrphanInstances, pre.MissingInstances) {
		t.Fatalf("observer rollout precondition is unsafe: %#v", pre)
	}
	if len(audit.CanaryRuns) != 2 || audit.CanaryRuns[0].RunID != 31288871282 ||
		audit.CanaryRuns[0].Role != "deployment-smoke" || audit.CanaryRuns[0].DiagnosticBundleCountAfter != 39 ||
		audit.CanaryRuns[1].RunID != 31288969439 || audit.CanaryRuns[1].Role != "controlled-convergence-proof" ||
		audit.CanaryRuns[1].DiagnosticBundleCountAfter != 40 {
		t.Fatalf("canary evidence is incomplete: %#v", audit.CanaryRuns)
	}
	for _, run := range audit.CanaryRuns {
		if run.HeadSHA != commit || run.Conclusion != "success" {
			t.Errorf("canary run does not prove the deployed commit: %#v", run)
		}
	}
	controlled := audit.CanaryRuns[1]
	if controlled.CreatedAt.IsZero() || !controlled.JobStartedAt.After(controlled.CreatedAt) ||
		!controlled.JobCompletedAt.After(controlled.JobStartedAt) {
		t.Errorf("controlled canary timestamps are incoherent: %#v", controlled)
	}
	anchor := audit.ExporterAnchor
	if anchor.SourceBundles != 39 || anchor.ExportedBundles != 39 || anchor.PendingBundles != 0 ||
		anchor.ConsecutiveFailures != 0 || anchor.SourceBytes != anchor.ExportedBytes || anchor.ObservedAt.IsZero() {
		t.Fatalf("exporter anchor is not healthy: %#v", anchor)
	}
	grace := audit.GraceSample
	if grace.SnapshotSchemaVersion != 2 || !grace.Healthy || grace.CollectionErrors != 0 ||
		grace.State != "convergence-grace" || grace.GracePeriodSeconds != 90 || grace.GraceRemainingSeconds <= 0 ||
		grace.GraceRemainingSeconds >= grace.GracePeriodSeconds || grace.LocalBundleDelta != 1 || grace.LocalByteDelta <= 0 ||
		grace.DiagnosticBundles-grace.ExporterSourceBundles != grace.LocalBundleDelta ||
		grace.DiagnosticBytes-anchor.SourceBytes != grace.LocalByteDelta || grace.ExporterSourceBundles != 39 ||
		grace.ExporterExportedBundles != 39 || grace.ExporterPendingBundles != 0 ||
		!emptyInventory(grace.VisibleInstances, grace.JournalLeases, grace.OrphanInstances, grace.MissingInstances) {
		t.Fatalf("bounded grace sample is invalid: %#v", grace)
	}
	converged := audit.ConvergedSample
	if converged.SnapshotSchemaVersion != 2 || !converged.Healthy || converged.CollectionErrors != 0 ||
		converged.State != "synchronized" || converged.GraceRemainingSeconds != 0 || converged.LocalBundleDelta != 0 ||
		converged.LocalByteDelta != 0 || converged.DiagnosticBundles != 40 || converged.DiagnosticBytes != grace.DiagnosticBytes ||
		converged.ExporterSourceBundles != converged.DiagnosticBundles ||
		converged.ExporterExportedBundles != converged.DiagnosticBundles || converged.ExporterPendingBundles != 0 ||
		converged.ExporterConsecutiveFailures != 0 || !converged.ExporterObservedAt.After(grace.CapturedAt) ||
		!converged.CapturedAt.After(converged.ExporterObservedAt) ||
		!emptyInventory(converged.VisibleInstances, converged.JournalLeases, converged.OrphanInstances, converged.MissingInstances) {
		t.Fatalf("exporter did not prove convergence: %#v", converged)
	}
	expectedUnits := []string{
		"garm", "gha-diagnostic-exporter.timer", "gha-fleet-gateway",
		"gha-fleet-observer", "gha-rustfs", "gha-zot",
	}
	post := audit.Postcondition
	if post.GitHubRegisteredRunners != 0 || !slices.Equal(post.ActivePlatformUnits, expectedUnits) ||
		post.FailedSystemdUnits != 0 || post.RootFilesystemFreePercent < 20 || post.LegacyRunnerListeners != 12 ||
		post.ExamplePlatformHTTPStatus != 200 || post.CaptchaHTTPStatus != 200 {
		t.Fatalf("observer rollout postcondition is unsafe: %#v", post)
	}
}

func readObserverAudit(t *testing.T) observerAudit {
	t.Helper()
	file, err := os.Open(observerAuditPath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 128<<10))
	decoder.DisallowUnknownFields()
	var audit observerAudit
	if err := decoder.Decode(&audit); err != nil {
		t.Fatal(err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("observer audit contains trailing JSON: %v", err)
	}
	return audit
}

func emptyInventory(values ...int) bool {
	for _, value := range values {
		if value != 0 {
			return false
		}
	}
	return true
}
