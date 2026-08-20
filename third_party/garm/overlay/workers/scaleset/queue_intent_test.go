package scaleset

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/cloudbase/garm/params"
)

func TestAdmittedCapacityIntentDisappearsOnCompletion(t *testing.T) {
	now := time.Date(2026, 8, 19, 7, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repository", now)
	entity := testQueueEntityForJob(job)
	if _, err := coordinator.ObserveLifecycle(scaleSet, entity, []params.ScaleSetJobMessage{job}, nil, nil); err != nil {
		t.Fatal(err)
	}
	target, err := coordinator.AdmittedCapacityTarget(scaleSet, entity)
	if err != nil || target != 1 {
		t.Fatalf("provisional JobAssigned capacity target=%d err=%v", target, err)
	}
	if err := coordinator.Validate(); err != nil {
		t.Fatal(err)
	}
	target, err = coordinator.AdmittedCapacityTarget(scaleSet, entity)
	if err != nil || target != 1 {
		t.Fatalf("provisional capacity was downgraded by journal migration: target=%d err=%v", target, err)
	}
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	target, err = coordinator.AdmittedCapacityTarget(scaleSet, entity)
	if err != nil || target != 1 {
		t.Fatalf("available intent was not reserved: target=%d err=%v", target, err)
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, entity, nil, nil, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	target, err = coordinator.AdmittedCapacityTarget(scaleSet, entity)
	if err != nil || target != 0 {
		t.Fatalf("completed intent target=%d err=%v", target, err)
	}
}

func TestAuthoritativeReconciliationReleasesOneExactIntentIdempotently(t *testing.T) {
	now := time.Now().UTC()
	coordinator := testQueueCoordinator(t, &now, nil)
	t.Setenv(queueConfigEnvironment, coordinator.configPath)
	t.Setenv(queueFileEnvironment, coordinator.journalPath)
	t.Setenv(queueLockEnvironment, coordinator.lockPath)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(501, "owner", "repository", now)
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	removed, err := NDDevRemoveQueueIntent(context.Background(), job.JobID)
	if err != nil || !removed {
		t.Fatalf("first reconciliation removed=%t err=%v", removed, err)
	}
	removed, err = NDDevRemoveQueueIntent(context.Background(), job.JobID)
	if err != nil || removed {
		t.Fatalf("idempotent reconciliation removed=%t err=%v", removed, err)
	}
}

func TestStartupMigratesLegacyAssignedOwnershipAndPhaseTTLs(t *testing.T) {
	now := time.Date(2026, 8, 20, 6, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinatorOfWidth(t, &now, nil, 3, 3)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(601, "owner", "repository", now.Add(-time.Hour)),
		testQueueJob(602, "owner", "repository", now.Add(-time.Hour)),
		testQueueJob(603, "owner", "repository", now.Add(-time.Hour)),
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	for index, job := range jobs {
		intent, err := queueIntentFromJob(scaleSet, job, now.Add(-time.Hour), 24*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		intent.State = queueStateAssigned
		if index == 0 {
			intent.RunnerRequestID = 0
		}
		if index == 2 {
			intent.State = queueStateAcquired
		}
		journal.Intents[intent.Key] = intent
	}
	if err := writeQueueIntentJournal(coordinator.journalPath, journal); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Validate(); err != nil {
		t.Fatal(err)
	}
	migrated, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	observed := migrated.Intents[queueIntentKey(11, jobs[0].JobID)]
	reserved := migrated.Intents[queueIntentKey(11, jobs[1].JobID)]
	acquired := migrated.Intents[queueIntentKey(11, jobs[2].JobID)]
	if observed.State != queueStateAssigned || observed.ExpiresAt.After(now.Add(2*time.Minute)) {
		t.Fatalf("legacy observation = %#v", observed)
	}
	if reserved.State != queueStateAssigned || reserved.ExpiresAt.After(now.Add(2*time.Minute)) {
		t.Fatalf("legacy reservation = %#v", reserved)
	}
	if acquired.State != queueStateAcquired || acquired.ExpiresAt.After(now.Add(10*time.Minute)) {
		t.Fatalf("legacy acquisition = %#v", acquired)
	}
}

func TestStartupPromotesDurableQueuedIntentWithoutWebhookRedelivery(t *testing.T) {
	now := time.Date(2026, 8, 20, 8, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(701, "owner", "repository", now.Add(-time.Minute))
	assigned := job
	assigned.RunnerRequestID = 0
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(assigned), []params.ScaleSetJobMessage{assigned}, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(11, job.JobID)
	intent := journal.Intents[key]
	intent.State = queueStateQueued
	intent.UpdatedAt = now.Add(-time.Minute)
	intent.ExpiresAt = now.Add(9 * time.Minute)
	journal.Intents[key] = intent
	if err := writeQueueIntentJournal(coordinator.journalPath, journal); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Validate(); err != nil {
		t.Fatal(err)
	}
	recovered, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := recovered.Intents[key]; got.State != queueStateAssigned ||
		got.ExpiresAt.After(now.Add(2*time.Minute)) {
		t.Fatalf("restart recovery did not promote bounded provisional ownership: %#v", got)
	}
}

func TestQueueCoordinatorSelectsPriorityBeforeAcquireJobs(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, map[string]queueRepositoryPolicy{})
	standard := testQueueScaleSet(11, "nddev-linux-standard")
	release := testQueueScaleSet(12, "nddev-linux-release")
	standardJob := testQueueJob(101, "owner", "standard", now.Add(-2*time.Minute))
	releaseJob := testQueueJob(202, "owner", "release", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(release, []params.ScaleSetJobMessage{releaseJob}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ObserveAvailable(standard, []params.ScaleSetJobMessage{standardJob}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(standard, []params.ScaleSetJobMessage{standardJob})
	if err != nil || len(selected) != 0 {
		t.Fatalf("standard selected before release: %v, %v", selected, err)
	}
	selected, err = coordinator.SelectForAcquire(release, []params.ScaleSetJobMessage{releaseJob})
	if err != nil || len(selected) != 1 || selected[0] != releaseJob.RunnerRequestID {
		t.Fatalf("release selection = %v, %v", selected, err)
	}
	if err := coordinator.CompleteAcquire(release, selected, selected); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ObserveLifecycle(release, testQueueEntityForJob(releaseJob), nil, nil, []params.ScaleSetJobMessage{releaseJob}); err != nil {
		t.Fatal(err)
	}
	assignedStandard := standardJob
	assignedStandard.RunnerRequestID = 0
	if deferred, err := coordinator.ObserveLifecycle(standard, testQueueEntityForJob(assignedStandard), []params.ScaleSetJobMessage{assignedStandard}, nil, nil); err != nil || deferred {
		t.Fatalf("standard readmission deferred=%t err=%v", deferred, err)
	}
	selected, err = coordinator.SelectForAcquire(standard, []params.ScaleSetJobMessage{standardJob})
	if err != nil || len(selected) != 1 || selected[0] != standardJob.RunnerRequestID {
		t.Fatalf("standard selection after release = %v, %v", selected, err)
	}
}

func TestQueueCoordinatorUsesDurableWeightedStrideFairness(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, map[string]queueRepositoryPolicy{
		"owner/heavy": {Weight: 2, MaxInFlight: 1},
		"owner/light": {Weight: 1, MaxInFlight: 1},
	})
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	jobs := make([]params.ScaleSetJobMessage, 0, 6)
	for id := int64(1); id <= 3; id++ {
		jobs = append(jobs, testQueueJob(id, "owner", "heavy", now.Add(-time.Minute)))
		jobs = append(jobs, testQueueJob(100+id, "owner", "light", now.Add(-time.Minute)))
	}
	for _, job := range jobs {
		if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
			t.Fatal(err)
		}
	}
	selectedByRepository := map[string]int{}
	remaining := append([]params.ScaleSetJobMessage(nil), jobs...)
	for range len(jobs) {
		for _, job := range remaining {
			assigned := job
			assigned.RunnerRequestID = 0
			if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(assigned), []params.ScaleSetJobMessage{assigned}, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		selected, err := coordinator.SelectForAcquire(scaleSet, remaining)
		if err != nil || len(selected) != 1 {
			t.Fatalf("selection = %v, %v", selected, err)
		}
		var selectedJob params.ScaleSetJobMessage
		next := remaining[:0]
		for _, job := range remaining {
			if job.RunnerRequestID == selected[0] {
				selectedJob = job
			} else {
				next = append(next, job)
			}
		}
		remaining = next
		selectedByRepository[selectedJob.RepositoryName]++
		if err := coordinator.CompleteAcquire(scaleSet, selected, selected); err != nil {
			t.Fatal(err)
		}
		if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(selectedJob), nil, nil, []params.ScaleSetJobMessage{selectedJob}); err != nil {
			t.Fatal(err)
		}
	}
	if selectedByRepository["heavy"] != 3 || selectedByRepository["light"] != 3 {
		// The finite fixture has equal demand, so every job must eventually run.
		t.Fatalf("jobs starved: %#v", selectedByRepository)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Repositories["owner/heavy"].Pass*2 != journal.Repositories["owner/light"].Pass {
		t.Fatalf("weighted stride state = %#v", journal.Repositories)
	}
}

func TestQueueCoordinatorDuplicateDeliveryCannotDowngradeAcquiredIntent(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.CompleteAcquire(scaleSet, selected, selected); err != nil {
		t.Fatal(err)
	}
	before, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := journal.Intents[queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)].State; got != queueStateAcquired {
		t.Fatalf("duplicate delivery downgraded state to %q", got)
	}
	if journal.Generation != before.Generation || journal.Intents[queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)].ExpiresAt !=
		before.Intents[queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)].ExpiresAt {
		t.Fatalf("redelivery refreshed durable state: before=%#v after=%#v", before, journal)
	}
}

func TestQueueCoordinatorRejectsChangedDuplicateIdentity(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	changed := job
	changed.JobWorkflowRef = "owner/repo/.github/workflows/other.yml@refs/heads/feature"
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{changed}); err == nil {
		t.Fatal("changed duplicate identity was accepted")
	}
}

func TestQueueCoordinatorDelayedLifecycleCannotDowngradeRunning(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.CompleteAcquire(scaleSet, selected, selected); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(job), nil, []params.ScaleSetJobMessage{job}, nil); err != nil {
		t.Fatal(err)
	}
	before, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(job), []params.ScaleSetJobMessage{job}, nil, nil); err != nil {
		t.Fatal(err)
	}
	after, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
	if after.Intents[key].State != queueStateRunning || after.Intents[key].ExpiresAt != before.Intents[key].ExpiresAt || after.Generation != before.Generation {
		t.Fatalf("delayed assigned event changed running state: before=%#v after=%#v", before, after)
	}
}

