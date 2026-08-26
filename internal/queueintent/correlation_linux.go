//go:build linux

package queueintent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrRunningCorrelationNotReady = errors.New("exact running queue intent is not ready")

type RunningCorrelation struct {
	RunnerName     string
	PoolName       string
	Repository     string
	WorkflowRunID  int64
	JobDisplayName string
	WorkflowRef    string
}

type CorrelationResult struct {
	Key        string `json:"key"`
	Generation uint64 `json:"generation"`
	Changed    bool   `json:"changed"`
}

// Correlator binds the authenticated runner job-start identity to GARM's
// authoritative queue journal. The caller has already verified the one-job
// cache-claim token for RunnerName; no job-name or timing heuristic is used.
type Correlator struct {
	Path          string
	LockPath      string
	Now           func() time.Time
	Attempts      int
	RetryInterval time.Duration
}

func (c Correlator) Ready(ctx context.Context) error {
	if !filepath.IsAbs(c.Path) || !filepath.IsAbs(c.LockPath) || filepath.Dir(c.Path) != filepath.Dir(c.LockPath) || c.Path == c.LockPath {
		return errors.New("queue journal and lock must be distinct absolute siblings")
	}
	_, err := (Reader{Path: c.Path}).ReadActive(ctx)
	return err
}

func (c Correlator) BindRunning(ctx context.Context, correlation RunningCorrelation) (CorrelationResult, error) {
	if err := validateRunningCorrelation(correlation); err != nil {
		return CorrelationResult{}, err
	}
	attempts := c.Attempts
	if attempts <= 0 {
		attempts = 20
	}
	interval := c.RetryInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	var last error
	for attempt := 0; attempt < attempts; attempt++ {
		result, err := c.bindOnce(ctx, correlation)
		if err == nil || !errors.Is(err, ErrRunningCorrelationNotReady) {
			return result, err
		}
		last = err
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return CorrelationResult{}, fmt.Errorf("bind running correlation: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return CorrelationResult{}, last
}

func (c Correlator) bindOnce(ctx context.Context, correlation RunningCorrelation) (CorrelationResult, error) {
	if err := ctx.Err(); err != nil {
		return CorrelationResult{}, err
	}
	if !filepath.IsAbs(c.Path) || !filepath.IsAbs(c.LockPath) || filepath.Dir(c.Path) != filepath.Dir(c.LockPath) || c.Path == c.LockPath {
		return CorrelationResult{}, errors.New("queue journal and lock must be distinct absolute siblings")
	}
	parent := filepath.Dir(c.Path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return CorrelationResult{}, errors.New("queue state parent must be a real directory")
	}
	lockFD, err := syscall.Open(c.LockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return CorrelationResult{}, fmt.Errorf("open queue lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), c.LockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return CorrelationResult{}, fmt.Errorf("lock queue journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := requirePrivateOwnedRegular(lock, "queue lock"); err != nil {
		return CorrelationResult{}, err
	}
	journal, err := readJournal(c.Path)
	if err != nil {
		return CorrelationResult{}, err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	key := ""
	exactRunning := false
	for candidateKey, intent := range journal.Intents {
		if intent.State != StateRunning || intent.RunnerName != correlation.RunnerName || !intent.ExpiresAt.After(now) {
			continue
		}
		owner := strings.SplitN(correlation.Repository, "/", 2)[0]
		if intent.OwnerAccount() != owner || (intent.Repository != owner && intent.Repository != correlation.Repository) {
			continue
		}
		if key != "" {
			return CorrelationResult{}, errors.New("runner identity maps to multiple running queue intents")
		}
		key = candidateKey
		exactRunning = true
	}
	if key == "" {
		for candidateKey, intent := range journal.Intents {
			if intent.ScaleSetName != correlation.PoolName || !intent.ExpiresAt.After(now) {
				continue
			}
			switch intent.State {
			case StateAssigned, StateAcquiring, StateAcquired:
			default:
				continue
			}
			owner := strings.SplitN(correlation.Repository, "/", 2)[0]
			if intent.OwnerAccount() != owner || (intent.Repository != owner && intent.Repository != correlation.Repository) ||
				!jobDisplayMatchesHook(intent.JobDisplayName, correlation.JobDisplayName) {
				continue
			}
			if intent.WorkflowRunID != 0 && intent.WorkflowRunID != correlation.WorkflowRunID {
				continue
			}
			if key != "" {
				return CorrelationResult{}, errors.New("pool, repository and job identity map to multiple active queue intents")
			}
			key = candidateKey
		}
	}
	if key == "" {
		return CorrelationResult{}, ErrRunningCorrelationNotReady
	}
	intent := journal.Intents[key]
	if intent.WorkflowRunID != 0 && intent.WorkflowRunID != correlation.WorkflowRunID {
		return CorrelationResult{}, errors.New("running queue intent has conflicting workflow run identity")
	}
	changed := !exactRunning || intent.WorkflowRunID == 0 || intent.Repository != correlation.Repository ||
		(intent.JobDisplayName == "" && correlation.JobDisplayName != "") ||
		((intent.WorkflowRef == "" || intent.WorkflowRef == "unavailable-before-job-available") && correlation.WorkflowRef != "")
	if !changed {
		return CorrelationResult{Key: key, Generation: journal.Generation}, nil
	}
	intent.Repository = correlation.Repository
	intent.WorkflowRunID = correlation.WorkflowRunID
	if !exactRunning {
		intent.State = StateRunning
		intent.StateEnteredAt = now
		intent.RunnerName = correlation.RunnerName
	}
	if intent.JobDisplayName == "" {
		intent.JobDisplayName = correlation.JobDisplayName
	}
	if intent.WorkflowRef == "" || intent.WorkflowRef == "unavailable-before-job-available" {
		intent.WorkflowRef = correlation.WorkflowRef
	}
	intent.UpdatedAt = now
	journal.Intents[key] = intent
	journal.Generation++
	journal.UpdatedAt = now
	if err := journal.Validate(); err != nil {
		return CorrelationResult{}, err
	}
	if err := writeCorrelatedJournal(parent, c.Path, journal); err != nil {
		return CorrelationResult{}, err
	}
	return CorrelationResult{Key: key, Generation: journal.Generation, Changed: true}, nil
}

func jobDisplayMatchesHook(displayName, jobKey string) bool {
	return displayName == jobKey || strings.HasPrefix(displayName, jobKey+" / ")
}

func validateRunningCorrelation(correlation RunningCorrelation) error {
	if !validText(correlation.RunnerName) || !validText(correlation.PoolName) || !validRepository(correlation.Repository) || correlation.WorkflowRunID <= 0 ||
		!validText(correlation.JobDisplayName) || !validText(correlation.WorkflowRef) {
		return errors.New("running correlation identity is incomplete")
	}
	return nil
}

func writeCorrelatedJournal(parent, path string, journal Journal) error {
	temporary, err := os.CreateTemp(parent, ".queue-correlation-*")
	if err != nil {
		return fmt.Errorf("create correlation journal: %w", err)
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
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return fmt.Errorf("encode correlation journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync correlation journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close correlation journal: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish correlation journal: %w", err)
	}
	published = true
	directory, err := os.Open(parent)
	if err != nil {
		return fmt.Errorf("open queue directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync queue directory: %w", err)
	}
	return nil
}
