//go:build linux

package queueintent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// AuthorizeRunning proves that this scale set currently holds active work for
// the exact repository and workflow run the runner presents, without demanding
// the one intent that job will eventually occupy.
//
// BindRunning has to identify a single intent, because it writes to it. To pick
// one out of a run's sibling jobs it compares the journal's job display name --
// what GitHub puts in the scale-set message -- against GITHUB_JOB, which is the
// job id from the workflow file. Those two agree for a bare job and for a
// reusable-workflow prefix, and disagree for every matrix job: `Analyze
// (python)` is not `analyze`. Measured over one hour on the live fleet, 19 warm
// claims were refused and 7 bound.
//
// Authorization does not need that. The credential a claim yields is scoped to
// a repository, so the question is whether this warm container is serving that
// repository's run -- and (scale set, owner, repository, workflow run id) is a
// strictly tighter answer than any job-name comparison. Siblings of the same
// run are interchangeable for this purpose: they carry the same repository and
// the same trust role. Nothing here mutates the journal; the exact intent is
// still bound by BindRunning and its asynchronous retry once GitHub reports the
// job started.
func (c Correlator) AuthorizeRunning(ctx context.Context, correlation RunningCorrelation) error {
	if err := validateRunningCorrelation(correlation); err != nil {
		return err
	}
	// A warm container can reach this endpoint before GARM has written any
	// intent for the job at all -- there is no create latency to hide the gap.
	// Measured after the display-name mismatch was fixed, every remaining
	// refusal was that race and nothing else. Retry on the same budget
	// BindRunning uses; the answer is a bounded read of one file.
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
		last = c.authorizeOnce(ctx, correlation)
		if last == nil || !errors.Is(last, ErrRunningCorrelationNotReady) {
			return last
		}
		if attempt+1 == attempts {
			break
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("authorize running correlation: %w", ctx.Err())
		case <-timer.C:
		}
	}
	return last
}

func (c Correlator) authorizeOnce(ctx context.Context, correlation RunningCorrelation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(c.Path) {
		return errors.New("queue journal must be an absolute path")
	}
	journal, err := readJournal(c.Path)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	owner := strings.SplitN(correlation.Repository, "/", 2)[0]
	for _, intent := range journal.Intents {
		if !intent.ExpiresAt.After(now) || intent.ScaleSetName != correlation.PoolName {
			continue
		}
		switch intent.State {
		case StateAssigned, StateAcquiring, StateAcquired, StateRunning:
		default:
			continue
		}
		if intent.OwnerAccount() != owner || (intent.Repository != owner && intent.Repository != correlation.Repository) {
			continue
		}
		// An intent admitted before JobAvailable carries no run id yet, and
		// BindRunning already treats that as compatible. Refusing it here would
		// deny a warm runner exactly the head-of-queue case warm capacity serves.
		if intent.WorkflowRunID != 0 && intent.WorkflowRunID != correlation.WorkflowRunID {
			continue
		}
		return nil
	}
	return ErrRunningCorrelationNotReady
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

// ReleaseUncoveredRunning removes running intents whose runner holds no
// execution lease, which is the only state in this journal that nothing else
// reclaims.
//
// A running intent leaves the journal when GARM observes a completed lifecycle
// message for its exact job UUID. When that message never arrives -- the runner
// vanished, the completion was missed across a manager restart -- the intent
// stays, and expiryForState gives running the execution horizon of a whole day.
// That is not free: validateQueueBudget excludes running intents from the
// reservation budget, but queueInFlight counts every non-queued intent and
// eligibleQueueCandidates compares that count against max_in_flight and the
// cross-repository share. Measured on the live fleet, 28 running intents stood
// against 12 worker instances and held 16 of 32 slots for jobs that had
// finished hours earlier.
//
// `covered` is the set of runner names that currently hold a provider execution
// lease. It is the same correlation the observer already publishes as
// gha_fleet_queue_uncovered_running, computed there as a count; this needs the
// identities, so the caller passes them.
//
// The grace exists because a lease appears slightly after the broker writes the
// running intent. Nothing is released inside it, so a job that is merely
// starting is never mistaken for one that has finished.
func (c Correlator) ReleaseUncoveredRunning(ctx context.Context, covered map[string]struct{}, grace time.Duration) ([]string, error) {
	if grace <= 0 {
		return nil, errors.New("release grace must be positive")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(c.Path) || !filepath.IsAbs(c.LockPath) || filepath.Dir(c.Path) != filepath.Dir(c.LockPath) || c.Path == c.LockPath {
		return nil, errors.New("queue journal and lock must be distinct absolute siblings")
	}
	parent := filepath.Dir(c.Path)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return nil, errors.New("queue state parent must be a real directory")
	}
	lockFD, err := syscall.Open(c.LockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open queue lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), c.LockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("lock queue journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if err := requirePrivateOwnedRegular(lock, "queue lock"); err != nil {
		return nil, err
	}
	journal, err := readJournal(c.Path)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	released := make([]string, 0)
	for key, intent := range journal.Intents {
		if intent.State != StateRunning || intent.RunnerName == "" {
			continue
		}
		if _, held := covered[intent.RunnerName]; held {
			continue
		}
		if now.Sub(intent.StateEnteredAt) < grace {
			continue
		}
		delete(journal.Intents, key)
		released = append(released, intent.RunnerName)
	}
	if len(released) == 0 {
		return nil, nil
	}
	sort.Strings(released)
	journal.Generation++
	journal.UpdatedAt = now
	if err := journal.Validate(); err != nil {
		return nil, err
	}
	if err := writeCorrelatedJournal(parent, c.Path, journal); err != nil {
		return nil, err
	}
	return released, nil
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