func TestQueueCoordinatorConcurrentSelectionHasOneWinner(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "one", now.Add(-time.Minute)),
		testQueueJob(102, "owner", "two", now.Add(-time.Minute)),
	}
	for _, job := range jobs {
		if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
			t.Fatal(err)
		}
	}
	var wait sync.WaitGroup
	winners := make(chan []int64, 2)
	errors := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			selected, err := coordinator.SelectForAcquire(scaleSet, jobs)
			winners <- selected
			errors <- err
		}()
	}
	wait.Wait()
	close(winners)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	selected := 0
	for winner := range winners {
		selected += len(winner)
	}
	if selected != 1 {
		t.Fatalf("concurrent winners = %d, want 1", selected)
	}
}

func TestQueueCoordinatorRetainsOnlyUnacquiredAvailableMessage(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	standard := testQueueScaleSet(11, "nddev-linux-standard")
	release := testQueueScaleSet(12, "nddev-linux-release")
	standardJob := testQueueJob(101, "owner", "standard", now.Add(-2*time.Minute))
	releaseJob := testQueueJob(202, "owner", "release", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(release, []params.ScaleSetJobMessage{releaseJob}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ObserveAvailable(standard, []params.ScaleSetJobMessage{standardJob}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(standard, []params.ScaleSetJobMessage{standardJob})
	if err != nil || len(selected) != 0 {
		t.Fatalf("standard selection=%v err=%v", selected, err)
	}
	pending, err := coordinator.HasQueuedAvailable(standard, []params.ScaleSetJobMessage{standardJob})
	if err != nil || !pending {
		t.Fatalf("unacquired standard pending=%v err=%v", pending, err)
	}
	selected, err = coordinator.SelectForAcquire(release, []params.ScaleSetJobMessage{releaseJob})
	if err != nil || len(selected) != 1 {
		t.Fatalf("release selection=%v err=%v", selected, err)
	}
	if err := coordinator.CompleteAcquire(release, selected, selected); err != nil {
		t.Fatal(err)
	}
	pending, err = coordinator.HasQueuedAvailable(release, []params.ScaleSetJobMessage{releaseJob})
	if err != nil || pending {
		t.Fatalf("acquired release pending=%v err=%v", pending, err)
	}
}

// A batched message is GitHub's decision about delivery, not a breach of our
// capacity. These two used to assert the opposite -- that a message carrying two
// jobs must be refused -- and that refusal is what took the fleet down: it is
// not durable, so the message stayed unacknowledged, GitHub redelivered it, and
// the listener spun. 28502 refusals in one hour on gha-runner-1, placing
// nothing, while the same jobs cycled assigned/completed inside one millisecond
// (#253, #275).
func TestQueueCoordinatorRecordsABatchedAvailableMessage(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "one", now.Add(-2*time.Minute)),
		testQueueJob(102, "owner", "two", now.Add(-time.Minute)),
	}
	if err := coordinator.ObserveAvailable(scaleSet, jobs); err != nil {
		t.Fatalf("a batched available message was refused: %v", err)
	}
	// Capacity is unchanged: one of the two wins the slot and the other waits.
	selected, err := coordinator.SelectForAcquire(scaleSet, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 {
		t.Fatalf("selected %d runner requests from a two-job batch, want exactly one", len(selected))
	}
	if selected[0] != jobs[0].RunnerRequestID {
		t.Fatalf("selected %d, want the older job %d", selected[0], jobs[0].RunnerRequestID)
	}
}

func TestQueueCoordinatorRecordsABatchedAssignedMessage(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "one", now.Add(-2*time.Minute)),
		testQueueJob(102, "owner", "two", now.Add(-time.Minute)),
	}
	for index := range jobs {
		jobs[index].RunnerRequestID = 0
		jobs[index].MessageType = params.MessageTypeJobAssigned
	}
	deferred, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(jobs[0]), jobs, nil, nil)
	if err != nil {
		t.Fatalf("a batched assigned message was refused: %v", err)
	}
	// Both are durable before acknowledgement. The fair scheduler grants one
	// short provisional slot and leaves the other queued at the global width.
	if deferred {
		t.Fatal("a durable assigned batch was retained and would block completion messages")
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	states := map[queueIntentState]int{}
	for _, intent := range journal.Intents {
		states[intent.State]++
	}
	if states[queueStateAssigned] != 1 || states[queueStateQueued] != 1 {
		t.Fatalf("batched assigned state = %#v", states)
	}
}

