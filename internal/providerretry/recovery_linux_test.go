//go:build linux

package providerretry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoverTerminalUsesExactCASAndGeneration(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:org-id:1"
	state := journal{SchemaVersion: 1, Generation: 9, UpdatedAt: updatedAt, Records: map[string]record{key: {
		JobID: key, Attempts: 8, LastErrorClass: "provider", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, false)
	if err != nil || dry.Applied || dry.Generation != 9 {
		t.Fatalf("dry recovery=%#v err=%v", dry, err)
	}
	applied, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, true)
	if err != nil || !applied.Applied || applied.Generation != 10 {
		t.Fatalf("applied recovery=%#v err=%v", applied, err)
	}
	observed, err := readJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := observed.Records[key]; exists || observed.Generation != 10 {
		t.Fatalf("recovered journal=%#v", observed)
	}
}

func TestRecoverTerminalRejectsNonTerminalOrChangedRecord(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:org-id:1"
	state := journal{SchemaVersion: 1, Records: map[string]record{key: {
		JobID: key, Attempts: 7, LastErrorClass: "provider", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt.Add(time.Minute),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverTerminal(context.Background(), journalPath, lockPath, key, "org-id", 1, "provider", updatedAt, true); err == nil {
		t.Fatal("non-terminal record was recoverable")
	}
}

func TestRecoverTerminalRejectsWrongTenantOrConcreteRetryKey(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:account-a:17"
	state := journal{SchemaVersion: 1, Records: map[string]record{key: {
		JobID: key, Attempts: 8, LastErrorClass: "identity", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
	}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		key        string
		entityID   string
		scaleSetID uint
	}{
		{name: "wrong account", key: key, entityID: "account-b", scaleSetID: 17},
		{name: "wrong scale set", key: key, entityID: "account-a", scaleSetID: 18},
		{name: "concrete instance", key: key + ":instance:runner-one", entityID: "account-a", scaleSetID: 17},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RecoverTerminal(context.Background(), journalPath, lockPath, test.key, test.entityID, test.scaleSetID, "identity", updatedAt, false); err == nil {
				t.Fatal("mismatched recovery identity was accepted")
			}
		})
	}
}

func TestRecoverExactJobTerminalRequiresLiveQueueProofAndCAS(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	lockPath := filepath.Join(directory, "create-retries.lock")
	queuePath := filepath.Join(directory, "queue-intents.json")
	updatedAt := time.Now().UTC().Add(-time.Hour)
	key := "scale-set:entity-one:3:job:job-one"
	state := journal{SchemaVersion: 2, Generation: 19, UpdatedAt: updatedAt, Records: map[string]record{key: {
		JobID: key, Attempts: 3, LastErrorClass: "provider", UpdatedAt: updatedAt,
		NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
	}}, Reservations: map[string]reservation{"runner-existing": {RetryKey: "scale-set:other:2:job:other", UpdatedAt: updatedAt}}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	queue := fmt.Sprintf(`{"schema_version":4,"intents":{"github-scale-set-job:v2:3:job-one":{"job_id":"job-one","state":"assigned","expires_at":%q}}}`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano))
	if err := os.WriteFile(queuePath, []byte(queue), 0o600); err != nil {
		t.Fatal(err)
	}
	dry, err := RecoverExactJobTerminal(context.Background(), journalPath, lockPath, queuePath, key, "entity-one", 3, "provider", updatedAt, false)
	if err != nil || dry.Applied || dry.Generation != 19 {
		t.Fatalf("dry exact recovery=%#v err=%v", dry, err)
	}
	applied, err := RecoverExactJobTerminal(context.Background(), journalPath, lockPath, queuePath, key, "entity-one", 3, "provider", updatedAt, true)
	if err != nil || !applied.Applied || applied.Generation != 20 {
		t.Fatalf("applied exact recovery=%#v err=%v", applied, err)
	}
	observed, err := readJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := observed.Records[key]; exists || observed.Generation != 20 {
		t.Fatalf("exact recovery journal=%#v", observed)
	}
}

func TestRecoverExactJobTerminalRejectsMissingOrInvalidQueueProof(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		queue string
	}{
		{name: "job absent", queue: `{"schema_version":4,"intents":{}}`},
		{name: "job running", queue: `{"schema_version":4,"intents":{"job":{"job_id":"job-one","state":"running","expires_at":"2099-01-01T00:00:00Z"}}}`},
		{name: "wrong schema", queue: `{"schema_version":3,"intents":{"job":{"job_id":"job-one","state":"assigned","expires_at":"2099-01-01T00:00:00Z"}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			journalPath := filepath.Join(directory, "create-retries.json")
			lockPath := filepath.Join(directory, "create-retries.lock")
			queuePath := filepath.Join(directory, "queue-intents.json")
			updatedAt := time.Now().UTC().Add(-time.Hour)
			key := "scale-set:entity-one:3:job:job-one"
			state := journal{SchemaVersion: 2, Generation: 21, UpdatedAt: updatedAt, Records: map[string]record{key: {
				JobID: key, Attempts: 3, LastErrorClass: "provider", UpdatedAt: updatedAt,
				NextAllowedAt: updatedAt, TerminalUntil: updatedAt.Add(24 * time.Hour),
			}}, Reservations: map[string]reservation{"runner-existing": {RetryKey: "scale-set:other:2:job:other", UpdatedAt: updatedAt}}}
			content, _ := json.Marshal(state)
			if err := os.WriteFile(journalPath, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(queuePath, []byte(test.queue), 0o600); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(journalPath)
			if _, err := RecoverExactJobTerminal(context.Background(), journalPath, lockPath, queuePath, key, "entity-one", 3, "provider", updatedAt, true); err == nil {
				t.Fatal("invalid queue proof recovered exact job retry")
			}
			after, _ := os.ReadFile(journalPath)
			if string(after) != string(before) {
				t.Fatal("failed exact recovery mutated retry journal")
			}
		})
	}
}

func TestInspectCountsOnlyTerminalFailureDomains(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	now := time.Date(2026, 8, 21, 7, 0, 0, 0, time.UTC)
	state := journal{SchemaVersion: 1, Generation: 42, UpdatedAt: now.Add(-time.Minute), Records: map[string]record{
		"scale-set:entity-one:3": {
			JobID: "scale-set:entity-one:3", Attempts: 8, LastErrorClass: "provider",
			UpdatedAt: now.Add(-10 * time.Minute), NextAllowedAt: now.Add(-10 * time.Minute), TerminalUntil: now.Add(24 * time.Hour),
		},
		"scale-set:entity-one:3:instance:runner-one": {
			JobID: "scale-set:entity-one:3:instance:runner-one", Attempts: 1, LastErrorClass: "provider",
			UpdatedAt: now.Add(-10 * time.Minute), NextAllowedAt: now.Add(-9 * time.Minute),
		},
		"scale-set:entity-one:4": {
			JobID: "scale-set:entity-one:4", Attempts: 2, LastErrorClass: "capacity",
			UpdatedAt: now.Add(-time.Minute), NextAllowedAt: now.Add(30 * time.Second),
		},
		"scale-set:entity-one:4:instance:create-in-progress": {
			JobID: "scale-set:entity-one:4:instance:create-in-progress", Attempts: 1,
			UpdatedAt: now, NextAllowedAt: now.Add(2 * time.Minute),
		},
	}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Generation != 42 || snapshot.Records != 4 || snapshot.TerminalCircuits != 1 ||
		snapshot.DeferredRecords != 2 || snapshot.DeferredByErrorClass["provider"] != 1 || snapshot.DeferredByErrorClass["capacity"] != 1 ||
		snapshot.OldestTerminalAgeSeconds != 600 || snapshot.NextRetryDelaySeconds != 30 ||
		snapshot.ByErrorClass["provider"] != 2 || snapshot.ByErrorClass["capacity"] != 1 || snapshot.ByErrorClass["unknown"] != 1 {
		t.Fatalf("unexpected retry snapshot: %#v", snapshot)
	}
}

func TestInspectExposesBoundedSharedCapacityProbeState(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	now := time.Date(2026, 8, 23, 5, 0, 0, 0, time.UTC)
	state := journal{SchemaVersion: 1, Generation: 43, UpdatedAt: now, Records: map[string]record{
		"capacity-domain:measured-fleet": {
			JobID: "capacity-domain:measured-fleet", Attempts: 3, LastErrorClass: "capacity",
			UpdatedAt: now.Add(-12 * time.Second), NextAllowedAt: now.Add(108 * time.Second),
			ProbeOwner: "scale-set:private-entity:17:instance:runner-secret", WakeReason: "probe-leased", Owner: "private-org",
		},
		"scale-set:private-entity:17": {
			JobID: "scale-set:private-entity:17", Attempts: 2, LastErrorClass: "capacity",
			UpdatedAt: now.Add(-time.Minute), NextAllowedAt: now.Add(time.Minute), Owner: "private-org",
		},
	}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SharedCapacitySaturated || !snapshot.SharedCapacityProbeOwned || !snapshot.SharedCapacityProbeActive ||
		snapshot.SharedCapacityWaiters != 1 || snapshot.SharedCapacityAgeSeconds != 12 || snapshot.SharedCapacityWakeReason != "probe-leased" {
		t.Fatalf("shared capacity snapshot = %#v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-entity") || strings.Contains(string(encoded), "runner-secret") || strings.Contains(string(encoded), "private-org") {
		t.Fatalf("dynamic probe owner leaked from bounded observation: %s", encoded)
	}
}

func TestInspectCountsSchemaTwoReservationsWithoutExposingIdentity(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	now := time.Date(2026, 8, 24, 8, 30, 0, 0, time.UTC)
	state := journal{
		SchemaVersion: 2,
		Generation:    44,
		UpdatedAt:     now,
		Records:       map[string]record{},
		Reservations: map[string]reservation{
			"private-runner": {
				RetryKey: "scale-set:private-entity:17:job:private-job", ScaleSetName: "private-scale-set", UpdatedAt: now,
			},
		},
	}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Reservations != 1 {
		t.Fatalf("reservation count = %d", snapshot.Reservations)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-runner") || strings.Contains(string(encoded), "private-job") {
		t.Fatalf("reservation identity leaked from snapshot: %s", encoded)
	}
}

// The record outlives the refusal that wrote it, so reporting its existence as
// a live fact made gha_fleet_shared_capacity_saturated read 1 in 80.3% of
// samples over seven days. Measured on the live fleet 2026-08-29 at 12:47 it
// read 1 with zero queue intents, zero visible instances, zero waiters, no
// probe, and a state record already 900 seconds old -- while the metric's help
// text claims a provider refusal "has proven" the domain saturated.
//
// Anyone tuning capacity against it was reading a fact from the past as one
// about the present.
func TestInspectDoesNotCallAnExpiredCapacityDomainSaturated(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	now := time.Date(2026, 8, 29, 12, 47, 0, 0, time.UTC)
	// The shape observed live: the domain record is present and stale, its
	// retry window closed a quarter of an hour ago, and nothing is waiting.
	state := journal{SchemaVersion: 1, Generation: 91, UpdatedAt: now, Records: map[string]record{
		"capacity-domain:measured-fleet": {
			JobID: "capacity-domain:measured-fleet", Attempts: 2, LastErrorClass: "capacity",
			UpdatedAt: now.Add(-900 * time.Second), NextAllowedAt: now.Add(-870 * time.Second),
			WakeReason: "worker-deleted",
		},
	}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SharedCapacitySaturated {
		t.Errorf("an expired capacity domain still reports saturated: age=%ds next_allowed=%s",
			snapshot.SharedCapacityAgeSeconds, state.Records["capacity-domain:measured-fleet"].NextAllowedAt)
	}
	// The record itself must still be observable -- the age and wake reason are
	// how the last refusal is explained. Only the present-tense claim goes.
	if snapshot.SharedCapacityAgeSeconds != 900 || snapshot.SharedCapacityWakeReason != "worker-deleted" {
		t.Errorf("expiring the claim also erased the evidence: %#v", snapshot)
	}
}

// A terminal domain is still holding creates back even after its retry window
// passes, so it stays saturated until the terminal window closes.
func TestInspectKeepsATerminalCapacityDomainSaturated(t *testing.T) {
	directory := t.TempDir()
	journalPath := filepath.Join(directory, "create-retries.json")
	now := time.Date(2026, 8, 29, 12, 47, 0, 0, time.UTC)
	state := journal{SchemaVersion: 1, Generation: 92, UpdatedAt: now, Records: map[string]record{
		"capacity-domain:measured-fleet": {
			JobID: "capacity-domain:measured-fleet", Attempts: 5, LastErrorClass: "capacity",
			UpdatedAt: now.Add(-60 * time.Second), NextAllowedAt: now.Add(-30 * time.Second),
			TerminalUntil: now.Add(300 * time.Second), WakeReason: "capacity-refused",
		},
	}}
	content, _ := json.Marshal(state)
	if err := os.WriteFile(journalPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := Inspect(journalPath, now)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.SharedCapacitySaturated {
		t.Error("a terminal capacity domain stopped reporting saturated while it still blocks creates")
	}
}
