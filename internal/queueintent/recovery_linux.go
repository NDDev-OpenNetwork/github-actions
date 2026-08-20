//go:build linux

package queueintent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const minimumUnboundRunningRecoveryAge = time.Minute

type RecoveryResult struct {
	Applied            bool      `json:"applied"`
	Key                string    `json:"key"`
	ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
	PreviousGeneration uint64    `json:"previous_generation"`
	Generation         uint64    `json:"generation"`
	RecoveredAt        time.Time `json:"recovered_at"`
}

// RecoverUnboundRunning removes one exact stale organization intent while
// GARM is stopped. It uses GARM's sibling lock and an identity-checked atomic
// rename; callers must separately prove that no provider lease exists.
func RecoverUnboundRunning(ctx context.Context, journalPath, lockPath, key string, expectedUpdatedAt time.Time, apply bool) (RecoveryResult, error) {
	return recoverExactUnbound(ctx, journalPath, lockPath, key, expectedUpdatedAt, apply, StateRunning)
}

// RecoverCanceledUnbound removes one exact queued or assigned intent after the
// operator has proved its GitHub run/job is completed with conclusion
// cancelled. The intent must never have crossed the JobAvailable boundary.
func RecoverCanceledUnbound(ctx context.Context, journalPath, lockPath, key string, expectedUpdatedAt time.Time, apply bool) (RecoveryResult, error) {
	return recoverExactUnbound(ctx, journalPath, lockPath, key, expectedUpdatedAt, apply, "canceled-unbound")
}

func recoverExactUnbound(ctx context.Context, journalPath, lockPath, key string, expectedUpdatedAt time.Time, apply bool, recoveryKind State) (RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}
	if !filepath.IsAbs(journalPath) || !filepath.IsAbs(lockPath) || filepath.Dir(journalPath) != filepath.Dir(lockPath) || journalPath == lockPath {
		return RecoveryResult{}, errors.New("queue journal and lock must be distinct absolute siblings")
	}
	if !validText(key) || expectedUpdatedAt.IsZero() {
		return RecoveryResult{}, errors.New("exact queue key and updated_at are required")
	}
	parent := filepath.Dir(journalPath)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return RecoveryResult{}, errors.New("queue state parent must be a real directory")
	}
	lockFD, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("open queue lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), lockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return RecoveryResult{}, fmt.Errorf("lock queue journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := requirePrivateOwnedRegular(lock, "queue lock"); err != nil {
		return RecoveryResult{}, err
	}

	beforeInfo, err := os.Lstat(journalPath)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("stat queue journal: %w", err)
	}
	journalFile, err := os.Open(journalPath)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("open queue journal: %w", err)
	}
	if err := requirePrivateOwnedRegular(journalFile, "queue journal"); err != nil {
		journalFile.Close()
		return RecoveryResult{}, err
	}
	journalFile.Close()
	journal, err := readJournal(journalPath)
	if err != nil {
		return RecoveryResult{}, err
	}
	intent, exists := journal.Intents[key]
	if !exists {
		return RecoveryResult{}, errors.New("exact queue intent is absent")
	}
	now := time.Now().UTC()
	unbound := intent.RunnerRequestID == 0 && intent.Repository == intent.Owner &&
		intent.WorkflowRef == "unavailable-before-job-available" && intent.EventName == "unavailable-before-job-available"
	validState := intent.State == StateRunning
	errorText := "queue intent is not an unbound running lifecycle orphan"
	if recoveryKind == "canceled-unbound" {
		validState = intent.State == StateQueued || intent.State == StateAssigned
		errorText = "queue intent is not an unbound queued or assigned cancellation"
	}
	if !unbound || !validState {
		return RecoveryResult{}, errors.New(errorText)
	}
	if !intent.UpdatedAt.Equal(expectedUpdatedAt.UTC()) {
		return RecoveryResult{}, errors.New("queue intent updated_at changed")
	}
	if now.Sub(intent.UpdatedAt) < minimumUnboundRunningRecoveryAge {
		return RecoveryResult{}, errors.New("queue intent is inside the recovery grace period")
	}
	result := RecoveryResult{Key: key, ExpectedUpdatedAt: intent.UpdatedAt, PreviousGeneration: journal.Generation, Generation: journal.Generation, RecoveredAt: now}
	if !apply {
		return result, nil
	}
	delete(journal.Intents, key)
	journal.Generation++
	journal.UpdatedAt = now
	if err := journal.Validate(); err != nil {
		return RecoveryResult{}, err
	}
	temporary, err := os.CreateTemp(parent, ".queue-recovery-*")
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("create recovery journal: %w", err)
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		if !published {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return RecoveryResult{}, err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return RecoveryResult{}, fmt.Errorf("encode recovery journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return RecoveryResult{}, fmt.Errorf("sync recovery journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return RecoveryResult{}, fmt.Errorf("close recovery journal: %w", err)
	}
	afterInfo, err := os.Lstat(journalPath)
	if err != nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Size() != afterInfo.Size() || beforeInfo.ModTime() != afterInfo.ModTime() {
		return RecoveryResult{}, errors.New("queue journal changed during recovery")
	}
	if err := os.Rename(temporaryPath, journalPath); err != nil {
		return RecoveryResult{}, fmt.Errorf("publish recovery journal: %w", err)
	}
	published = true
	directory, err := os.Open(parent)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("open queue directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return RecoveryResult{}, fmt.Errorf("sync queue directory: %w", err)
	}
	result.Applied = true
	result.Generation = journal.Generation
	return result, nil
}

func requirePrivateOwnedRegular(file *os.File, description string) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s must be a private regular file", description)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("%s must be owned by the recovery identity", description)
	}
	return nil
}
