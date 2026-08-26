package cachebroker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"
)

const (
	JournalSchemaVersion = 1
	maximumJournalBytes  = 4 * 1024 * 1024
	ClaimTokenBytes      = 32
	ClaimTTL             = 15 * time.Minute
)

var (
	instancePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{5,63}$`)
	hashPattern     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Claim struct {
	InstanceName      string    `json:"instance_name"`
	PoolName          string    `json:"pool_name"`
	Role              string    `json:"role"`
	TokenSHA256       string    `json:"token_sha256"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ClaimedRepository string    `json:"claimed_repository,omitempty"`
	ClaimedAt         time.Time `json:"claimed_at,omitempty"`
}

type Journal struct {
	SchemaVersion int              `json:"schema_version"`
	Generation    uint64           `json:"generation"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Claims        map[string]Claim `json:"claims"`
}

type Store struct {
	Path, LockPath string
	Now            func() time.Time
}

func HashToken(token []byte) string { sum := sha256.Sum256(token); return hex.EncodeToString(sum[:]) }

func (s Store) Add(ctx context.Context, instance, pool, role string, token []byte) error {
	if len(token) != ClaimTokenBytes {
		return fmt.Errorf("cache claim token must contain %d bytes", ClaimTokenBytes)
	}
	if !instancePattern.MatchString(instance) || pool == "" {
		return errors.New("cache claim ownership is invalid")
	}
	if !validClaimRole(role) {
		return fmt.Errorf("cache claim role %q is invalid", role)
	}
	return s.update(ctx, func(journal *Journal, now time.Time) error {
		if existing, exists := journal.Claims[instance]; exists && existing.ClaimedRepository != "" {
			return fmt.Errorf("cache claim for %q is already bound", instance)
		}
		journal.Claims[instance] = Claim{InstanceName: instance, PoolName: pool, Role: role, TokenSHA256: HashToken(token), CreatedAt: now, ExpiresAt: now.Add(ClaimTTL)}
		return nil
	})
}

func (s Store) Consume(ctx context.Context, instance string, token []byte, repository string) (Claim, error) {
	if len(token) != ClaimTokenBytes {
		return Claim{}, errors.New("cache claim token is invalid")
	}
	var consumed Claim
	err := s.update(ctx, func(journal *Journal, now time.Time) error {
		claim, exists := journal.Claims[instance]
		if !exists {
			return errors.New("cache claim is absent")
		}
		if !claim.ExpiresAt.After(now) {
			delete(journal.Claims, instance)
			return errors.New("cache claim expired")
		}
		actual, err := hex.DecodeString(claim.TokenSHA256)
		if err != nil {
			return errors.New("cache claim hash is invalid")
		}
		digest := sha256.Sum256(token)
		if subtle.ConstantTimeCompare(actual, digest[:]) != 1 {
			return errors.New("cache claim token does not match")
		}
		if claim.ClaimedRepository != "" && claim.ClaimedRepository != repository {
			return errors.New("cache claim is already bound to another repository")
		}
		if claim.ClaimedRepository == "" {
			claim.ClaimedRepository = repository
			claim.ClaimedAt = now
			journal.Claims[instance] = claim
		}
		consumed = claim
		return nil
	})
	return consumed, err
}

func (s Store) Verify(ctx context.Context, instance string, token []byte) (Claim, error) {
	if len(token) != ClaimTokenBytes {
		return Claim{}, errors.New("cache claim token is invalid")
	}
	journal, err := s.Read(ctx)
	if err != nil {
		return Claim{}, err
	}
	claim, exists := journal.Claims[instance]
	if !exists {
		return Claim{}, errors.New("cache claim is absent")
	}
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if !claim.ExpiresAt.After(now) {
		return Claim{}, errors.New("cache claim expired")
	}
	actual, err := hex.DecodeString(claim.TokenSHA256)
	if err != nil {
		return Claim{}, errors.New("cache claim hash is invalid")
	}
	digest := sha256.Sum256(token)
	if subtle.ConstantTimeCompare(actual, digest[:]) != 1 {
		return Claim{}, errors.New("cache claim token does not match")
	}
	return claim, nil
}

func (s Store) Remove(ctx context.Context, instance string) error {
	return s.update(ctx, func(journal *Journal, _ time.Time) error { delete(journal.Claims, instance); return nil })
}

func (s Store) Read(ctx context.Context) (Journal, error) {
	var result Journal
	err := s.withLock(ctx, false, func() error { var err error; result, err = s.readUnlocked(); return err })
	return result, err
}

func (s Store) update(ctx context.Context, mutate func(*Journal, time.Time) error) error {
	return s.withLock(ctx, true, func() error {
		journal, err := s.readUnlocked()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if s.Now != nil {
			now = s.Now().UTC()
		}
		for key, claim := range journal.Claims {
			if !claim.ExpiresAt.After(now) {
				delete(journal.Claims, key)
			}
		}
		before, _ := json.Marshal(journal)
		if err := mutate(&journal, now); err != nil {
			return err
		}
		if err := journal.Validate(); err != nil {
			return err
		}
		after, _ := json.Marshal(journal)
		if bytes.Equal(before, after) {
			return nil
		}
		journal.Generation++
		journal.UpdatedAt = now
		return s.writeUnlocked(journal)
	})
}

func (s Store) withLock(ctx context.Context, exclusive bool, action func() error) error {
	if !filepath.IsAbs(s.Path) || !filepath.IsAbs(s.LockPath) {
		return errors.New("cache claim journal paths must be absolute")
	}
	lock, err := os.OpenFile(s.LockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open cache claim lock: %w", err)
	}
	defer lock.Close()
	operation := syscall.LOCK_SH
	if exclusive {
		operation = syscall.LOCK_EX
	}
	for {
		if err := syscall.Flock(int(lock.Fd()), operation|syscall.LOCK_NB); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return action()
}

func (s Store) readUnlocked() (Journal, error) {
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Journal{SchemaVersion: JournalSchemaVersion, Claims: map[string]Claim{}}, nil
	}
	if err != nil {
		return Journal{}, fmt.Errorf("open cache claim journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Journal{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Journal{}, errors.New("cache claim journal must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumJournalBytes+1))
	if err != nil {
		return Journal{}, err
	}
	if len(raw) > maximumJournalBytes {
		return Journal{}, errors.New("cache claim journal is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode cache claim journal: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Journal{}, errors.New("cache claim journal contains trailing data")
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (s Store) writeUnlocked(journal Journal) error {
	directory := filepath.Dir(s.Path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".cache-claims-*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	keep := false
	defer func() {
		temporary.Close()
		if !keep {
			_ = os.Remove(name)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.Path); err != nil {
		return err
	}
	keep = true
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (j Journal) Validate() error {
	if j.SchemaVersion != JournalSchemaVersion || j.Claims == nil {
		return errors.New("cache claim journal schema is invalid")
	}
	keys := make([]string, 0, len(j.Claims))
	for key := range j.Claims {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		claim := j.Claims[key]
		if key != claim.InstanceName || !instancePattern.MatchString(key) || claim.PoolName == "" || !hashPattern.MatchString(claim.TokenSHA256) {
			return fmt.Errorf("cache claim %q has invalid identity", key)
		}
		if !validClaimRole(claim.Role) || claim.CreatedAt.IsZero() || !claim.ExpiresAt.After(claim.CreatedAt) || claim.ExpiresAt.Sub(claim.CreatedAt) > ClaimTTL {
			return fmt.Errorf("cache claim %q has invalid contract", key)
		}
		if (claim.ClaimedRepository == "") != claim.ClaimedAt.IsZero() {
			return fmt.Errorf("cache claim %q has incomplete claimed state", key)
		}
		if claim.ClaimedRepository != "" {
			if _, _, err := splitRepository(claim.ClaimedRepository); err != nil || claim.ClaimedAt.Before(claim.CreatedAt) || claim.ClaimedAt.After(claim.ExpiresAt) {
				return fmt.Errorf("cache claim %q has invalid claimed state", key)
			}
		}
	}
	return nil
}

func validClaimRole(role string) bool {
	if role == "correlation-only" {
		return true
	}
	_, _, ok := roleContract(role)
	return ok
}
