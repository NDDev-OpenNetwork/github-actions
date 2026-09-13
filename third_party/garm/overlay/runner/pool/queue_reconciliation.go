package pool

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/go-github/v84/github"

	"github.com/cloudbase/garm/params"
	scaleSetWorker "github.com/cloudbase/garm/workers/scaleset"
)

var nddevQueueReconciliationCandidates = scaleSetWorker.NDDevQueueReconciliationCandidates

type repositoryWorkflowRunLister interface {
	ListRepositoryWorkflowRuns(context.Context, string, string, *github.ListWorkflowRunsOptions) (*github.WorkflowRuns, *github.Response, error)
}

const (
	queueJournalReconcileInterval = 5 * time.Minute
	queueJournalReconcileBatch    = 2
	queueLegacyRunLimit           = 10
)

// DB cleanup can legitimately precede scale-set listener completion. The
// durable journal must therefore be an independent input to reconciliation.
// Neither its age nor the absence of a DB row authorizes terminalization.
func (r *basePoolManager) reconcileJournalQueueIntents() {
	candidates, err := nddevQueueReconciliationCandidates(r.ctx, r.entity)
	if err != nil {
		slog.ErrorContext(r.ctx, "cannot observe journal reconciliation candidates", "error", err)
		return
	}
	if r.checkedJournalIntents == nil {
		r.checkedJournalIntents = make(map[string]time.Time)
	}
	now := time.Now()
	for key, checked := range r.checkedJournalIntents {
		if now.Sub(checked) > 2*queueJournalReconcileInterval {
			delete(r.checkedJournalIntents, key)
		}
	}
	checked := 0
	for _, candidate := range candidates {
		if checked >= queueJournalReconcileBatch || r.authoritativeReconcileDeferred(time.Now()) || r.ctx.Err() != nil {
			break
		}
		job := candidate.Job
		if last := r.checkedJournalIntents[job.ScaleSetJobID]; !last.IsZero() && now.Sub(last) < queueJournalReconcileInterval {
			continue
		}
		r.checkedJournalIntents[job.ScaleSetJobID] = now
		checked++
		if job.RunID == 0 {
			runID, found := r.discoverLegacyQueueRun(job)
			if !found {
				slog.WarnContext(r.ctx, "journal workflow run identity remains unproven", "scale_set_job_id", job.ScaleSetJobID)
				continue
			}
			job.RunID = runID
		}
		// A successful DB reconciliation may preserve an in-progress intent.
		// Keep its check budget until the journal itself stops selecting it.
		r.reconcileExactScaleSetJob(job, candidate.ScaleSetID)
	}
}

// Older rehydrated entries omitted RunID. A small, complete run window is a
// discovery hint only; the existing exact GUID/run/attempt/check URL/source/
// label verifier re-reads the selected job before any journal mutation. An
// incomplete window, duplicate match or absent identity remains unknown.
func (r *basePoolManager) discoverLegacyQueueRun(job params.Job) (int64, bool) {
	lister, ok := r.ghcli.(repositoryWorkflowRunLister)
	if !ok || job.CreatedAt.IsZero() || job.ScaleSetJobID == "" {
		return 0, false
	}
	from, until := job.CreatedAt.Add(-30*time.Minute), job.CreatedAt.Add(time.Minute)
	options := &github.ListWorkflowRunsOptions{
		Created:     from.UTC().Format(time.RFC3339) + ".." + until.UTC().Format(time.RFC3339),
		ListOptions: github.ListOptions{PerPage: queueLegacyRunLimit},
	}
	runs, resp, err := lister.ListRepositoryWorkflowRuns(r.ctx, job.RepositoryOwner, job.RepositoryName, options)
	if err != nil {
		r.noteAuthoritativeScaleSetRead(resp, err, "failed to discover legacy journal workflow run", job)
		return 0, false
	}
	if runs == nil || runs.TotalCount == nil || runs.GetTotalCount() < 0 || runs.GetTotalCount() > queueLegacyRunLimit ||
		len(runs.WorkflowRuns) != runs.GetTotalCount() || resp == nil || resp.NextPage != 0 {
		return 0, false
	}
	seen := make(map[int64]bool)
	for _, run := range runs.WorkflowRuns {
		if run == nil || run.GetID() <= 0 || seen[run.GetID()] || run.GetCheckSuiteID() <= 0 || run.CreatedAt == nil ||
			run.GetCreatedAt().Time.Before(from) || run.GetCreatedAt().Time.After(until) || run.Repository == nil ||
			!strings.EqualFold(run.Repository.GetOwner().GetLogin(), job.RepositoryOwner) ||
			!strings.EqualFold(run.Repository.GetName(), job.RepositoryName) {
			return 0, false
		}
		seen[run.GetID()] = true
	}
	var matched int64
	for _, run := range runs.WorkflowRuns {
		check, complete, err := r.listExactScaleSetCheckRun(job, run)
		if err != nil || !complete {
			return 0, false
		}
		if check == nil {
			continue
		}
		if !isGitHubActionsCheckRun(check) || matched != 0 {
			return 0, false
		}
		matched = run.GetID()
	}
	return matched, matched != 0
}
