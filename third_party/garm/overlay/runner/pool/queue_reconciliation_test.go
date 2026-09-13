//go:build testing

package pool

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/mock"

	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/params"
	"github.com/cloudbase/garm/runner/common"
	scaleSetWorker "github.com/cloudbase/garm/workers/scaleset"
)

type journalRunLister struct {
	common.GithubClient
	list func(*github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error)
}

func (c journalRunLister) ListRepositoryWorkflowRuns(_ context.Context, _, _ string, opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
	return c.list(opts)
}

func (s *PoolStressTestSuite) journalFixture(runID int64, actualScaleSetID int) (string, params.Job, []byte) {
	s.T().Helper()
	_, err := s.store.CreateEntityScaleSet(s.adminCtx, s.entity, params.CreateScaleSetParams{
		ProviderName: "test-provider", Name: "nddev-linux-standard", Image: "test-image", Flavor: "test-flavor",
		ScaleSetID: actualScaleSetID, MaxRunners: 1, OSType: commonParams.Linux, OSArch: commonParams.Amd64, Enabled: true,
	})
	s.Require().NoError(err)
	directory := s.T().TempDir()
	now := time.Now().UTC()
	first := now.Add(-5 * time.Minute)
	config := map[string]any{
		"schema_version": 5, "max_in_flight": 4, "max_background_in_flight": 1,
		"default_repository_limit": 4, "default_weight": 1, "queued_ttl_seconds": 600,
		"acquiring_ttl_seconds": 120, "acquired_ttl_seconds": 600, "execution_ttl_seconds": 86400,
		"priority_aging_seconds": 300, "max_repository_share_percent": 75,
		"repositories": map[string]any{},
		"capacity":     map[string]int{"cpu_units": 8, "memory_mib": 16384},
		"scale_sets": map[string]any{"nddev-linux-standard": map[string]int{
			"cpu_units": 2, "memory_mib": 4096, "reservation_cpu_units": 2, "reservation_memory_mib": 4096}},
	}
	makeIntent := func(guid, state, runner string) map[string]any {
		expiry := now.Add(time.Hour)
		if state == "assigned" {
			expiry = now.Add(5 * time.Minute)
		}
		return map[string]any{
			"key": "github-scale-set-job:v2:11:" + guid, "job_id": guid, "scale_set_id": 11,
			"scale_set_name": "nddev-linux-standard", "runner_request_id": 0, "workflow_run_id": runID,
			"runner_name": runner, "owner": "test-owner", "repository": "test-owner/test-repo",
			"workflow_ref": "authoritative-rehydration", "event_name": "push", "state": state, "priority": 1,
			"queue_time": first, "first_queued_at": first, "state_entered_at": first,
			"updated_at": first, "expires_at": expiry,
		}
	}
	busy := makeIntent("example-busy", "running", "example-active-runner")
	busy["runner_request_id"] = 99
	journal := map[string]any{
		"schema_version": 6, "generation": 1, "updated_at": first,
		"intents": map[string]any{
			"github-scale-set-job:v2:11:example-orphan": makeIntent("example-orphan", "assigned", ""),
			"github-scale-set-job:v2:11:example-busy":   busy,
		},
		"repositories":  map[string]any{"test-owner/test-repo": map[string]any{"repository": "test-owner/test-repo", "weight": 1, "pass": 0}},
		"terminal_jobs": map[string]any{},
	}
	for name, value := range map[string]any{"config.json": config, "journal.json": journal} {
		data, err := json.Marshal(value)
		s.Require().NoError(err)
		s.Require().NoError(os.WriteFile(filepath.Join(directory, name), data, 0600))
	}
	s.T().Setenv("GARM_NDDEV_QUEUE_ADMISSION_CONFIG", filepath.Join(directory, "config.json"))
	s.T().Setenv("GARM_NDDEV_QUEUE_INTENT_FILE", filepath.Join(directory, "journal.json"))
	s.T().Setenv("GARM_NDDEV_QUEUE_INTENT_LOCK_FILE", filepath.Join(directory, "journal.lock"))
	nddevRemoveAuthoritativeQueueIntent = scaleSetWorker.NDDevRemoveQueueIntent
	nddevEnsureAuthoritativeQueueIntent = scaleSetWorker.NDDevEnsureQueueIntent
	path := filepath.Join(directory, "journal.json")
	before, err := os.ReadFile(path)
	s.Require().NoError(err)
	return path, params.Job{ScaleSetJobID: "example-orphan", RunID: runID, RepositoryOwner: "test-owner", RepositoryName: "test-repo", CreatedAt: first}, before
}

func (s *PoolStressTestSuite) TestJournalReconciliationWithoutDBRowPreservesRunningWork() {
	path, _, before := s.journalFixture(4545, 11)
	s.expectBoundScaleSetIdentity(4545, 9010, 501, 8001, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "example-orphan", "completed", "success")
	s.Require().NoError(s.mgr.reconcileStaleJobs())
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	var original, result map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(before, &original))
	s.Require().NoError(json.Unmarshal(after, &result))
	var oldIntents, intents map[string]json.RawMessage
	s.Require().NoError(json.Unmarshal(original["intents"], &oldIntents))
	s.Require().NoError(json.Unmarshal(result["intents"], &intents))
	s.Len(intents, 1)
	s.JSONEq(string(oldIntents["github-scale-set-job:v2:11:example-busy"]), string(intents["github-scale-set-job:v2:11:example-busy"]))
	var terminals map[string]time.Time
	s.Require().NoError(json.Unmarshal(result["terminal_jobs"], &terminals))
	s.True(terminals["example-orphan"].After(time.Now()))
	jobs, err := s.store.ListAllJobs(s.adminCtx)
	s.Require().NoError(err)
	s.Empty(jobs)
	s.mgr.checkedJournalIntents = nil // The same durable state after a restart.
	s.Require().NoError(s.mgr.reconcileStaleJobs())
	repeated, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(after), string(repeated))
}

