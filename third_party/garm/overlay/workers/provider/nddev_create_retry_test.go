package provider

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

	"github.com/cloudbase/garm/params"
)

func TestNDDevParallelFailuresSaturateTheDomainAttemptCounter(t *testing.T) {
	now := time.Date(2026, 8, 20, 4, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))

	keys := make([]string, 16)
	for index := range keys {
		keys[index] = fmt.Sprintf("scale-set:entity-one:17:instance:parallel-%02d", index)
		if err := nddevBeforeProviderCreate(context.Background(), keys[index]); err != nil {
			t.Fatalf("reserve %d: %v", index, err)
		}
	}
	var group sync.WaitGroup
	errorsSeen := make(chan error, len(keys))
	for _, key := range keys {
		key := key
		group.Add(1)
		go func() {
			defer group.Done()
			errorsSeen <- nddevRecordProviderCreateFailure(context.Background(), key, errors.New("provider transport failed"))
		}()
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("parallel failure corrupted the journal: %v", err)
		}
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	domain := journal.Records["scale-set:entity-one:17"]
	if domain.Attempts != nddevRetryMaximum || domain.TerminalUntil.IsZero() {
		t.Fatalf("parallel domain = %#v", domain)
	}
}

func TestNDDevCapacityClassificationCoversEveryAdmissionAndStorageReason(t *testing.T) {
	for _, message := range []string{
		"pool-saturated", "insufficient-cpu", "insufficient-memory", "memory-pressure",
		"cpu-pressure", "disk-pressure", "pressure-unavailable", "recent-oom",
		"io-pressure", "host-unhealthy",
		"project memory limit exceeded", "project disk limit exceeded",
		"instance-count limit exceeded", "storage high-watermark", "no eligible member",
	} {
		if got := nddevProviderErrorClass(errors.New(message)); got != "capacity" {
			t.Fatalf("nddevProviderErrorClass(%q) = %q, want capacity", message, got)
		}
	}
}

func TestNDDevProviderRetryKeyFallsBackToTheStableScaleSetFailureDomain(t *testing.T) {
	key := nddevProviderRetryKey(params.Instance{}, params.ScaleSet{ScaleSetID: 17}, params.ForgeEntity{ID: "entity-one"})
	if key != "scale-set:entity-one:17" {
		t.Fatalf("fallback key=%q", key)
	}
	instance := params.Instance{Name: "runner-one", Job: &params.Job{ScaleSetJobID: "job-one"}}
	if key := nddevProviderRetryKey(instance, params.ScaleSet{ScaleSetID: 17}, params.ForgeEntity{ID: "entity-one"}); key != "scale-set:entity-one:17:job:job-one" {
		t.Fatalf("job key=%q", key)
	}
	instance.Job = nil
	if key := nddevProviderRetryKey(instance, params.ScaleSet{ScaleSetID: 17}, params.ForgeEntity{ID: "entity-one"}); key != "scale-set:entity-one:17:instance:runner-one" {
		t.Fatalf("instance key=%q", key)
	}
}

func TestNDDevProviderRetryIsDurableBoundedAndClearedBySuccess(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	key := "scale-set:entity-one:17:instance:runner-one"
	domainKey := "scale-set:entity-one:17"

	for attempt := 1; attempt <= nddevRetryMaximum; attempt++ {
		if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
			t.Fatalf("attempt %d preflight: %v", attempt, err)
		}
		if err := nddevRecordProviderCreateFailure(context.Background(), key, errors.New("provider transport failed")); err != nil {
			t.Fatalf("attempt %d failure: %v", attempt, err)
		}
		journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
		if err != nil {
			t.Fatal(err)
		}
		record := journal.Records[domainKey]
		if record.Attempts != attempt || record.LastErrorClass != "provider" {
			t.Fatalf("attempt %d domain record=%#v", attempt, record)
		}
		if attempt < nddevRetryMaximum {
			now = record.NextAllowedAt
			if attemptRecord := journal.Records[key]; attemptRecord.NextAllowedAt.After(now) {
				now = attemptRecord.NextAllowedAt
			}
		}
	}
	if err := nddevBeforeProviderCreate(context.Background(), key); err == nil {
		t.Fatal("ninth provider attempt passed an open circuit")
	}
	if err := nddevRecordProviderCreateSuccess(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Records) != 0 {
		t.Fatalf("success retained retry state: %#v", journal.Records)
	}
}