func TestQueueCoordinatorCompletionPromotesQueuedIntentWithoutRedelivery(t *testing.T) {
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-fast")
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "one", now.Add(-2*time.Minute)),
		testQueueJob(102, "owner", "one", now.Add(-time.Minute)),
	}
	available := append([]params.ScaleSetJobMessage(nil), jobs...)
	for index := range jobs {
		jobs[index].RunnerRequestID = 0
		jobs[index].MessageType = params.MessageTypeJobAssigned
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(jobs[0]), jobs, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.ObserveAvailable(scaleSet, available); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, testQueueEntityForJob(jobs[0]), nil, nil, jobs[:1]); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	remaining := journal.Intents[queueIntentKey(int64(scaleSet.ScaleSetID), jobs[1].JobID)]
	if len(journal.Intents) != 1 || remaining.State != queueStateAssigned {
		t.Fatalf("completion did not promote queued intent: %#v", journal.Intents)
	}
}

// A started job whose intent is missing must not refuse the message. GitHub is
// reporting something already true -- a runner has the job -- and refusing does
// not stop it. It only means nothing in the batch is acknowledged, so GitHub
// redelivers the same message forever and every other job on the scale set
// stops moving.
//
// Observed 2026-08-15: one such job produced 29,472 redeliveries in thirty
// minutes and the standard class served nothing for sixteen hours. The intent
// was missing because the scale set had been recreated and its identifier
// moved, so keys built from the current identifier could not match entries
// written under the previous one.
func TestQueueCoordinatorAcknowledgesAStartedJobWithNoIntent(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")

	orphan := testQueueJob(999, "owner", "gone", now.Add(-time.Hour))
	orphan.MessageType = params.MessageTypeJobStarted
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(orphan), nil, []params.ScaleSetJobMessage{orphan}, nil,
	); err != nil {
		t.Fatalf("a started job with no intent was refused, which poisons the queue: %v", err)
	}
}

