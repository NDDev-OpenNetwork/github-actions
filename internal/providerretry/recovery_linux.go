//go:build linux

package providerretry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const minimumTerminalRecoveryAge = time.Minute

type record struct {
	JobID          string    `json:"job_id"`
	Attempts       int       `json:"attempts"`
	LastErrorClass string    `json:"last_error_class,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextAllowedAt  time.Time `json:"next_allowed_at"`
	TerminalUntil  time.Time `json:"terminal_until,omitempty"`
}

type journal struct {
	SchemaVersion int               `json:"schema_version"`
	Generation    uint64            `json:"generation"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Records       map[string]record `json:"records"`
}

type RecoveryResult struct {
	Applied            bool      `json:"applied"`
	Key                string    `json:"key"`
	EntityID           string    `json:"entity_id"`
	ScaleSetID         uint      `json:"scale_set_id"`
	ErrorClass         string    `json:"error_class"`
	ExpectedUpdatedAt  time.Time `json:"expected_updated_at"`
	PreviousGeneration uint64    `json:"previous_generation"`
	Generation         uint64    `json:"generation"`
	RecoveredAt        time.Time `json:"recovered_at"`
}

// Snapshot is the bounded, identity-free operational view of the durable
// provider-create retry journal. Dynamic tenant, scale-set, job and runner
// identities deliberately stay out of metrics.
type Snapshot struct {
	Generation               uint64         `json:"generation"`
	Records                  int            `json:"records"`
	TerminalCircuits         int            `json:"terminal_circuits"`
	ByErrorClass             map[string]int `json:"by_error_class"`
	OldestTerminalAgeSeconds int64          `json:"oldest_terminal_age_seconds"`
	NextRetryDelaySeconds    int64          `json:"next_retry_delay_seconds"`
}

// Inspect returns a read-only summary. A terminal domain is counted once even
// when concrete per-instance attempts remain, because only the domain record
// can block an entire scale set.
func Inspect(path string, now time.Time) (Snapshot, error) {
	state, err := readJournal(path)
	if err != nil {
		return Snapshot{}, err
	}
	if now.IsZero() {
		return Snapshot{}, errors.New("retry observation time is required")
	}
	now = now.UTC()
	result := Snapshot{
		Generation:   state.Generation,
		Records:      len(state.Records),
		ByErrorClass: map[string]int{"capacity": 0, "identity": 0, "intent": 0, "provider": 0, "timeout": 0, "unknown": 0},
	}
	for key, retry := range state.Records {
		class := retry.LastErrorClass
		if _, known := result.ByErrorClass[class]; !known {
			class = "unknown"
		}
		result.ByErrorClass[class]++
		if retry.NextAllowedAt.After(now) {
			delay := int64(retry.NextAllowedAt.Sub(now) / time.Second)
			if result.NextRetryDelaySeconds == 0 || delay < result.NextRetryDelaySeconds {
				result.NextRetryDelaySeconds = delay
			}
		}
		if retry.TerminalUntil.After(now) && retryDomainKey(key) == key {
			result.TerminalCircuits++
			age := max(int64(0), int64(now.Sub(retry.UpdatedAt)/time.Second))
			if age > result.OldestTerminalAgeSeconds {
				result.OldestTerminalAgeSeconds = age
			}
		}
	}
	return result, nil
}

func retryDomainKey(key string) string {
	for _, marker := range []string{":instance:", ":job:"} {
		if index := strings.Index(key, marker); index > 0 {
			return key[:index]
		}
	}
	return key
}

