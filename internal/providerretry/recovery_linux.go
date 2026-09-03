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

	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

const minimumTerminalRecoveryAge = time.Minute

const maximumQueueJournalBytes = 1024 * 1024

type record struct {
	JobID          string    `json:"job_id"`
	Attempts       int       `json:"attempts"`
	LastErrorClass string    `json:"last_error_class,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextAllowedAt  time.Time `json:"next_allowed_at"`
	TerminalUntil  time.Time `json:"terminal_until,omitempty"`
	ProbeOwner     string    `json:"probe_owner,omitempty"`
	WakeReason     string    `json:"wake_reason,omitempty"`
	ScaleSetName   string    `json:"scale_set_name,omitempty"`
	Owner          string    `json:"owner,omitempty"`
}

type journal struct {
	SchemaVersion int                    `json:"schema_version"`
	Generation    uint64                 `json:"generation"`
	UpdatedAt     time.Time              `json:"updated_at"`
	Records       map[string]record      `json:"records"`
	Reservations  map[string]reservation `json:"reservations,omitempty"`
}

type reservation struct {
	RetryKey     string    `json:"retry_key"`
	ScaleSetName string    `json:"scale_set_name"`
	UpdatedAt    time.Time `json:"updated_at"`
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
	Generation                uint64         `json:"generation"`
	Records                   int            `json:"records"`
	Reservations              int            `json:"reservations"`
	DeferredRecords           int            `json:"deferred_records"`
	TerminalCircuits          int            `json:"terminal_circuits"`
	ByErrorClass              map[string]int `json:"by_error_class"`
	DeferredByErrorClass      map[string]int `json:"deferred_by_error_class"`
	OldestTerminalAgeSeconds  int64          `json:"oldest_terminal_age_seconds"`
	NextRetryDelaySeconds     int64          `json:"next_retry_delay_seconds"`
	SharedCapacitySaturated   bool           `json:"shared_capacity_saturated"`
	SharedCapacityProbeOwned  bool           `json:"shared_capacity_probe_owned"`
	SharedCapacityProbeActive bool           `json:"shared_capacity_probe_active"`
	SharedCapacityWaiters     int            `json:"shared_capacity_waiters"`
	SharedCapacityAgeSeconds  int64          `json:"shared_capacity_age_seconds"`
	SharedCapacityWakeReason  string         `json:"shared_capacity_wake_reason,omitempty"`
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
		Generation:           state.Generation,
		Records:              len(state.Records),
		Reservations:         len(state.Reservations),
		ByErrorClass:         retryClassCounts(),
		DeferredByErrorClass: retryClassCounts(),
	}
	for key, retry := range state.Records {
		if key == "capacity-domain:measured-fleet" {
			// Presence is not saturation. The record outlives the refusal that
			// wrote it, so reporting its existence as a live fact made this
			// read 1 in 80.3% of samples over seven days -- including, measured
			// on 2026-08-29 at 12:47, with zero queue intents, zero visible
			// instances, zero waiters, no probe, and a state record already 900
			// seconds old. The metric's own help text claims the present tense:
			// "whether a provider refusal has proven the shared
			// measured-capacity domain saturated".
			//
			// The same window test the deferred count below already applies is
			// what makes it present tense: the domain is saturated while it is
			// still holding creates back, and not afterwards. Every neighbouring
			// field in this block was already computed against now; this one
			// alone ignored it.
			result.SharedCapacitySaturated = retry.NextAllowedAt.After(now) || retry.TerminalUntil.After(now)
			result.SharedCapacityProbeOwned = retry.ProbeOwner != ""
			result.SharedCapacityProbeActive = retry.WakeReason == "probe-leased" && retry.NextAllowedAt.After(now)
			result.SharedCapacityAgeSeconds = max(int64(0), int64(now.Sub(retry.UpdatedAt)/time.Second))
			result.SharedCapacityWakeReason = retry.WakeReason
		}
		if key != "capacity-domain:measured-fleet" && retryDomainKey(key) == key && retry.LastErrorClass == "capacity" {
			result.SharedCapacityWaiters++
		}
		class := retry.LastErrorClass
		if _, known := result.ByErrorClass[class]; !known {
			class = "unknown"
		}
		result.ByErrorClass[class]++
		// A concrete create writes a short-lived duplicate-suppression guard
		// before provider execution. It has no error class and may remain while
		// the successful create is being consolidated; it is not a retry failure.
		deferred := strings.TrimSpace(retry.LastErrorClass) != "" &&
			(retry.NextAllowedAt.After(now) || retry.TerminalUntil.After(now))
		if deferred {
			result.DeferredRecords++
			result.DeferredByErrorClass[class]++
		}
		if deferred && retry.NextAllowedAt.After(now) {
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

func retryClassCounts() map[string]int {
	return map[string]int{"capacity": 0, "identity": 0, "intent": 0, "provider": 0, "timeout": 0, "unknown": 0}
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
	return recoverTerminalRecord(ctx, journalPath, lockPath, key, entityID, scaleSetID, errorClass, expectedUpdatedAt, 8, nil, apply)
}

func RecoverExactJobTerminal(ctx context.Context, journalPath, lockPath, queuePath, key, entityID string, scaleSetID uint, errorClass string, expectedUpdatedAt time.Time, apply bool) (RecoveryResult, error) {
	jobID, parsedEntityID, parsedScaleSetID, err := parseExactJobKey(key)
	if err != nil || parsedEntityID != entityID || parsedScaleSetID != scaleSetID ||
		(errorClass != "provider" && errorClass != "identity" && errorClass != "timeout") || expectedUpdatedAt.IsZero() {
		return RecoveryResult{}, errors.New("exact job retry key, entity_id, scale_set_id, recoverable error class and updated_at are required")
	}
	if !filepath.IsAbs(queuePath) || filepath.Dir(queuePath) != filepath.Dir(journalPath) {
		return RecoveryResult{}, errors.New("queue journal must be an absolute retry-journal sibling")
	}
	return recoverTerminalRecord(ctx, journalPath, lockPath, key, entityID, scaleSetID, errorClass, expectedUpdatedAt, 3, func(now time.Time) error {
		return requireActiveQueueJob(queuePath, jobID, now)
	}, apply)
}

func recoverTerminalRecord(ctx context.Context, journalPath, lockPath, key, entityID string, scaleSetID uint, errorClass string, expectedUpdatedAt time.Time, attempts int, prove func(time.Time) error, apply bool) (RecoveryResult, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryResult{}, err
	}
	if !filepath.IsAbs(journalPath) || !filepath.IsAbs(lockPath) || filepath.Dir(journalPath) != filepath.Dir(lockPath) || journalPath == lockPath {
		return RecoveryResult{}, errors.New("retry journal and lock must be distinct absolute siblings")
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
	if !exists || retry.JobID != key || retry.Attempts != attempts || retry.LastErrorClass != errorClass || retry.TerminalUntil.IsZero() {
		return RecoveryResult{}, errors.New("retry record is not the exact terminal circuit")
	}
	if !retry.UpdatedAt.Equal(expectedUpdatedAt.UTC()) {
		return RecoveryResult{}, errors.New("retry record updated_at changed")
	}
	now := time.Now().UTC()
	if now.Sub(retry.UpdatedAt) < minimumTerminalRecoveryAge {
		return RecoveryResult{}, errors.New("retry circuit is inside the recovery grace period")
	}
	if prove != nil {
		if err := prove(now); err != nil {
			return RecoveryResult{}, err
		}
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

func parseExactJobKey(key string) (jobID, entityID string, scaleSetID uint, err error) {
	domain, jobID, found := strings.Cut(key, ":job:")
	if !found || jobID == "" || strings.Contains(jobID, ":") {
		return "", "", 0, errors.New("retry key is not an exact job key")
	}
	entityID, scaleSetID, err = parseScaleSetDomainKey(domain)
	return jobID, entityID, scaleSetID, err
}

func requireActiveQueueJob(path, jobID string, now time.Time) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > maximumQueueJournalBytes {
		return errors.New("queue journal must be a bounded private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read queue journal: %w", err)
	}
	var queue struct {
		SchemaVersion int `json:"schema_version"`
		Intents       map[string]struct {
			JobID     string    `json:"job_id"`
			State     string    `json:"state"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"intents"`
	}
	if err := json.Unmarshal(data, &queue); err != nil || queue.SchemaVersion != queueintent.SchemaVersion || queue.Intents == nil {
		return errors.New("queue journal identity is invalid")
	}
	for _, intent := range queue.Intents {
		if intent.JobID != jobID || !intent.ExpiresAt.After(now) {
			continue
		}
		switch intent.State {
		case "queued", "acquiring", "acquired", "assigned":
			return nil
		}
	}
	return errors.New("exact retry job is not active in the queue journal")
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
	if (state.SchemaVersion != 1 && state.SchemaVersion != 2) || state.Records == nil ||
		(state.SchemaVersion == 2 && state.Reservations == nil) {
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