func (s *PoolStressTestSuite) TestJournalReconciliationRefusesWrongScaleSet() {
	path, _, before := s.journalFixture(4545, 12)
	s.expectBoundScaleSetIdentity(4545, 9010, 501, 8001, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "example-orphan", "completed", "success")
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(before), string(after))
}

func (s *PoolStressTestSuite) TestJournalReconciliationRetainsAccessRefusalAndRateLimit() {
	path, _, before := s.journalFixture(4545, 11)
	s.ghcliMock.EXPECT().GetWorkflowRunByID(mock.Anything, "test-owner", "test-repo", int64(4545)).
		Return(nil, &github.Response{Response: &http.Response{StatusCode: 403}}, fmt.Errorf("denied")).Once()
	s.mgr.reconcileJournalQueueIntents()
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(before), string(after))
	s.True(s.mgr.authoritativeReconcileDeferred(time.Now()))
}

func (s *PoolStressTestSuite) TestJournalReconciliationDiscoversOnlyAnExactCompleteLegacyRun() {
	path, job, _ := s.journalFixture(0, 11)
	run := scaleSetIdentityRun(4545, 9010, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1)
	run.CreatedAt = &github.Timestamp{Time: job.CreatedAt.Add(-time.Minute)}
	s.mgr.ghcli = journalRunLister{GithubClient: s.ghcliMock, list: func(opts *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
		s.Equal(queueLegacyRunLimit, opts.PerPage)
		s.Equal(job.CreatedAt.Add(-30*time.Minute).Format(time.RFC3339)+".."+job.CreatedAt.Add(time.Minute).Format(time.RFC3339), opts.Created)
		return &github.WorkflowRuns{TotalCount: github.Ptr(1), WorkflowRuns: []*github.WorkflowRun{run}}, &github.Response{}, nil
	}}
	check := scaleSetIdentityCheckRun(501, "example-orphan", "completed", run.GetHeadSHA(), 9010)
	page, response := completeCheckPage([]*github.CheckRun{check}, 1, 0)
	s.ghcliMock.EXPECT().ListCheckRunsCheckSuite(mock.Anything, "test-owner", "test-repo", int64(9010), mock.MatchedBy(allCheckRuns)).Return(page, response, nil).Once()
	s.expectBoundScaleSetIdentity(4545, 9010, 501, 8001, 1, run.GetHeadSHA(), "example-orphan", "completed", "success")
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	var document struct {
		Intents map[string]json.RawMessage `json:"intents"`
	}
	s.Require().NoError(json.Unmarshal(after, &document))
	s.NotContains(document.Intents, "github-scale-set-job:v2:11:example-orphan")
}

func (s *PoolStressTestSuite) TestJournalDiscoveryRefusesIncompleteWindow() {
	path, _, before := s.journalFixture(0, 11)
	s.mgr.ghcli = journalRunLister{GithubClient: s.ghcliMock, list: func(*github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
		return &github.WorkflowRuns{TotalCount: github.Ptr(11)}, &github.Response{NextPage: 2}, nil
	}}
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(before), string(after))
}

func (s *PoolStressTestSuite) TestJournalReconciliationKeepsRunningJobAndRecheckBudget() {
	path, _, before := s.journalFixture(4545, 11)
	s.expectBoundScaleSetIdentity(4545, 9010, 501, 8001, 1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "example-orphan", "in_progress", "")
	s.mgr.reconcileJournalQueueIntents()
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(before), string(after))
}

func (s *PoolStressTestSuite) TestJournalDiscoveryRetainsDuplicateGUIDAcrossRuns() {
	path, job, before := s.journalFixture(0, 11)
	runs := []*github.WorkflowRun{
		scaleSetIdentityRun(4545, 9010, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1),
		scaleSetIdentityRun(4546, 9011, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1),
	}
	for _, run := range runs {
		run.CreatedAt = &github.Timestamp{Time: job.CreatedAt.Add(-time.Minute)}
		check := scaleSetIdentityCheckRun(run.GetCheckSuiteID()+1, "example-orphan", "completed", run.GetHeadSHA(), run.GetCheckSuiteID())
		page, response := completeCheckPage([]*github.CheckRun{check}, 1, 0)
		s.ghcliMock.EXPECT().ListCheckRunsCheckSuite(mock.Anything, "test-owner", "test-repo", run.GetCheckSuiteID(), mock.MatchedBy(allCheckRuns)).Return(page, response, nil).Once()
	}
	s.mgr.ghcli = journalRunLister{GithubClient: s.ghcliMock, list: func(*github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error) {
		return &github.WorkflowRuns{TotalCount: github.Ptr(2), WorkflowRuns: runs}, &github.Response{}, nil
	}}
	s.mgr.reconcileJournalQueueIntents()
	after, err := os.ReadFile(path)
	s.Require().NoError(err)
	s.Equal(string(before), string(after))
}