func TestNDDevProviderCapacityBackpressureNeverOpensCircuit(t *testing.T) {
	now := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	key := "scale-set:entity-one:17:instance:runner-capacity"
	domainKey := "scale-set:entity-one:17"

	for attempt := 0; attempt < 32; attempt++ {
		if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
			t.Fatalf("capacity attempt %d preflight: %v", attempt, err)
		}
		if err := nddevRecordProviderCreateFailure(context.Background(), key, errors.New("provider admission rejected pool: insufficient-cpu")); err != nil {
			t.Fatalf("capacity attempt %d failure: %v", attempt, err)
		}
		journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
		if err != nil {
			t.Fatal(err)
		}
		for _, recordKey := range []string{key, domainKey} {
			record := journal.Records[recordKey]
			wantAttempts := min(attempt+1, nddevRetryMaximum)
			if record.Attempts != wantAttempts || record.LastErrorClass != "capacity" || !record.TerminalUntil.IsZero() {
				t.Fatalf("capacity record %q accumulated failure state: %#v", recordKey, record)
			}
		}
		now = journal.Records[key].NextAllowedAt
		if domainNext := journal.Records[domainKey].NextAllowedAt; domainNext.After(now) {
			now = domainNext
		}
	}
}

func TestNDDevCapacityFallbackReachesFiveMinutesWhileDeleteWakeRemainsImmediate(t *testing.T) {
	key := "scale-set:entity-one:17"
	if delay := nddevCapacityRetryDelay(key, nddevRetryMaximum); delay <= time.Minute || delay > 5*time.Minute {
		t.Fatalf("saturated fallback delay = %s, want (1m,5m]", delay)
	}

	now := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	concrete := key + ":instance:waiting"
	if err := nddevBeforeProviderCreate(context.Background(), concrete); err != nil {
		t.Fatal(err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), concrete, errors.New("insufficient-memory")); err != nil {
		t.Fatal(err)
	}
	if err := NDDevProviderCapacityReleased(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Records[key]; exists {
		t.Fatal("completed deletion did not bypass the fallback for the waiting domain")
	}
	if _, exists := journal.Records[concrete]; exists {
		t.Fatal("completed deletion retained the concrete fallback")
	}
}

func TestNDDevCapacityReleaseGrantsOneOldestCapacityDomain(t *testing.T) {
	now := time.Date(2026, 8, 19, 8, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))

	oldestCapacityKey := "scale-set:entity-one:17:instance:capacity-oldest"
	newerCapacityKey := "scale-set:entity-one:19:instance:capacity-newer"
	providerKey := "scale-set:entity-one:18:instance:provider"
	for key, failure := range map[string]error{
		oldestCapacityKey: errors.New("provider admission rejected pool: insufficient-cpu"),
		providerKey:       errors.New("provider transport failed"),
	} {
		if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
			t.Fatal(err)
		}
		if err := nddevRecordProviderCreateFailure(context.Background(), key, failure); err != nil {
			t.Fatal(err)
		}
	}
	now = now.Add(time.Second)
	if err := nddevBeforeProviderCreate(context.Background(), newerCapacityKey); err != nil {
		t.Fatal(err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), newerCapacityKey, errors.New("insufficient-memory")); err != nil {
		t.Fatal(err)
	}
	if err := NDDevProviderCapacityReleased(context.Background()); err != nil {
		t.Fatal(err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := journal.Records[oldestCapacityKey]; exists {
		t.Fatal("capacity release retained the oldest concrete backoff")
	}
	if _, exists := journal.Records[nddevRetryDomainKey(oldestCapacityKey)]; exists {
		t.Fatal("capacity release retained the oldest domain backoff")
	}
	if _, exists := journal.Records[newerCapacityKey]; !exists {
		t.Fatal("one capacity release woke a second concrete attempt")
	}
	if _, exists := journal.Records[nddevRetryDomainKey(newerCapacityKey)]; !exists {
		t.Fatal("one capacity release woke a second retry domain")
	}
	if _, exists := journal.Records[providerKey]; !exists {
		t.Fatal("capacity release erased provider failure")
	}
	if _, exists := journal.Records[nddevRetryDomainKey(providerKey)]; !exists {
		t.Fatal("capacity release erased provider domain failure")
	}
}

func TestNDDevCapacityRetryReservesOneDomainAttempt(t *testing.T) {
	now := time.Date(2026, 8, 21, 13, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	first := "scale-set:entity-one:17:instance:first"
	second := "scale-set:entity-one:17:instance:second"
	third := "scale-set:entity-one:17:instance:third"
	domainKey := nddevRetryDomainKey(first)

	if err := nddevBeforeProviderCreate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), first, errors.New("insufficient-cpu")); err != nil {
		t.Fatal(err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	now = journal.Records[domainKey].NextAllowedAt
	domainAttempts := journal.Records[domainKey].Attempts
	if err := nddevBeforeProviderCreate(context.Background(), second); err != nil {
		t.Fatalf("first eligible domain retry was refused: %v", err)
	}
	journal, err = nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	domain := journal.Records[domainKey]
	if domain.Attempts != domainAttempts || domain.NextAllowedAt != now.Add(nddevRetryAttemptLease) || domain.UpdatedAt != now {
		t.Fatalf("capacity domain lease = %#v", domain)
	}
	if err := nddevBeforeProviderCreate(context.Background(), third); err == nil || !strings.Contains(err.Error(), "retry is deferred") {
		t.Fatalf("parallel capacity retry was not suppressed: %v", err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), second, errors.New("insufficient-memory")); err != nil {
		t.Fatal(err)
	}
	journal, err = nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	if !journal.Records[domainKey].NextAllowedAt.After(now) {
		t.Fatal("failed reserved retry did not restore capacity backoff")
	}
}

func TestNDDevMissingCanceledIntentNeverOpensCircuit(t *testing.T) {
	now := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	key := "scale-set:entity-one:17:instance:runner-canceled"
	domainKey := "scale-set:entity-one:17"

	for attempt := 0; attempt < 32; attempt++ {
		if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
			t.Fatalf("intent attempt %d preflight: %v", attempt, err)
		}
		if err := nddevRecordProviderCreateFailure(context.Background(), key, errors.New("no active pre-AcquireJobs queue intent")); err != nil {
			t.Fatalf("intent attempt %d failure: %v", attempt, err)
		}
		journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
		if err != nil {
			t.Fatal(err)
		}
		for _, recordKey := range []string{key, domainKey} {
			record := journal.Records[recordKey]
			if record.Attempts != 1 || record.LastErrorClass != "intent" || !record.TerminalUntil.IsZero() {
				t.Fatalf("intent record %q accumulated failure state: %#v", recordKey, record)
			}
		}
		now = journal.Records[key].NextAllowedAt
	}
}

func TestNDDevCanceledCreateErrorsClassifyAsIntent(t *testing.T) {
	for _, message := range []string{
		"no active pre-AcquireJobs queue intent",
		"instance stopped during canceled create",
		"guest agent rejected file: Instance is not running",
	} {
		if got := nddevProviderErrorClass(errors.New(message)); got != "intent" {
			t.Fatalf("nddevProviderErrorClass(%q) = %q, want intent", message, got)
		}
	}
}

func TestNDDevProviderRetryJournalPrunesExpiredNonTerminalRecords(t *testing.T) {
	now := time.Date(2026, 8, 18, 11, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	scaleSet := params.ScaleSet{ScaleSetID: 17}
	entity := params.ForgeEntity{ID: "entity-one"}
	key := nddevProviderRetryKey(params.Instance{Name: "runner-stale"}, scaleSet, entity)

	if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), key, errors.New("provider transport failed")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(nddevRetryStaleTTL + time.Second)
	if err := NDDevScaleSetCreateAllowed(context.Background(), scaleSet, entity); err != nil {
		t.Fatalf("stale retry state still blocked the scale set: %v", err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal.Records) != 0 {
		t.Fatalf("stale retry records survived garbage collection: %#v", journal.Records)
	}
}

func TestNDDevProviderRetryReservesBeforeTheProviderCall(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	key := "job-one"

	if err := nddevBeforeProviderCreate(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if err := nddevBeforeProviderCreate(context.Background(), key); err == nil {
		t.Fatal("a concurrent provider attempt ignored the durable attempt lease")
	}
}

func TestNDDevScaleSetCreatePreflightDoesNotReserveAnAttempt(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	scaleSet := params.ScaleSet{ScaleSetID: 17}
	entity := params.ForgeEntity{ID: "entity-one"}

	if err := NDDevScaleSetCreateAllowed(context.Background(), scaleSet, entity); err != nil {
		t.Fatal(err)
	}
	journal, err := nddevReadRetryJournal(os.Getenv(nddevRetryFileEnv))
	if err != nil || len(journal.Records) != 0 {
		t.Fatalf("read-only preflight reserved state: journal=%#v err=%v", journal, err)
	}
	first := nddevProviderRetryKey(params.Instance{Name: "runner-one"}, scaleSet, entity)
	second := nddevProviderRetryKey(params.Instance{Name: "runner-two"}, scaleSet, entity)
	if err := nddevBeforeProviderCreate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := nddevBeforeProviderCreate(context.Background(), second); err != nil {
		t.Fatalf("a distinct admitted instance was serialized: %v", err)
	}
	if err := nddevBeforeProviderCreate(context.Background(), first); err == nil {
		t.Fatal("a duplicate instance ignored its durable attempt lease")
	}
	if err := NDDevScaleSetCreateAllowed(context.Background(), scaleSet, entity); err != nil {
		t.Fatalf("an active attempt incorrectly blocked the scale-set domain: %v", err)
	}
}

func TestNDDevScaleSetFailureStillDefersEveryParallelAttempt(t *testing.T) {
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	originalNow := nddevRetryNow
	nddevRetryNow = func() time.Time { return now }
	t.Cleanup(func() { nddevRetryNow = originalNow })
	directory := t.TempDir()
	t.Setenv(nddevRetryFileEnv, filepath.Join(directory, "retry.json"))
	t.Setenv(nddevRetryLockEnv, filepath.Join(directory, "retry.lock"))
	scaleSet := params.ScaleSet{ScaleSetID: 17}
	entity := params.ForgeEntity{ID: "entity-one"}
	first := nddevProviderRetryKey(params.Instance{Name: "runner-one"}, scaleSet, entity)
	second := nddevProviderRetryKey(params.Instance{Name: "runner-two"}, scaleSet, entity)
	if err := nddevBeforeProviderCreate(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := nddevBeforeProviderCreate(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := nddevRecordProviderCreateFailure(context.Background(), first, errors.New("provider failed")); err != nil {
		t.Fatal(err)
	}
	if err := NDDevScaleSetCreateAllowed(context.Background(), scaleSet, entity); err == nil {
		t.Fatal("scale-set preflight ignored a domain failure backoff")
	}
	if err := nddevRecordProviderCreateSuccess(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := NDDevScaleSetCreateAllowed(context.Background(), scaleSet, entity); err != nil {
		t.Fatalf("a successful parallel attempt did not clear the domain: %v", err)
	}
}