func TestQueueCoordinatorTransfersAssignedCapacityToTheJobGitHubActuallyStarted(t *testing.T) {
	now := time.Date(2026, 8, 20, 20, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	reserved := testQueueJob(101, "example-org", "reserved", now.Add(-time.Minute))
	reserved.MessageType = params.MessageTypeJobAssigned
	reserved.RunnerRequestID = 0
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(reserved), []params.ScaleSetJobMessage{reserved}, nil, nil,
	); err != nil {
		t.Fatal(err)
	}
	before, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	reservation := before.Intents[queueIntentKey(11, reserved.JobID)]

	started := testQueueJob(202, "example-org", "actual", now)
	started.MessageType = params.MessageTypeJobStarted
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(started), nil, []params.ScaleSetJobMessage{started}, nil,
	); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Intents[queueIntentKey(11, reserved.JobID)]; exists {
		t.Fatal("the substituted job kept a second capacity token")
	}
	actual, exists := journal.Intents[queueIntentKey(11, started.JobID)]
	if !exists || actual.State != queueStateRunning || actual.QueueTime != reservation.QueueTime {
		t.Fatalf("capacity reservation was not transferred: %#v", actual)
	}
	if total, _ := queueInFlight(&journal); total != 1 {
		t.Fatalf("reservation transfer changed in-flight width: %d", total)
	}
}

