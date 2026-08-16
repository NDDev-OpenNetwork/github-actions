package providerjournal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

const maxJournalBytes = 4 * 1024 * 1024

type Store struct {
	Path     string
	LockPath string
	Now      func() time.Time
}

func (s Store) Read(ctx context.Context) (Journal, error) {
	var snapshot Journal
	err := s.withLock(ctx, func() error {
		journal, err := s.readUnlocked()
		if err != nil {
			return err
		}
		snapshot = cloneJournal(journal)
		return nil
	})
	return snapshot, err
}

// ReadOnly observes one atomically published journal generation without
// opening or creating the lock file. Store updates are fsync+rename based, so a
// reader sees either the complete previous generation or the complete next
// generation. This path is for sandboxed telemetry processes that have no
// write access to the journal directory; lifecycle mutations must use Update.
func (s Store) ReadOnly(ctx context.Context) (Journal, error) {
	if err := ctx.Err(); err != nil {
		return Journal{}, fmt.Errorf("read journal: %w", err)
	}
	if !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) == string(filepath.Separator) {
		return Journal{}, fmt.Errorf("journal path must be absolute and bounded")
	}
	parent := filepath.Dir(s.Path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return Journal{}, fmt.Errorf("resolve journal parent: %w", err)
	}
	if filepath.Clean(resolvedParent) != filepath.Clean(parent) {
		return Journal{}, fmt.Errorf("journal parent must not traverse symlinks")
	}
	journal, err := s.readUnlocked()
	if err != nil {
		return Journal{}, err
	}
	return cloneJournal(journal), nil
}

func (s Store) Update(ctx context.Context, mutate func(*Journal) error) (Journal, error) {
	if mutate == nil {
		return Journal{}, fmt.Errorf("journal mutation is required")
	}
	var snapshot Journal
	err := s.withLock(ctx, func() error {
		journal, err := s.readUnlocked()
		if err != nil {
			return err
		}
		before := cloneJournal(journal)
		if err := mutate(&journal); err != nil {
			return err
		}
		if err := journal.Validate(); err != nil {
			return fmt.Errorf("validate mutated journal: %w", err)
		}
		if journalsEqual(before, journal) {
			snapshot = cloneJournal(journal)
			return nil
		}
		journal.Generation++
		journal.UpdatedAt = s.now()
		if err := s.writeUnlocked(journal); err != nil {
			return err
		}
		snapshot = cloneJournal(journal)
		return nil
	})
	return snapshot, err
}

func (s Store) withLock(ctx context.Context, run func() error) error {
	if !filepath.IsAbs(s.Path) || !filepath.IsAbs(s.LockPath) {
		return fmt.Errorf("journal and lock paths must be absolute")
	}
	if filepath.Clean(s.Path) == filepath.Clean(s.LockPath) {
		return fmt.Errorf("journal and lock paths must differ")
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create journal directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.LockPath), 0o700); err != nil {
		return fmt.Errorf("create journal lock directory: %w", err)
	}
	lock, err := acquireFileLock(ctx, s.LockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	return run()
}

func (s Store) readUnlocked() (Journal, error) {
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return newJournal(), nil
	}
	if err != nil {
		return Journal{}, fmt.Errorf("open journal: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return Journal{}, fmt.Errorf("stat journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Journal{}, fmt.Errorf("journal must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return Journal{}, fmt.Errorf("read journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return Journal{}, fmt.Errorf("journal exceeds %d bytes", maxJournalBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Journal{}, fmt.Errorf("journal contains multiple JSON values")
		}
		return Journal{}, fmt.Errorf("decode trailing journal data: %w", err)
	}
	if journal.SchemaVersion == LegacySchemaVersion && journal.Claims == nil {
		journal.Claims = make(map[string]Claim)
	}
	if journal.SchemaVersion == LegacySchemaVersion || journal.SchemaVersion == ClaimsSchemaVersion ||
		journal.SchemaVersion == PreemptionSchemaVersion {
		journal.SchemaVersion = SchemaVersion
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (s Store) writeUnlocked(journal Journal) error {
	directory := filepath.Dir(s.Path)
	temporary, err := os.CreateTemp(directory, ".provider-journal-*")
	if err != nil {
		return fmt.Errorf("create temporary journal: %w", err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary journal permissions: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return fmt.Errorf("encode journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary journal: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, s.Path); err != nil {
		return fmt.Errorf("replace journal: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open journal directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync journal directory: %w", err)
	}
	return nil
}

func (s Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func cloneJournal(journal Journal) Journal {
	clone := journal
	clone.Leases = make(map[string]Lease, len(journal.Leases))
	for key, lease := range journal.Leases {
		clone.Leases[key] = lease
	}
	clone.Claims = make(map[string]Claim, len(journal.Claims))
	for key, claim := range journal.Claims {
		clone.Claims[key] = claim
	}
	return clone
}

func journalsEqual(left, right Journal) bool {
	if left.SchemaVersion != right.SchemaVersion || left.WarmPreemptionsTotal != right.WarmPreemptionsTotal ||
		len(left.Leases) != len(right.Leases) || len(left.Claims) != len(right.Claims) {
		return false
	}
	for key, leftLease := range left.Leases {
		if rightLease, exists := right.Leases[key]; !exists || leftLease != rightLease {
			return false
		}
	}
	for key, leftClaim := range left.Claims {
		if rightClaim, exists := right.Claims[key]; !exists || leftClaim != rightClaim {
			return false
		}
	}
	return true
}
