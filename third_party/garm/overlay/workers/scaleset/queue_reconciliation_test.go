package scaleset

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/cloudbase/garm/params"
)

func TestAuthoritativeRunIdentitySurvivesRehydrationAndRepositoryBinding(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "already-repository-bound"}[existing], func(t *testing.T) {
			now := time.Now().UTC()
			c := testQueueCoordinator(t, &now, nil)
			set := testQueueScaleSet(11, "nddev-linux-standard")
			entity := params.ForgeEntity{EntityType: params.ForgeEntityTypeRepository, Owner: "example-owner", Name: "example-repo"}
			job := params.Job{ScaleSetJobID: "example-guid", RepositoryOwner: entity.Owner, RepositoryName: entity.Name,
				RunID: 321, CreatedAt: now.Add(-time.Minute), Action: "push"}
			if existing {
				legacy := job
				legacy.RunID = 0
				if _, err := c.EnsureAuthoritative(set, entity, legacy); err != nil {
					t.Fatal(err)
				}
			}
			if changed, err := c.EnsureAuthoritative(set, entity, job); err != nil || !changed {
				t.Fatalf("run identity was not bound: changed=%t err=%v", changed, err)
			}
			before, err := readQueueIntentJournal(c.journalPath)
			if err != nil {
				t.Fatal(err)
			}
			key := queueIntentKey(int64(set.ScaleSetID), job.ScaleSetJobID)
			if before.Intents[key].WorkflowRunID != job.RunID {
				t.Fatal("rehydrated journal lost the authoritative run ID")
			}
			if changed, err := c.EnsureAuthoritative(set, entity, job); err != nil || changed {
				t.Fatalf("repeat binding changed the journal: %t %v", changed, err)
			}
			job.RunID++
			if _, err := c.EnsureAuthoritative(set, entity, job); err == nil {
				t.Fatal("a different run replaced an established identity")
			}
			after, err := readQueueIntentJournal(c.journalPath)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("failed binding or repeat renewed state or capacity")
			}
		})
	}
}

func TestJournalCandidatesAreReadOnlyScopedAndPreserveExecution(t *testing.T) {
	now := time.Now().UTC()
	c := testQueueCoordinator(t, &now, nil)
	set := testQueueScaleSet(11, "nddev-linux-standard")
	entity := params.ForgeEntity{EntityType: params.ForgeEntityTypeRepository, Owner: "example-owner", Name: "example-repo"}
	job := params.Job{ScaleSetJobID: "example-orphan", RunID: 321, RepositoryOwner: entity.Owner, RepositoryName: entity.Name, CreatedAt: now, Action: "push"}
	if _, err := c.EnsureAuthoritative(set, entity, job); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(c.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(int64(set.ScaleSetID), job.ScaleSetJobID)
	original := journal.Intents[key]
	original.FirstQueuedAt = now.Add(-3 * time.Minute)
	journal.Intents[key] = original
	for _, change := range []string{"running", "acquired", "request", "runner", "foreign-owner", "foreign-repo", "fresh", "terminal"} {
		intent := original
		intent.JobID = "example-" + change
		intent.Key = queueIntentKey(intent.ScaleSetID, intent.JobID)
		switch change {
		case "running":
			intent.State = queueStateRunning
		case "acquired":
			intent.State = queueStateAcquired
		case "request":
			intent.RunnerRequestID = 10
		case "runner":
			intent.RunnerName = "example-busy"
		case "foreign-owner":
			intent.Owner, intent.Repository = "example-other", "example-other/repo"
		case "foreign-repo":
			intent.Repository = "example-owner/another"
		case "fresh":
			intent.FirstQueuedAt = now
		case "terminal":
			journal.TerminalJobs[intent.JobID] = now.Add(time.Hour)
		}
		journal.Intents[intent.Key] = intent
	}
	if err := writeQueueIntentJournal(c.journalPath, journal); err != nil {
		t.Fatal(err)
	}
	t.Setenv(queueFileEnvironment, c.journalPath)
	before, _ := os.ReadFile(c.journalPath)
	got, err := NDDevQueueReconciliationCandidates(context.Background(), entity)
	if err != nil || len(got) != 1 || got[0].Job.ScaleSetJobID != job.ScaleSetJobID || got[0].Job.RunID != job.RunID {
		t.Fatalf("incorrect journal scope: %#v %v", got, err)
	}
	after, _ := os.ReadFile(c.journalPath)
	if string(before) != string(after) {
		t.Fatal("candidate observation mutated or renewed the journal")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NDDevQueueReconciliationCandidates(cancelled, entity); err == nil {
		t.Fatal("cancelled observation succeeded")
	}
}