func TestQueueCoordinatorDoesNotRecreateAnOrphanStartedIntentFromSameBatch(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(999, "example-org", "gone", now.Add(-time.Hour))
	assigned := job
	assigned.MessageType = params.MessageTypeJobAssigned
	assigned.RunnerRequestID = 0
	assigned.OwnerName = ""
	assigned.RepositoryName = ""
	started := assigned
	started.MessageType = params.MessageTypeJobStarted

	for range 2 {
		if _, err := coordinator.ObserveLifecycle(
			scaleSet, testQueueEntityForJob(job), []params.ScaleSetJobMessage{assigned},
			[]params.ScaleSetJobMessage{started}, nil,
		); err != nil {
			t.Fatal(err)
		}
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Intents[queueIntentKey(11, job.JobID)]; exists {
		t.Fatal("orphan started event recreated a phantom running intent")
	}
}

// The same message carrying a job we did admit must still record that job, so
// tolerating the orphan cannot become tolerating everything.
func TestQueueCoordinatorStillRecordsAStartedJobBesideAnOrphan(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")

	known := testQueueJob(101, "owner", "one", now.Add(-time.Minute))
	known.MessageType = params.MessageTypeJobAssigned
	known.RunnerRequestID = 0
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(known), []params.ScaleSetJobMessage{known}, nil, nil,
	); err != nil {
		t.Fatalf("assigning a job failed: %v", err)
	}

	orphan := testQueueJob(999, "owner", "gone", now.Add(-time.Hour))
	orphan.MessageType = params.MessageTypeJobStarted
	started := known
	started.MessageType = params.MessageTypeJobStarted
	if _, err := coordinator.ObserveLifecycle(
		scaleSet, testQueueEntityForJob(known), nil,
		[]params.ScaleSetJobMessage{orphan, started}, nil,
	); err != nil {
		t.Fatalf("a batch mixing an orphan with a known job was refused: %v", err)
	}
}

// A scale set's own ceiling and the queue's width are different statements.
// The queue enforces max_in_flight; the scale set ceiling belongs to GitHub and
// GARM. Refusing a wider scale set here made growing the fleet look like a
// protocol violation: raising max_runners to 3 produced an unacknowledged
// message per delivery and the listener spun without placing anything.
func TestQueueCoordinatorAcceptsAWiderScaleSet(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinatorOfWidth(t, &now, nil, 3, 3)
	wide := testQueueScaleSet(11, "nddev-linux-standard")
	wide.MaxRunners = 3
	job := testQueueJob(101, "owner", "one", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(wide, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatalf("a three-wide scale set was refused: %v", err)
	}
	if _, err := coordinator.ObserveLifecycle(wide, testQueueEntityForJob(job), []params.ScaleSetJobMessage{job}, nil, nil); err != nil {
		t.Fatalf("a three-wide scale set was refused on the lifecycle path: %v", err)
	}
}

// A scale set that admits no runner at all is still a configuration mistake.
func TestQueueCoordinatorRefusesAScaleSetThatAdmitsNoRunner(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	empty := testQueueScaleSet(11, "nddev-linux-standard")
	empty.MaxRunners = 0
	job := testQueueJob(101, "owner", "one", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(empty, []params.ScaleSetJobMessage{job}); err == nil {
		t.Fatal("a zero-wide scale set was accepted")
	}
	if _, err := coordinator.ObserveLifecycle(empty, testQueueEntityForJob(job), []params.ScaleSetJobMessage{job}, nil, nil); err == nil {
		t.Fatal("a zero-wide scale set was accepted on the lifecycle path")
	}
}

// The width is the whole point of the change: three queued jobs on a three-wide
// queue must all be admitted and acquired, where a one-wide queue released them
// one round trip at a time.
func TestQueueCoordinatorDrainsUpToItsWidth(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinatorOfWidth(t, &now, nil, 3, 3)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	scaleSet.MaxRunners = 3
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "repo", now.Add(-3*time.Minute)),
		testQueueJob(102, "owner", "repo", now.Add(-2*time.Minute)),
		testQueueJob(103, "owner", "repo", now.Add(-time.Minute)),
	}
	if err := coordinator.ObserveAvailable(scaleSet, jobs); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 3 {
		t.Fatalf("a three-wide queue released %d of 3 jobs", len(selected))
	}
}

// Width is not a licence to ignore the per-repository share. One repository
// must not take the whole fleet while another waits.
func TestQueueCoordinatorStillCapsOneRepositoryBelowItsWidth(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinatorOfWidth(t, &now, map[string]queueRepositoryPolicy{
		"owner/greedy": {Weight: 1, MaxInFlight: 2},
	}, 4, 4)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	scaleSet.MaxRunners = 4
	jobs := []params.ScaleSetJobMessage{
		testQueueJob(101, "owner", "greedy", now.Add(-4*time.Minute)),
		testQueueJob(102, "owner", "greedy", now.Add(-3*time.Minute)),
		testQueueJob(103, "owner", "greedy", now.Add(-2*time.Minute)),
	}
	if err := coordinator.ObserveAvailable(scaleSet, jobs); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, jobs)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 {
		t.Fatalf("a repository limited to 2 released %d jobs on a 4-wide queue", len(selected))
	}
}