func RecoverTerminal(ctx context.Context, journalPath, lockPath, key, entityID string, scaleSetID uint, errorClass string, expectedUpdatedAt time.Time, apply bool) (RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}
	if !filepath.IsAbs(journalPath) || !filepath.IsAbs(lockPath) || filepath.Dir(journalPath) != filepath.Dir(lockPath) || journalPath == lockPath {
		return RecoveryResult{}, errors.New("retry journal and lock must be distinct absolute siblings")
	}
	parsedEntityID, parsedScaleSetID, err := parseScaleSetDomainKey(key)
	if err != nil || parsedEntityID != entityID || parsedScaleSetID != scaleSetID ||
		(errorClass != "provider" && errorClass != "intent" && errorClass != "identity" && errorClass != "timeout") || expectedUpdatedAt.IsZero() {
		return RecoveryResult{}, errors.New("exact scale-set retry key, entity_id, scale_set_id, recoverable error class and updated_at are required")
	}
	parent := filepath.Dir(journalPath)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return RecoveryResult{}, errors.New("retry state parent must be a real directory")
	}
	lockFD, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("open retry lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), lockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return RecoveryResult{}, fmt.Errorf("lock retry journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := requirePrivateOwnedRegular(lock, "retry lock"); err != nil {
		return RecoveryResult{}, err
	}
	beforeInfo, err := os.Lstat(journalPath)
	if err != nil {
		return RecoveryResult{}, err
	}
	state, err := readJournal(journalPath)
	if err != nil {
		return RecoveryResult{}, err
	}
	retry, exists := state.Records[key]
	if !exists || retry.JobID != key || retry.Attempts != 8 || retry.LastErrorClass != errorClass || retry.TerminalUntil.IsZero() {
		return RecoveryResult{}, errors.New("retry record is not the exact terminal circuit")
	}
	if !retry.UpdatedAt.Equal(expectedUpdatedAt.UTC()) {
		return RecoveryResult{}, errors.New("retry record updated_at changed")
	}
	now := time.Now().UTC()
	if now.Sub(retry.UpdatedAt) < minimumTerminalRecoveryAge {
		return RecoveryResult{}, errors.New("retry circuit is inside the recovery grace period")
	}
	result := RecoveryResult{Key: key, EntityID: entityID, ScaleSetID: scaleSetID, ErrorClass: errorClass, ExpectedUpdatedAt: retry.UpdatedAt, PreviousGeneration: state.Generation, Generation: state.Generation, RecoveredAt: now}
	if !apply {
		return result, nil
	}
	delete(state.Records, key)
	state.Generation++
	state.UpdatedAt = now
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return RecoveryResult{}, err
	}
	content = append(content, '\n')
	temporary, err := os.CreateTemp(parent, ".retry-recovery-*")
	if err != nil {
		return RecoveryResult{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		return RecoveryResult{}, err
	}
	if _, err := temporary.Write(content); err != nil {
		return RecoveryResult{}, err
	}
	if err := temporary.Sync(); err != nil {
		return RecoveryResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return RecoveryResult{}, err
	}
	afterInfo, err := os.Lstat(journalPath)
	if err != nil || !os.SameFile(beforeInfo, afterInfo) || beforeInfo.Size() != afterInfo.Size() || beforeInfo.ModTime() != afterInfo.ModTime() {
		return RecoveryResult{}, errors.New("retry journal changed during recovery")
	}
	if err := os.Rename(temporaryPath, journalPath); err != nil {
		return RecoveryResult{}, err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return RecoveryResult{}, err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return RecoveryResult{}, err
	}
	result.Applied = true
	result.Generation = state.Generation
	return result, nil
}

// parseScaleSetDomainKey accepts only the terminal failure domain written by
// GARM: scale-set:<forge entity UUID>:<scale-set database ID>. Concrete
// :job/:instance: retry keys are deliberately not recoverable through the
// operator command. The immutable forge entity identity is what separates
// same-named pools belonging to different accounts and repositories.
func parseScaleSetDomainKey(key string) (string, uint, error) {
	if key == "" || len(key) > 256 {
		return "", 0, errors.New("scale-set retry key is invalid")
	}
	parts := strings.Split(key, ":")
	if len(parts) != 3 || parts[0] != "scale-set" || strings.TrimSpace(parts[1]) == "" {
		return "", 0, errors.New("retry key is not an exact scale-set failure domain")
	}
	parsed, err := strconv.ParseUint(parts[2], 10, 32)
	if err != nil || parsed == 0 {
		return "", 0, errors.New("retry key has invalid scale-set identity")
	}
	return parts[1], uint(parsed), nil
}

func readJournal(path string) (journal, error) {
	file, err := os.Open(path)
	if err != nil {
		return journal{}, err
	}
	defer file.Close()
	if err := requirePrivateOwnedRegular(file, "retry journal"); err != nil {
		return journal{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(data) > 1024*1024 {
		return journal{}, errors.New("retry journal has invalid bounded content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state journal
	if err := decoder.Decode(&state); err != nil {
		return journal{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return journal{}, errors.New("retry journal has trailing data")
	}
	if state.SchemaVersion != 1 || state.Records == nil {
		return journal{}, errors.New("retry journal identity is invalid")
	}
	return state, nil
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
