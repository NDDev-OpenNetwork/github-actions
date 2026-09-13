package scaleset

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/cloudbase/garm/params"
)

// NDDevQueueReconciliationCandidate is observation only. Its GUID, entity and
// scale-set identity must still bind to one authoritative GitHub workflow job.
type NDDevQueueReconciliationCandidate struct {
	Job        params.Job
	ScaleSetID int64
}

// NDDevQueueReconciliationCandidates includes journal entries whose DB job may
// already have been deleted by normal webhook processing. It never renews a
// TTL or interprets DB absence, delivery completion or expiry as job success.
func NDDevQueueReconciliationCandidates(ctx context.Context, entity params.ForgeEntity) ([]NDDevQueueReconciliationCandidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := newQueueIntentCoordinatorFromEnvironment()
	if c.journalPath == "" {
		return nil, nil
	}
	journal, err := readQueueIntentJournal(c.journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return queueReconciliationCandidates(journal, entity, c.nowUTC()), nil
}

func queueReconciliationCandidates(journal queueIntentJournal, entity params.ForgeEntity, now time.Time) []NDDevQueueReconciliationCandidate {
	var result []NDDevQueueReconciliationCandidate
	for _, intent := range journal.Intents {
		if intent.State != queueStateQueued && intent.State != queueStateAssigned {
			continue
		}
		if intent.RunnerName != "" || intent.RunnerRequestID != 0 || intent.Owner != entity.Owner || !validRepository(intent.Repository) {
			continue
		}
		if expiry := journal.TerminalJobs[intent.JobID]; expiry.After(now) {
			continue
		}
		parts := strings.SplitN(intent.Repository, "/", 2)
		if parts[0] != entity.Owner || (entity.EntityType == params.ForgeEntityTypeRepository && parts[1] != entity.Name) {
			continue
		}
		if entity.EntityType != params.ForgeEntityTypeRepository && entity.EntityType != params.ForgeEntityTypeOrganization {
			continue
		}
		first := queueWaitSince(intent)
		if first.IsZero() || now.Sub(first) < 2*time.Minute {
			continue
		}
		result = append(result, NDDevQueueReconciliationCandidate{
			ScaleSetID: intent.ScaleSetID,
			Job: params.Job{ScaleSetJobID: intent.JobID, RunID: intent.WorkflowRunID,
				RepositoryOwner: parts[0], RepositoryName: parts[1], CreatedAt: first,
				Action: intent.EventName, Status: string(params.JobStatusQueued),
				Labels: []string{intent.ScaleSetName}},
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Job.CreatedAt.Equal(result[j].Job.CreatedAt) {
			return result[i].Job.CreatedAt.Before(result[j].Job.CreatedAt)
		}
		return result[i].Job.ScaleSetJobID < result[j].Job.ScaleSetJobID
	})
	return result
}