// A per-repository limit above the global width is a promise the queue can
// never keep, so it is refused at load rather than silently clamped.
func TestQueueAdmissionConfigRefusesARepositoryWiderThanTheQueue(t *testing.T) {
	config := queueAdmissionConfig{
		SchemaVersion: 1, MaxInFlight: 2, DefaultRepositoryLimit: 1, DefaultWeight: 1,
		QueuedTTLSeconds: 600, AcquiringTTLSeconds: 120, AcquiredTTLSeconds: 600,
		ExecutionTTLSeconds: 86400, PriorityAgingSeconds: 300,
		Repositories: map[string]queueRepositoryPolicy{"owner/repo": {Weight: 1, MaxInFlight: 3}},
	}
	if err := config.Validate(); err == nil {
		t.Fatal("a repository allowed 3 in flight on a 2-wide queue was accepted")
	}
	config.Repositories["owner/repo"] = queueRepositoryPolicy{Weight: 1, MaxInFlight: 2}
	if err := config.Validate(); err != nil {
		t.Fatalf("a repository at exactly the queue width was refused: %v", err)
	}
}

// The width is bounded. An unbounded number here would ask for more workers
// than every host together can hold.
func TestQueueAdmissionConfigRefusesAnUnboundedWidth(t *testing.T) {
	config := queueAdmissionConfig{
		SchemaVersion: 1, MaxInFlight: queueMaxInFlightCeiling + 1, DefaultRepositoryLimit: 1, DefaultWeight: 1,
		QueuedTTLSeconds: 600, AcquiringTTLSeconds: 120, AcquiredTTLSeconds: 600,
		ExecutionTTLSeconds: 86400, PriorityAgingSeconds: 300,
		Repositories: map[string]queueRepositoryPolicy{},
	}
	if err := config.Validate(); err == nil {
		t.Fatalf("a width above the %d ceiling was accepted", queueMaxInFlightCeiling)
	}
}

func TestQueueCoordinatorCleansExpiredCrashReservation(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil || len(selected) != 1 {
		t.Fatalf("initial selection = %v, %v", selected, err)
	}
	now = now.Add(121 * time.Second)
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	selected, err = coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil || len(selected) != 1 {
		t.Fatalf("expired reservation was not recovered: %v, %v", selected, err)
	}
}

func TestQueueCoordinatorRetriesCrashReservationWithoutDoubleChargingStride(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	first, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil || len(first) != 1 {
		t.Fatalf("first selection=%v err=%v", first, err)
	}
	before, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	immediate, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil || len(immediate) != 0 {
		t.Fatalf("fresh acquiring reservation was retried concurrently: %v, %v", immediate, err)
	}
	now = now.Add(6 * time.Second)
	retried, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil || len(retried) != 1 || retried[0] != job.RunnerRequestID {
		t.Fatalf("crash retry=%v err=%v", retried, err)
	}
	after, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Repositories["owner/repo"].Pass != before.Repositories["owner/repo"].Pass {
		t.Fatalf("crash retry charged stride twice: before=%#v after=%#v", before.Repositories, after.Repositories)
	}
	if err := coordinator.CompleteAcquire(scaleSet, retried, nil); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
	if journal.Intents[key].State != queueStateAcquiring {
		t.Fatalf("ambiguous successful acquire did not retain bounded acquiring state: %#v", journal)
	}
}

func TestQueueCoordinatorAPIFailureReturnsReservationToAssigned(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "owner", "repo", now.Add(-time.Minute))
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatal(err)
	}
	selected, err := coordinator.SelectForAcquire(scaleSet, []params.ScaleSetJobMessage{job})
	if err != nil {
		t.Fatal(err)
	}
	if err := coordinator.FailAcquire(scaleSet, selected); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
	if journal.Intents[key].State != queueStateAssigned {
		t.Fatalf("failed API reservation lost its admitted assignment: %#v", journal)
	}
}

