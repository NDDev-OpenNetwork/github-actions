package cachebroker

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

func writeReclaimFixture(t *testing.T, directory string, now time.Time, intents map[string]queueintent.Intent, leases map[string]providerjournal.Lease) (string, string, string) {
	t.Helper()
	queuePath := filepath.Join(directory, "queue-intents.json")
	queueLock := filepath.Join(directory, "queue-intents.lock")
	queue := queueintent.Journal{
		SchemaVersion: queueintent.SchemaVersion, Generation: 4, UpdatedAt: now,
		Intents: intents, Repositories: map[string]queueintent.RepositoryState{}, TerminalJobs: map[string]time.Time{},
	}
	raw, err := json.Marshal(queue)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(queuePath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	providerPath := filepath.Join(directory, "provider-journal.json")
	provider := providerjournal.Journal{SchemaVersion: providerjournal.SchemaVersion, Leases: leases, Claims: map[string]providerjournal.Claim{}}
	rawProvider, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providerPath, append(rawProvider, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	return queuePath, queueLock, providerPath
}

func runningIntent(job, runner string, entered time.Time) queueintent.Intent {
	key := "github-scale-set-job:v2:42:" + job
	return queueintent.Intent{
		Key: key, ScaleSetID: 42, JobID: job, ScaleSetName: "example-standard",
		Owner: "example-org", Repository: "example-org/example-repo", RunnerName: runner,
		WorkflowRef: "example-org/example-repo/.github/workflows/ci.yml@refs/heads/main",
		EventName:   "push", QueueTime: entered.Add(-time.Minute), State: queueintent.StateRunning,
		Priority: 1, WorkflowRunID: 4242, JobDisplayName: "build",
		StateEnteredAt: entered, UpdatedAt: entered, ExpiresAt: entered.Add(24 * time.Hour),
	}
}

// The defect: a running intent whose runner holds no execution lease stays for
// a day and holds an in-flight slot the whole time.
func TestReclaimReleasesARunningIntentWithNoExecutionLease(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	queuePath, queueLock, providerPath := writeReclaimFixture(t, directory, now,
		map[string]queueintent.Intent{
			"github-scale-set-job:v2:42:ghost": runningIntent("ghost", "nddev-ghost", now.Add(-90*time.Minute)),
			"github-scale-set-job:v2:42:live":  runningIntent("live", "nddev-live", now.Add(-90*time.Minute)),
		},
		map[string]providerjournal.Lease{
			"nddev-live": {InstanceName: "nddev-live", ControllerID: "c", PoolID: "p", PoolName: "example-standard",
				VCPU: 2, MemoryMiB: 4096, ImageFingerprint: "fingerprint", State: providerjournal.StateCreated,
				AdmittedAt: now.Add(-90 * time.Minute), UpdatedAt: now, ExpiresAt: now.Add(time.Hour)},
		})
	reclaimer := Reclaimer{
		Correlator:      &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now }},
		ProviderJournal: providerjournal.Store{Path: providerPath},
	}
	released, err := reclaimer.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 1 || released[0] != "nddev-ghost" {
		t.Fatalf("released = %v, want exactly the runner with no lease", released)
	}
	snapshot, err := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active) != 1 || snapshot.Active[0].RunnerName != "nddev-live" {
		t.Fatalf("a covered running intent was released: %+v", snapshot.Active)
	}
}

// The reclaim must never take a job that is merely starting. The provider
// records its lease a moment after the broker writes the running intent.
func TestReclaimNeverTouchesAnIntentInsideTheGrace(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	queuePath, queueLock, providerPath := writeReclaimFixture(t, directory, now,
		map[string]queueintent.Intent{
			"github-scale-set-job:v2:42:starting": runningIntent("starting", "nddev-starting", now.Add(-30*time.Second)),
		},
		map[string]providerjournal.Lease{})
	reclaimer := Reclaimer{
		Correlator:      &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now }},
		ProviderJournal: providerjournal.Store{Path: providerPath},
	}
	released, err := reclaimer.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("released a job inside the grace: %v", released)
	}
	snapshot, err := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active) != 1 {
		t.Fatalf("starting job was removed: %+v", snapshot.Active)
	}
}

// A warm-promoted worker keeps its provider instance name while the runner
// takes GitHub's. The claim carries both, and either one covers the intent.
func TestReclaimTreatsAWarmClaimAsCoverage(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	queuePath, queueLock, providerPath := writeReclaimFixture(t, directory, now,
		map[string]queueintent.Intent{
			"github-scale-set-job:v2:42:warm": runningIntent("warm", "nddev-runtime", now.Add(-90*time.Minute)),
		},
		map[string]providerjournal.Lease{})
	provider := providerjournal.Journal{
		SchemaVersion: providerjournal.SchemaVersion,
		Leases: map[string]providerjournal.Lease{
			"warm-standard-abc": {InstanceName: "warm-standard-abc", ControllerID: "c", PoolID: "p",
				PoolName: "example-standard", VCPU: 2, MemoryMiB: 4096, ImageFingerprint: "fingerprint",
				State: providerjournal.StateWarmClaimed, AdmittedAt: now.Add(-90 * time.Minute),
				UpdatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
		Claims: map[string]providerjournal.Claim{
			"nddev-runtime": {JobName: "nddev-runtime", InstanceName: "warm-standard-abc", ControllerID: "c",
				PoolID: "p", PoolName: "example-standard", ImageFingerprint: "fingerprint",
				State:      providerjournal.ClaimInjected,
				ReservedAt: now.Add(-90 * time.Minute), UpdatedAt: now, ExpiresAt: now.Add(time.Hour)},
		},
	}
	raw, err := json.Marshal(provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(providerPath, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	reclaimer := Reclaimer{
		Correlator:      &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now }},
		ProviderJournal: providerjournal.Store{Path: providerPath},
	}
	released, err := reclaimer.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("released a warm-claimed runner: %v", released)
	}
}

// Nothing but running is this reclaim's business: queued and assigned intents
// already expire on their own bounded horizons.
func TestReclaimLeavesEveryNonRunningState(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	directory := t.TempDir()
	queued := runningIntent("queued", "", now.Add(-90*time.Minute))
	queued.State = queueintent.StateQueued
	assigned := runningIntent("assigned", "", now.Add(-90*time.Minute))
	assigned.State = queueintent.StateAssigned
	queuePath, queueLock, providerPath := writeReclaimFixture(t, directory, now,
		map[string]queueintent.Intent{"github-scale-set-job:v2:42:queued": queued, "github-scale-set-job:v2:42:assigned": assigned},
		map[string]providerjournal.Lease{})
	reclaimer := Reclaimer{
		Correlator:      &queueintent.Correlator{Path: queuePath, LockPath: queueLock, Now: func() time.Time { return now }},
		ProviderJournal: providerjournal.Store{Path: providerPath},
	}
	released, err := reclaimer.Once(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(released) != 0 {
		t.Fatalf("reclaim reached a non-running state: %v", released)
	}
	snapshot, err := (queueintent.Reader{Path: queuePath, Now: func() time.Time { return now }}).ReadActive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Active) != 2 {
		t.Fatalf("non-running intents were removed: %+v", snapshot.Active)
	}
}
