package providerjournal

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) Store {
	t.Helper()
	directory := t.TempDir()
	now := time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)
	return Store{
		Path:     filepath.Join(directory, "journal.json"),
		LockPath: filepath.Join(directory, "journal.lock"),
		Now:      func() time.Time { return now },
	}
}

func validLease(name string, now time.Time) Lease {
	return Lease{
		InstanceName:     name,
		ControllerID:     "controller-test",
		PoolID:           "pool-test",
		PoolName:         "nddev-linux-standard",
		VCPU:             4,
		MemoryMiB:        10240,
		ImageFingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		State:            StateAdmitted,
		AdmittedAt:       now,
		UpdatedAt:        now,
		ExpiresAt:        now.Add(5 * time.Minute),
	}
}

func TestStoreReadAbsentReturnsEmptyJournal(t *testing.T) {
	store := testStore(t)
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.SchemaVersion != SchemaVersion || journal.Generation != 0 || len(journal.Leases) != 0 || len(journal.Claims) != 0 {
		t.Fatalf("unexpected empty journal: %#v", journal)
	}
}

func TestStoreReadsPreviousSchemasAsCurrent(t *testing.T) {
	store := testStore(t)
	for _, document := range []string{
		`{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","leases":{}}`,
		`{"schema_version":2,"generation":0,"updated_at":"0001-01-01T00:00:00Z","leases":{},"claims":{}}`,
		`{"schema_version":3,"generation":0,"updated_at":"0001-01-01T00:00:00Z","leases":{},"claims":{}}`,
	} {
		if err := os.WriteFile(store.Path, []byte(document), 0o600); err != nil {
			t.Fatal(err)
		}
		journal, err := store.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if journal.SchemaVersion != SchemaVersion || journal.Claims == nil || len(journal.Claims) != 0 || journal.WarmPreemptionsTotal != 0 {
			t.Fatalf("legacy migration=%#v", journal)
		}
	}
}

func TestStoreReadOnlyDoesNotCreateOrOpenMutationLock(t *testing.T) {
	store := testStore(t)
	journal, err := store.ReadOnly(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.SchemaVersion != SchemaVersion || len(journal.Leases) != 0 {
		t.Fatalf("unexpected empty journal: %#v", journal)
	}
	if _, err := os.Lstat(store.LockPath); !os.IsNotExist(err) {
		t.Fatalf("read-only observation created a lock file: %v", err)
	}

	_, err = store.Update(context.Background(), func(journal *Journal) error {
		journal.Leases["runner-1"] = validLease("runner-1", store.Now())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.LockPath); err != nil {
		t.Fatal(err)
	}
	read, err := store.ReadOnly(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if read.Generation != 1 || read.Leases["runner-1"].InstanceName != "runner-1" {
		t.Fatalf("read-only journal mismatch: %#v", read)
	}
	if _, err := os.Lstat(store.LockPath); !os.IsNotExist(err) {
		t.Fatalf("read-only observation recreated a lock file: %v", err)
	}
}

func TestStoreReadOnlyHonorsCancelledContext(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReadOnly(ctx); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("cancelled read error = %v", err)
	}
}

func TestStoreUpdatePersistsPrivateAtomicJournal(t *testing.T) {
	store := testStore(t)
	now := store.Now()
	journal, err := store.Update(context.Background(), func(journal *Journal) error {
		journal.Leases["runner-1"] = validLease("runner-1", now)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if journal.Generation != 1 || !journal.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected generation metadata: %#v", journal)
	}
	info, err := os.Lstat(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("journal mode=%v, want private regular 0600", info.Mode())
	}

	read, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if read.Generation != 1 || read.Leases["runner-1"].InstanceName != "runner-1" {
		t.Fatalf("persisted journal mismatch: %#v", read)
	}

	noOp, err := store.Update(context.Background(), func(*Journal) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if noOp.Generation != 1 {
		t.Fatalf("no-op update changed generation to %d", noOp.Generation)
	}
}

func TestStoreMutationErrorLeavesJournalUnchanged(t *testing.T) {
	store := testStore(t)
	want := errors.New("reject mutation")
	_, err := store.Update(context.Background(), func(journal *Journal) error {
		journal.Leases["runner-1"] = validLease("runner-1", store.Now())
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("error=%v, want %v", err, want)
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Generation != 0 || len(journal.Leases) != 0 {
		t.Fatalf("failed mutation leaked state: %#v", journal)
	}
}

func TestStoreRejectsUnknownOrInsecureJournal(t *testing.T) {
	store := testStore(t)
	unknown := `{"schema_version":1,"generation":0,"updated_at":"0001-01-01T00:00:00Z","leases":{},"unknown":true}`
	if err := os.WriteFile(store.Path, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background()); err == nil {
		t.Fatal("unknown field was accepted")
	}
	if err := os.Chmod(store.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Read(context.Background()); err == nil {
		t.Fatal("world-readable journal was accepted")
	}
}

func TestJournalRejectsInvalidPreemptionOwnership(t *testing.T) {
	store := testStore(t)
	now := store.Now()
	for _, mutate := range []func(*Journal){
		func(journal *Journal) {
			victim := validLease("warm", now)
			victim.PoolID = "warm/nddev-linux-standard"
			victim.State = StateWarmReady
			victim.PreemptedBy = "missing"
			journal.Leases[victim.InstanceName] = victim
		},
		func(journal *Journal) {
			target := validLease("runner", now)
			victim := validLease("warm", now)
			victim.PoolID = "warm/nddev-linux-standard"
			victim.State = StateDeleting
			victim.PreemptedBy = target.InstanceName
			target.PoolID = "warm/nddev-linux-integration"
			journal.Leases[target.InstanceName] = target
			journal.Leases[victim.InstanceName] = victim
		},
		func(journal *Journal) {
			target := validLease("runner", now)
			victim := validLease("warm", now)
			victim.PoolID = "warm/nddev-linux-standard"
			victim.State = StateWarmReady
			victim.PreemptedBy = target.InstanceName
			journal.Leases[target.InstanceName] = target
			journal.Leases[victim.InstanceName] = victim
			journal.Claims["job"] = Claim{
				JobName: "job", InstanceName: victim.InstanceName, ControllerID: victim.ControllerID,
				PoolID: "pool-test", PoolName: victim.PoolName, ImageFingerprint: victim.ImageFingerprint,
				State: ClaimReserved, ReservedAt: now, UpdatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
			}
		},
	} {
		_, err := store.Update(context.Background(), func(journal *Journal) error {
			mutate(journal)
			return nil
		})
		if err == nil {
			t.Fatal("invalid preemption ownership was accepted")
		}
	}
}

func TestStoreSerializesConcurrentUpdates(t *testing.T) {
	store := testStore(t)
	const writers = 24
	var wait sync.WaitGroup
	errorsChannel := make(chan error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			name := fmt.Sprintf("runner-%02d", index)
			_, err := store.Update(context.Background(), func(journal *Journal) error {
				journal.Leases[name] = validLease(name, store.Now())
				return nil
			})
			errorsChannel <- err
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	journal, err := store.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if journal.Generation != writers || len(journal.Leases) != writers {
		t.Fatalf("generation=%d leases=%d, want %d", journal.Generation, len(journal.Leases), writers)
	}
}