func TestQueueCoordinatorAdmitsLiveAssignedUUIDWithoutRunnerRequestID(t *testing.T) {
	now := time.Date(2026, 8, 9, 15, 20, 46, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(11, "nddev-linux-standard")
	job := testQueueJob(101, "example-user", "github-actions", now.Add(-time.Second))
	job.MessageType = params.MessageTypeJobAssigned
	job.JobID = "5c3077ba-3664-5824-b2cf-e22a31b25f43"
	job.RunnerRequestID = 0
	entity := testQueueEntityForJob(job)
	job.OwnerName = ""
	job.RepositoryName = ""
	job.JobWorkflowRef = ""
	job.EventName = ""
	job.QueueTime = time.Time{}
	deferred, err := coordinator.ObserveLifecycle(scaleSet, entity, []params.ScaleSetJobMessage{job}, nil, nil)
	if err != nil || deferred {
		t.Fatalf("live assigned identity deferred=%t err=%v", deferred, err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
	intent := journal.Intents[key]
	if intent.JobID != job.JobID || intent.RunnerRequestID != 0 || intent.State != queueStateAssigned {
		t.Fatalf("live assigned intent = %#v", intent)
	}
	available := testQueueJob(101, "example-user", "github-actions", now.Add(-time.Second))
	available.JobID = job.JobID
	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{available}); err != nil {
		t.Fatal(err)
	}
	journal, err = readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	intent = journal.Intents[key]
	if intent.RunnerRequestID != available.RunnerRequestID || intent.WorkflowRef != available.JobWorkflowRef ||
		intent.EventName != available.EventName || !intent.QueueTime.Equal(available.QueueTime) {
		t.Fatalf("available job did not enrich assigned intent: %#v", intent)
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, entity, nil,
		[]params.ScaleSetJobMessage{job}, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatalf("mixed started/completed lifecycle failed: %v", err)
	}
	if _, err := coordinator.ObserveLifecycle(scaleSet, entity,
		[]params.ScaleSetJobMessage{job}, []params.ScaleSetJobMessage{job}, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatalf("mixed terminal lifecycle redelivery failed: %v", err)
	}
	journal, err = readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Intents[key]; exists {
		t.Fatalf("completed mixed lifecycle retained intent: %#v", journal.Intents[key])
	}
}

func testQueueCoordinator(t *testing.T, now *time.Time, repositories map[string]queueRepositoryPolicy) *queueIntentCoordinator {
	t.Helper()
	return testQueueCoordinatorOfWidth(t, now, repositories, 1, 1)
}

func testQueueCoordinatorOfWidth(t *testing.T, now *time.Time, repositories map[string]queueRepositoryPolicy, maxInFlight, defaultRepositoryLimit int) *queueIntentCoordinator {
	t.Helper()
	directory := t.TempDir()
	if repositories == nil {
		repositories = map[string]queueRepositoryPolicy{}
	}
	config := queueAdmissionConfig{
		SchemaVersion:          1,
		MaxInFlight:            maxInFlight,
		DefaultRepositoryLimit: defaultRepositoryLimit,
		DefaultWeight:          1,
		QueuedTTLSeconds:       600,
		AcquiringTTLSeconds:    120,
		AcquiredTTLSeconds:     600,
		ExecutionTTLSeconds:    86400,
		PriorityAgingSeconds:   300,
		Repositories:           repositories,
	}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(directory, "queue-admission.json")
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	coordinator := &queueIntentCoordinator{
		configPath:  configPath,
		journalPath: filepath.Join(directory, "queue-intents.json"),
		lockPath:    filepath.Join(directory, "queue-intents.lock"),
		now:         func() time.Time { return *now },
	}
	if err := coordinator.Validate(); err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func testQueueScaleSet(id int, name string) params.ScaleSet {
	return params.ScaleSet{ScaleSetID: id, Name: name, MaxRunners: 1}
}

func testQueueEntityForJob(job params.ScaleSetJobMessage) params.ForgeEntity {
	return params.ForgeEntity{
		Owner:      job.OwnerName,
		Name:       job.RepositoryName,
		EntityType: params.ForgeEntityTypeRepository,
	}
}

func testQueueJob(id int64, owner, repository string, queuedAt time.Time) params.ScaleSetJobMessage {
	return params.ScaleSetJobMessage{
		MessageType:     params.MessageTypeJobAvailable,
		JobID:           fmt.Sprintf("00000000-0000-4000-8000-%012d", id),
		RunnerRequestID: id,
		RepositoryName:  repository,
		OwnerName:       owner,
		JobWorkflowRef:  owner + "/" + repository + "/.github/workflows/ci.yml@refs/heads/feature",
		EventName:       "push",
		QueueTime:       queuedAt,
	}
}

func testQueueOrganizationEntity(owner string) params.ForgeEntity {
	return params.ForgeEntity{
		Owner:      owner,
		Name:       owner,
		EntityType: params.ForgeEntityTypeOrganization,
	}
}

// An organization scale set is registered on GitHub and assigned jobs like any
// other, so refusing its assigned message left GitHub handing out work that
// GARM answered by creating no runner at all -- a queue that never moved and
// never said why. The assigned message names only the job, so the account is
// the identity available, and the repository arrives with JobAvailable.
func TestOrganizationEntityIsAdmittedAndBindsItsRepositoryLater(t *testing.T) {
	now := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(21, "nddev-linux-integration")

	job := testQueueJob(301, "example-guild", "ai_stp", now.Add(-time.Second))
	assigned := job
	assigned.MessageType = params.MessageTypeJobAssigned
	assigned.RunnerRequestID = 0
	assigned.OwnerName = ""
	assigned.RepositoryName = ""
	assigned.JobWorkflowRef = ""
	assigned.EventName = ""
	assigned.QueueTime = time.Time{}

	deferred, err := coordinator.ObserveLifecycle(scaleSet,
		testQueueOrganizationEntity("example-guild"),
		[]params.ScaleSetJobMessage{assigned}, nil, nil)
	if err != nil {
		t.Fatalf("organization entity was refused admission: %v", err)
	}
	if deferred {
		t.Fatal("organization intent was deferred with a free budget")
	}

	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	key := queueIntentKey(21, assigned.JobID)
	intent, exists := journal.Intents[key]
	if !exists {
		t.Fatal("no intent was recorded for the assigned organization job")
	}
	if intent.Owner != "example-guild" {
		t.Fatalf("owner = %q", intent.Owner)
	}
	if intent.Repository != "example-guild" || queueIntentRepositoryBound(intent) {
		t.Fatalf("repository = %q, want the unbound account", intent.Repository)
	}

	if err := coordinator.ObserveAvailable(scaleSet, []params.ScaleSetJobMessage{job}); err != nil {
		t.Fatalf("the available message could not bind the repository: %v", err)
	}
	journal, err = readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	bound := journal.Intents[key]
	if bound.Repository != "example-guild/ai_stp" {
		t.Fatalf("bound repository = %q", bound.Repository)
	}
	if bound.RunnerRequestID != 301 {
		t.Fatalf("runner request = %d", bound.RunnerRequestID)
	}
}

// The account admitted is the account that may spend the slot. A repository
// belonging to someone else must not bind to it, or one tenant's admission
// would run another tenant's job.
func TestOrganizationIntentRefusesARepositoryOutsideItsAccount(t *testing.T) {
	admitted := queueIntent{
		Key: "k", ScaleSetID: 21, JobID: "j", ScaleSetName: "nddev-linux-integration",
		Owner: "example-guild", Repository: "example-guild",
	}
	foreign := admitted
	foreign.Repository = "example-org/example-actions"
	if queueIntentCoreIdentityEqual(admitted, foreign) {
		t.Fatal("an intent admitted for one account bound a repository owned by another")
	}

	sameAccount := admitted
	sameAccount.Repository = "example-guild/example-project"
	if !queueIntentCoreIdentityEqual(admitted, sameAccount) {
		t.Fatal("the account's own repository was refused")
	}

	// Once bound, it is immutable again.
	rebound := sameAccount
	rebound.Repository = "example-guild/other"
	if queueIntentCoreIdentityEqual(sameAccount, rebound) {
		t.Fatal("a bound repository was allowed to change")
	}
}

// A repository entity still carries a complete identity from the assigned
// message, so nothing about the single-tenant path moves.
func TestRepositoryEntityStillBindsAtAssignment(t *testing.T) {
	now := time.Date(2026, 8, 12, 16, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	scaleSet := testQueueScaleSet(22, "nddev-linux-standard")

	job := testQueueJob(302, "example-org", "github-actions", now.Add(-time.Second))
	entity := testQueueEntityForJob(job)
	assigned := job
	assigned.MessageType = params.MessageTypeJobAssigned
	assigned.RunnerRequestID = 0
	assigned.OwnerName = ""
	assigned.RepositoryName = ""

	if _, err := coordinator.ObserveLifecycle(scaleSet, entity,
		[]params.ScaleSetJobMessage{assigned}, nil, nil); err != nil {
		t.Fatal(err)
	}
	journal, err := readQueueIntentJournal(coordinator.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := journal.Intents[queueIntentKey(22, assigned.JobID)]
	if intent.Repository != "example-org/github-actions" || !queueIntentRepositoryBound(intent) || intent.State != queueStateAssigned {
		t.Fatalf("repository entity intent = %q, want it bound at assignment", intent.Repository)
	}
}

// Reservations and acquisitions before JobStarted use phase TTLs; only a
// running job may use the execution horizon.
func TestOnlyRunningIntentsUseTheExecutionHorizon(t *testing.T) {
	now := time.Date(2026, 8, 9, 14, 0, 0, 0, time.UTC)
	coordinator := testQueueCoordinator(t, &now, nil)
	config, err := coordinator.loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	assigned := expiryForState(config, queueStateAssigned, now)
	acquired := expiryForState(config, queueStateAcquired, now)
	running := expiryForState(config, queueStateRunning, now)
	if !assigned.Before(acquired) || !acquired.Before(running) {
		t.Fatalf("lifecycle expiries are not phase-specific: assigned=%s acquired=%s running=%s", assigned, acquired, running)
	}
	if got := assigned.Sub(now); got != time.Duration(config.AcquiringTTLSeconds)*time.Second {
		t.Fatalf("assigned reservation lifetime=%s, want acquiring TTL", got)
	}
	if got := acquired.Sub(now); got != time.Duration(config.AcquiredTTLSeconds)*time.Second {
		t.Fatalf("acquired lifetime=%s, want acquired TTL", got)
	}
}
