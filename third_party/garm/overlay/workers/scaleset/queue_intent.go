package scaleset

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloudbase/garm/params"
)

const (
	queueAdmissionSchemaVersion      = 5
	queueIntentLegacySchemaVersion   = 1
	queueIntentPreviousSchemaVersion = 3
	queueIntentSchemaVersion         = 4
	queueIntentMaxBytes              = 4 * 1024 * 1024
	queueSchedulerStride             = uint64(1_000_000)
	queueAcquireRetryDelay           = 5 * time.Second

	queueConfigEnvironment = "GARM_NDDEV_QUEUE_ADMISSION_CONFIG"
	queueFileEnvironment   = "GARM_NDDEV_QUEUE_INTENT_FILE"
	queueLockEnvironment   = "GARM_NDDEV_QUEUE_INTENT_LOCK_FILE"
)

type queueIntentState string

const (
	queueStateQueued    queueIntentState = "queued"
	queueStateAcquiring queueIntentState = "acquiring"
	queueStateAcquired  queueIntentState = "acquired"
	queueStateAssigned  queueIntentState = "assigned"
	queueStateRunning   queueIntentState = "running"
)

type queueRepositoryPolicy struct {
	Weight      uint64 `json:"weight"`
	MaxInFlight int    `json:"max_in_flight"`
}

type queueResourceBudget struct {
	CPUUnits  int `json:"cpu_units"`
	MemoryMiB int `json:"memory_mib"`
}

type queueScaleSetResources struct {
	CPUUnits             int `json:"cpu_units"`
	MemoryMiB            int `json:"memory_mib"`
	ReservationCPUUnits  int `json:"reservation_cpu_units"`
	ReservationMemoryMiB int `json:"reservation_memory_mib"`
	Priority             int `json:"priority"`
}

func (r queueScaleSetResources) reservation() queueResourceBudget {
	if r.ReservationCPUUnits > 0 && r.ReservationMemoryMiB > 0 {
		return queueResourceBudget{CPUUnits: r.ReservationCPUUnits, MemoryMiB: r.ReservationMemoryMiB}
	}
	memory := map[int]int{2048: 512, 3072: 512, 4096: 2560, 6144: 2048}[r.MemoryMiB]
	if memory == 0 {
		return queueResourceBudget{CPUUnits: r.CPUUnits, MemoryMiB: r.MemoryMiB}
	}
	return queueResourceBudget{CPUUnits: 1, MemoryMiB: memory}
}

// queueMaxInFlightCeiling bounds how wide this queue may be configured. The
// queue is the fleet's only admission authority, so its width is the fleet's
// concurrency, and an unbounded number here would let one edited file ask for
// more workers than every host together can hold. Sixty-four is far above the
// four-host fleet this serves and far below anything that could exhaust the
// journal's single-writer update path.
const queueMaxInFlightCeiling = 64

type queueAdmissionConfig struct {
	SchemaVersion             int                               `json:"schema_version"`
	MaxInFlight               int                               `json:"max_in_flight"`
	MaxBackgroundInFlight     int                               `json:"max_background_in_flight"`
	DefaultRepositoryLimit    int                               `json:"default_repository_limit"`
	DefaultWeight             uint64                            `json:"default_weight"`
	QueuedTTLSeconds          int                               `json:"queued_ttl_seconds"`
	AcquiringTTLSeconds       int                               `json:"acquiring_ttl_seconds"`
	AcquiredTTLSeconds        int                               `json:"acquired_ttl_seconds"`
	ExecutionTTLSeconds       int                               `json:"execution_ttl_seconds"`
	PriorityAgingSeconds      int                               `json:"priority_aging_seconds"`
	MaxRepositorySharePercent int                               `json:"max_repository_share_percent"`
	Capacity                  queueResourceBudget               `json:"capacity"`
	ScaleSets                 map[string]queueScaleSetResources `json:"scale_sets"`
	Repositories              map[string]queueRepositoryPolicy  `json:"repositories"`
}

type queueIntent struct {
	Key             string `json:"key"`
	ScaleSetID      int64  `json:"scale_set_id"`
	JobID           string `json:"job_id"`
	RunnerRequestID int64  `json:"runner_request_id"`
	WorkflowRunID   int64  `json:"workflow_run_id,omitempty"`
	JobDisplayName  string `json:"job_display_name,omitempty"`
	GitHubRunnerID  int64  `json:"github_runner_id,omitempty"`
	ScaleSetName    string `json:"scale_set_name"`
	// RunnerName is bound only by JobStarted. It is the exact ephemeral GARM
	// instance name and lets the observer prove both directions of execution
	// coverage instead of comparing aggregate counts.
	RunnerName string `json:"runner_name,omitempty"`
	// Owner is the forge account the scale set hangs from. It is the half of
	// the identity that is known when a job is assigned, which Repository is
	// not for an organization entity. An intent written before this field
	// existed reads it as empty, which no live intent can be, so the two
	// never compare equal by accident.
	Owner string `json:"owner,omitempty"`
	// Repository is the account until JobAvailable names the repository. See
	// queueIntentRepositoryCompatible for the one transition that is allowed.
	Repository     string           `json:"repository"`
	WorkflowRef    string           `json:"workflow_ref"`
	EventName      string           `json:"event_name"`
	QueueTime      time.Time        `json:"queue_time"`
	State          queueIntentState `json:"state"`
	Priority       int              `json:"priority"`
	StateEnteredAt time.Time        `json:"state_entered_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
}

type queueRepositoryState struct {
	Repository string `json:"repository"`
	Weight     uint64 `json:"weight"`
	Pass       uint64 `json:"pass"`
}

type queueIntentJournal struct {
	SchemaVersion int                             `json:"schema_version"`
	Generation    uint64                          `json:"generation"`
	UpdatedAt     time.Time                       `json:"updated_at"`
	Intents       map[string]queueIntent          `json:"intents"`
	Repositories  map[string]queueRepositoryState `json:"repositories"`
}

type queueIntentCoordinator struct {
	configPath  string
	journalPath string
	lockPath    string
	now         func() time.Time
}

func newQueueIntentCoordinatorFromEnvironment() *queueIntentCoordinator {
	return &queueIntentCoordinator{
		configPath:  os.Getenv(queueConfigEnvironment),
		journalPath: os.Getenv(queueFileEnvironment),
		lockPath:    os.Getenv(queueLockEnvironment),
	}
}

// NDDevRemoveQueueIntent releases one exact journal intent after another GARM
// subsystem has authoritatively proved the GitHub job terminal or absent. Job
// IDs are globally stable UUIDs, but ambiguity still fails closed rather than
// deleting more than one scale-set generation.
func NDDevRemoveQueueIntent(ctx context.Context, jobID string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !validQueueText(jobID) {
		return false, fmt.Errorf("authoritative queue reconciliation requires an exact job ID")
	}
	coordinator := newQueueIntentCoordinatorFromEnvironment()
	config, err := coordinator.loadConfig()
	if err != nil {
		return false, err
	}
	removed := false
	err = coordinator.update(config, func(journal *queueIntentJournal, now time.Time) error {
		matched := ""
		for key, intent := range journal.Intents {
			if intent.JobID != jobID {
				continue
			}
			if matched != "" {
				return fmt.Errorf("authoritative queue reconciliation is ambiguous for job %q", jobID)
			}
			matched = key
		}
		if matched == "" {
			return nil
		}
		delete(journal.Intents, matched)
		admitQueuedToBudget(journal, config, now)
		removed = true
		return nil
	})
	return removed, err
}

// NDDevEnsureQueueIntent reconciles one GitHub-authoritatively queued DB job.
// It restores a missing intent after an acknowledged JobAssigned token expired
// and enriches a sparse organization-scoped intent with the repository that
// GitHub omitted from JobAssigned. The DB row and current scale-set identity
// are supplied by GARM's authoritative reconciler; this function never invents
// a job.
func NDDevEnsureQueueIntent(ctx context.Context, scaleSet params.ScaleSet, entity params.ForgeEntity, job params.Job) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return newQueueIntentCoordinatorFromEnvironment().EnsureAuthoritative(scaleSet, entity, job)
}

func (c *queueIntentCoordinator) EnsureAuthoritative(scaleSet params.ScaleSet, entity params.ForgeEntity, job params.Job) (bool, error) {
	config, err := c.loadConfig()
	if err != nil {
		return false, err
	}
	if scaleSet.ScaleSetID <= 0 || !validQueueText(scaleSet.Name) || !validQueueText(job.ScaleSetJobID) ||
		!validQueueText(entity.Owner) || !validQueueText(job.RepositoryOwner) || !validQueueText(job.RepositoryName) ||
		job.RepositoryOwner != entity.Owner {
		return false, fmt.Errorf("authoritative queued job has incomplete or cross-entity identity")
	}
	repository := job.RepositoryOwner + "/" + job.RepositoryName
	if !validRepository(repository) {
		return false, fmt.Errorf("authoritative queued job repository is invalid")
	}
	changed := false
	err = c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		key := queueIntentKey(int64(scaleSet.ScaleSetID), job.ScaleSetJobID)
		queueTime := job.CreatedAt.UTC()
		if queueTime.IsZero() || queueTime.After(now) {
			queueTime = now
		}
		if existing, exists := journal.Intents[key]; exists {
			if existing.ScaleSetName != scaleSet.Name || existing.Owner != entity.Owner ||
				!queueIntentRepositoryCompatible(existing, queueIntent{Owner: entity.Owner, Repository: repository}) {
				return fmt.Errorf("authoritative queued job changed immutable queue identity")
			}
			if queueIntentRepositoryBound(existing) {
				return nil
			}
			// Direct JIT normally goes from JobAssigned straight to JobStarted,
			// so an organization scale set may never receive JobAvailable. The
			// authoritative queued DB row is the first exact repository identity
			// available in that path. Bind it without changing state, lease,
			// runner identity or resource ownership.
			existing.Repository = repository
			existing.WorkflowRef = "authoritative-reconciliation"
			existing.EventName = job.Action
			existing.QueueTime = queueTime
			existing.Priority = baseQueuePriority(config, scaleSet.Name, params.ScaleSetJobMessage{EventName: job.Action})
			existing.UpdatedAt = now
			journal.Intents[key] = existing
			ensureRepositoryState(journal, config, repository)
			changed = true
			return nil
		}
		journal.Intents[key] = queueIntent{
			Key: key, ScaleSetID: int64(scaleSet.ScaleSetID), JobID: job.ScaleSetJobID,
			ScaleSetName: scaleSet.Name, Owner: entity.Owner, Repository: repository,
			WorkflowRef: "authoritative-rehydration", EventName: job.Action,
			QueueTime: queueTime, State: queueStateQueued,
			Priority:       baseQueuePriority(config, scaleSet.Name, params.ScaleSetJobMessage{EventName: job.Action}),
			StateEnteredAt: now, UpdatedAt: now, ExpiresAt: expiryForState(config, queueStateQueued, now),
		}
		ensureRepositoryState(journal, config, repository)
		admitQueuedToBudget(journal, config, now)
		changed = true
		return nil
	})
	return changed, err
}

func (c *queueIntentCoordinator) Validate() error {
	config, err := c.loadConfig()
	if err != nil {
		return err
	}
	if err := validateQueueStatePaths(c.journalPath, c.lockPath); err != nil {
		return err
	}
	return c.update(config, func(*queueIntentJournal, time.Time) error { return nil })
}

func (c *queueIntentCoordinator) ObserveAvailable(scaleSet params.ScaleSet, jobs []params.ScaleSetJobMessage) error {
	if !scaleSet.Enabled {
		return nil
	}
	config, err := c.loadConfig()
	if err != nil {
		return err
	}
	// How many jobs GitHub batches into one protocol message is GitHub's
	// decision. How many may run at once is ours, and SelectForAcquire is where
	// it is taken: it selects exactly one runner request per call regardless of
	// how many arrive. Refusing a batched message here conflated the two, and
	// the refusal is not durable -- the message stays unacknowledged, GitHub
	// redelivers it, and the listener spins. Observed at 28502 refusals in one
	// hour on gha-runner-1, placing nothing (#253, #275).
	// The scale set's own ceiling is GitHub's business and GARM's; how many
	// jobs may be in flight across the fleet is this queue's, and it is
	// max_in_flight. Asserting the two were both 1 conflated them, and made
	// widening the fleet fail here as a protocol error rather than as a
	// capacity decision. A scale set that can hold no runner at all is still
	// a configuration mistake worth refusing.
	if scaleSet.MaxRunners < 1 {
		return fmt.Errorf("queue admission requires a scale set that admits at least one runner, got max_runners=%d", scaleSet.MaxRunners)
	}
	return c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		for _, job := range jobs {
			intent, err := queueIntentFromJob(config, scaleSet, job, now, time.Duration(config.QueuedTTLSeconds)*time.Second)
			if err != nil {
				return err
			}
			existing, exists := journal.Intents[intent.Key]
			if exists {
				if !queueIntentCoreIdentityEqual(existing, intent) ||
					(existing.RunnerRequestID != 0 && existing.RunnerRequestID != intent.RunnerRequestID) {
					return fmt.Errorf("duplicate queue intent %q changed immutable identity", intent.Key)
				}
				if existing.RunnerRequestID == 0 {
					// The account-scoped intent learns its repository here.
					// queueIntentCoreIdentityEqual above has already refused
					// any binding outside the account that was admitted.
					existing.Repository = intent.Repository
					existing.RunnerRequestID = intent.RunnerRequestID
					existing.WorkflowRunID = intent.WorkflowRunID
					existing.JobDisplayName = intent.JobDisplayName
					existing.WorkflowRef = intent.WorkflowRef
					existing.EventName = intent.EventName
					existing.QueueTime = intent.QueueTime
					existing.Priority = intent.Priority
					journal.Intents[intent.Key] = existing
				} else if !queueIntentMetadataEqual(existing, intent) {
					return fmt.Errorf("duplicate queue intent %q changed available-job metadata", intent.Key)
				}
				ensureRepositoryState(journal, config, intent.Repository)
				// Redelivery is not progress and must not extend any state TTL.
				// In particular, a stale acquired message cannot block the global
				// admission slot forever by refreshing itself.
				continue
			}
			journal.Intents[intent.Key] = intent
			ensureRepositoryState(journal, config, intent.Repository)
		}
		for _, job := range jobs {
			if _, err := tryAdmitQueueIntent(journal, config, queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID), now); err != nil {
				return err
			}
		}
		return nil
	})
}

// HasQueuedAvailable reports whether the current GitHub message still owns an
// available job that has not been acquired. The listener must not acknowledge
// such a message: retaining it is the durable hand-off back to GitHub while a
// different Scale Set owns the global slot.
func (c *queueIntentCoordinator) HasQueuedAvailable(scaleSet params.ScaleSet, jobs []params.ScaleSetJobMessage) (bool, error) {
	config, err := c.loadConfig()
	if err != nil {
		return false, err
	}
	pending := false
	err = c.update(config, func(journal *queueIntentJournal, _ time.Time) error {
		for _, job := range jobs {
			key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
			intent, exists := journal.Intents[key]
			if !exists {
				return fmt.Errorf("available queue intent %q is missing", key)
			}
			if intent.State == queueStateQueued || intent.State == queueStateAssigned || intent.State == queueStateAcquiring {
				pending = true
			}
		}
		return nil
	})
	return pending, err
}

// AdmittedCapacityTarget is the exact current runner target from durable queue
// ownership. Desired runner count can lag cancellations, so it may only be an
// upper bound on this count.
func (c *queueIntentCoordinator) AdmittedCapacityTarget(scaleSet params.ScaleSet, entity params.ForgeEntity) (int, error) {
	config, err := c.loadConfig()
	if err != nil {
		return 0, err
	}
	target := 0
	err = c.update(config, func(journal *queueIntentJournal, _ time.Time) error {
		for _, intent := range journal.Intents {
			if intent.ScaleSetID != int64(scaleSet.ScaleSetID) || intent.ScaleSetName != scaleSet.Name || intent.Owner != entity.Owner {
				continue
			}
			switch intent.State {
			case queueStateAssigned, queueStateAcquiring, queueStateAcquired, queueStateRunning:
				target++
			}
		}
		return nil
	})
	return target, err
}

// SelectForAcquire reserves every globally eligible candidate in this message.
// The reservation is durable before the caller contacts GitHub's AcquireJobs
// API.
//
// Reserving several is safe without re-checking any budget here. An intent
// only reaches assigned through tryAdmitQueueIntent, which is the single place
// that charges the global and per-repository in-flight budgets and advances
// the stride scheduler; queueInFlight counts every state except queued, so a
// candidate this function can see has already been paid for. Selecting one at
// a time did not enforce a limit -- it just made the queue drain one job per
// round trip to GitHub no matter how much capacity the fleet had free.
func (c *queueIntentCoordinator) SelectForAcquire(scaleSet params.ScaleSet, jobs []params.ScaleSetJobMessage) ([]int64, error) {
	if !scaleSet.Enabled {
		return []int64{}, nil
	}
	config, err := c.loadConfig()
	if err != nil {
		return nil, err
	}
	for _, job := range jobs {
		if job.RunnerRequestID <= 0 || !validQueueText(job.JobID) {
			return nil, fmt.Errorf("available job has invalid runner request ID")
		}
	}
	var selected []int64
	err = c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		// An acquiring record may survive a crash on either side of the
		// AcquireJobs call. Retrying the same request ID is the only safe bridge
		// across that external transaction boundary and does not charge stride
		// twice.
		for _, job := range jobs {
			key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
			intent, exists := journal.Intents[key]
			if !exists || (intent.State != queueStateAcquiring && intent.State != queueStateAssigned) ||
				(intent.State == queueStateAcquiring && now.Sub(intent.UpdatedAt) < queueAcquireRetryDelay) {
				continue
			}
			intent.State = queueStateAcquiring
			intent.StateEnteredAt = now
			intent.UpdatedAt = now
			intent.ExpiresAt = expiryForState(config, queueStateAcquiring, now)
			journal.Intents[key] = intent
			selected = append(selected, job.RunnerRequestID)
		}
		return nil
	})
	if err != nil {
		// A failed update is not partially applied -- update writes the journal
		// once, at the end -- so anything appended above was never durable.
		return nil, err
	}
	return selected, nil
}

func (c *queueIntentCoordinator) CompleteAcquire(scaleSet params.ScaleSet, selected, acquired []int64) error {
	config, err := c.loadConfig()
	if err != nil {
		return err
	}
	selectedSet := make(map[int64]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}
	acquiredSet := make(map[int64]struct{}, len(acquired))
	for _, id := range acquired {
		if _, exists := selectedSet[id]; !exists {
			return fmt.Errorf("GitHub returned unselected runner request ID %d", id)
		}
		acquiredSet[id] = struct{}{}
	}
	return c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		for _, id := range selected {
			key, err := queueIntentKeyForRequest(journal, int64(scaleSet.ScaleSetID), id)
			if err != nil {
				return err
			}
			intent, exists := journal.Intents[key]
			if !exists || intent.State != queueStateAcquiring {
				return fmt.Errorf("queue intent %q is not durably acquiring", key)
			}
			if _, wasAcquired := acquiredSet[id]; wasAcquired {
				intent.State = queueStateAcquired
				intent.StateEnteredAt = now
			}
			// A successful API response that omitted this ID is externally
			// ambiguous after a crash: the earlier call may already have acquired
			// it. Keep the short acquiring lease and acknowledge the message; a
			// lifecycle event promotes it and TTL bounds the uncertainty.
			intent.UpdatedAt = now
			intent.ExpiresAt = expiryForState(config, intent.State, now)
			journal.Intents[key] = intent
		}
		return nil
	})
}

// FailAcquire is used only when the GitHub API call itself failed. No success
// response crossed the transaction boundary, so returning the reservation to
// its already-admitted assigned state and retaining the GitHub message is safe.
func (c *queueIntentCoordinator) FailAcquire(scaleSet params.ScaleSet, selected []int64) error {
	config, err := c.loadConfig()
	if err != nil {
		return err
	}
	return c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		for _, id := range selected {
			key, err := queueIntentKeyForRequest(journal, int64(scaleSet.ScaleSetID), id)
			if err != nil {
				return err
			}
			intent, exists := journal.Intents[key]
			if !exists || intent.State != queueStateAcquiring {
				return fmt.Errorf("queue intent %q is not durably acquiring", key)
			}
			intent.State = queueStateAssigned
			intent.StateEnteredAt = now
			intent.UpdatedAt = now
			intent.ExpiresAt = expiryForState(config, intent.State, now)
			journal.Intents[key] = intent
		}
		return nil
	})
}

// ObserveLifecycle records GitHub's JobAssigned event before GARM updates the
// desired runner count. JobAssigned precedes JobAvailable in the scale-set
// protocol and carries only jobId reliably. Repository identity comes from the
// canonical repository-scoped ForgeEntity; workflow metadata and the numeric
// runnerRequestId are bound later from JobAvailable. The returned boolean is
// JobAssigned owns one short-lived provisional capacity token. GitHub does not
// emit JobAvailable until a runner exists, so treating the sparse event as
// observation-only deadlocks an empty fleet: desired count rises, but the
// provider preflight sees zero admitted intent and refuses to create that first
// runner. JobAvailable binds the complete identity before AcquireJobs, while
// the short assigned TTL bounds a missing follow-up event.
func (c *queueIntentCoordinator) ObserveLifecycle(scaleSet params.ScaleSet, entity params.ForgeEntity, assigned, started, completed []params.ScaleSetJobMessage) (bool, error) {
	if !scaleSet.Enabled {
		return false, nil
	}
	config, err := c.loadConfig()
	if err != nil {
		return false, err
	}
	// Same conflation as ObserveAvailable, and the same spin: a backlog makes
	// GitHub batch assigned jobs, every batched message was refused whole, and
	// nothing was ever acknowledged. Recording several intents is safe -- each
	// still has to win the single acquisition slot.
	if scaleSet.MaxRunners < 1 {
		return false, fmt.Errorf("queue admission requires a scale set that admits at least one runner, got max_runners=%d", scaleSet.MaxRunners)
	}
	var orphanedStarts []string
	var uncorrelatedStarts []string
	var reservationTransfers [][2]string
	err = c.update(config, func(journal *queueIntentJournal, now time.Time) error {
		orphanedStarts = orphanedStarts[:0]
		uncorrelatedStarts = uncorrelatedStarts[:0]
		reservationTransfers = reservationTransfers[:0]
		completedKeys := make(map[string]struct{}, len(completed))
		startedKeys := make(map[string]struct{}, len(started))
		for _, job := range completed {
			if !validQueueText(job.JobID) {
				return fmt.Errorf("completed job has invalid job ID")
			}
			completedKeys[queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)] = struct{}{}
		}
		for _, job := range started {
			key := queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID)
			if !validQueueText(job.RunnerName) {
				// JobStarted is already true at GitHub. Never reject the lifecycle
				// batch and recreate a head-of-line redelivery loop merely because
				// its correlation field is missing; persist the running state and
				// surface the missing identity through observer telemetry.
				uncorrelatedStarts = append(uncorrelatedStarts, key)
			}
			startedKeys[key] = struct{}{}
			if _, completedInBatch := completedKeys[key]; completedInBatch {
				continue
			}
			intent, exists := journal.Intents[key]
			// A started job is GitHub reporting something already true: a
			// runner has it. Refusing the message does not stop it. It only
			// means nothing in the batch is ever acknowledged, so GitHub
			// redelivers the same message forever and the listener stops
			// making progress for every other job on this scale set -- the
			// same spin the batching conflation above already caused once,
			// reached by a different route.
			//
			// Observed 2026-08-15: one such job produced 29,472 redeliveries
			// in thirty minutes and `nddev-linux-standard` served nothing for
			// sixteen hours. The intent was missing because the scale set had
			// been recreated and its identifier moved, so every key built from
			// the current identifier missed entries written under the previous
			// one. That is a real condition to survive, not an invariant to
			// assert: a scale set may be recreated at any time, and this
			// journal is not the authority on what GitHub has already started.
			if !exists {
				fromJobID, transferred, transferErr := transferAssignedReservation(
					journal, config, scaleSet, entity, job, now,
				)
				if transferErr != nil {
					return transferErr
				}
				if transferred {
					reservationTransfers = append(reservationTransfers, [2]string{fromJobID, job.JobID})
					continue
				}
				// JobStarted plus its exact runner identity is authoritative
				// execution state even when the provisional intent expired or a
				// scale-set generation changed. Acknowledging it without tracking it
				// makes the provider lease look covered-but-idle forever and removes
				// real CPU/memory from central admission. Rehydrate the running intent
				// after the fact; the job already exists, so an over-budget result is
				// an observable fact, not a reason to poison lifecycle delivery.
				replacement, replacementErr := queueIntentFromLifecycle(
					config, scaleSet, entity, job, now, time.Duration(config.ExecutionTTLSeconds)*time.Second,
				)
				if replacementErr != nil {
					orphanedStarts = append(orphanedStarts, key)
					continue
				}
				replacement.State = queueStateRunning
				replacement.StateEnteredAt = now
				replacement.UpdatedAt = now
				replacement.ExpiresAt = expiryForState(config, queueStateRunning, now)
				journal.Intents[key] = replacement
				ensureRepositoryState(journal, config, replacement.Repository)
				continue
			}
			if intent.State != queueStateRunning {
				intent.StateEnteredAt = now
			}
			intent.State = queueStateRunning
			if validQueueText(job.RunnerName) {
				intent.RunnerName = job.RunnerName
			}
			if job.WorkflowRunID > 0 {
				intent.WorkflowRunID = job.WorkflowRunID
			}
			if validQueueText(job.JobDisplayName) {
				intent.JobDisplayName = job.JobDisplayName
			}
			if job.RunnerID > 0 {
				intent.GitHubRunnerID = job.RunnerID
			}
			intent.UpdatedAt = now
			intent.ExpiresAt = expiryForState(config, queueStateRunning, now)
			journal.Intents[key] = intent
		}
		for _, job := range assigned {
			intent, err := queueIntentFromLifecycle(config, scaleSet, entity, job, now, time.Duration(config.QueuedTTLSeconds)*time.Second)
			if err != nil {
				return err
			}
			if _, completedInBatch := completedKeys[intent.Key]; completedInBatch {
				continue
			}
			existing, exists := journal.Intents[intent.Key]
			// A started event without an existing admitted intent is already
			// acknowledged above as authoritative external state. Creating a new
			// assigned intent for the same job later in this batch makes the next
			// replay promote a phantom running intent with no runner request or VM.
			if _, startedInBatch := startedKeys[intent.Key]; startedInBatch && !exists {
				continue
			}
			if exists {
				if !queueIntentCoreIdentityEqual(existing, intent) {
					return fmt.Errorf("duplicate assigned intent %q changed immutable identity", intent.Key)
				}
				// Deliberately no write-back: the journal already holds `existing`,
				// which carries mutable state this freshly rebuilt `intent` does not,
				// so overwriting it would discard progress. This line used to read
				// `intent = existing`, which looked load-bearing while being dead —
				// `intent` is redeclared next iteration and nothing follows here.
			} else {
				journal.Intents[intent.Key] = intent
				ensureRepositoryState(journal, config, intent.Repository)
			}
		}
		for key := range completedKeys {
			delete(journal.Intents, key)
		}
		// JobAssigned is durable before its GitHub message is acknowledged. When
		// completion releases budget, promote the next queued intents inside the
		// same journal transaction so no later redelivery is required to wake the
		// fair scheduler.
		admitQueuedToBudget(journal, config, now)
		return nil
	})
	for _, transfer := range reservationTransfers {
		slog.Info(
			"transferred same-scale-set capacity reservation to started job",
			"from_job_id", transfer[0], "to_job_id", transfer[1],
			"scale_set", scaleSet.Name, "scale_set_id", scaleSet.ScaleSetID,
		)
	}
	for _, key := range orphanedStarts {
		slog.Warn(
			"started job has no admitted queue intent; acknowledging anyway",
			"key", key, "scale_set", scaleSet.Name, "scale_set_id", scaleSet.ScaleSetID,
		)
	}
	for _, key := range uncorrelatedStarts {
		slog.Warn(
			"started job has no valid runner identity; acknowledging with explicit telemetry gap",
			"key", key, "scale_set", scaleSet.Name, "scale_set_id", scaleSet.ScaleSetID,
		)
	}
	// A durable assigned/queued intent is the crash-safe ownership transfer.
	// Retaining JobAssigned at GitHub only causes head-of-line redelivery and
	// prevents later JobCompleted messages from reaching this listener.
	return false, err
}

func transferAssignedReservation(
	journal *queueIntentJournal,
	config queueAdmissionConfig,
	scaleSet params.ScaleSet,
	entity params.ForgeEntity,
	started params.ScaleSetJobMessage,
	now time.Time,
) (string, bool, error) {
	candidates := make([]queueIntent, 0)
	for _, candidate := range journal.Intents {
		if candidate.State != queueStateAssigned || candidate.ScaleSetID != int64(scaleSet.ScaleSetID) ||
			candidate.ScaleSetName != scaleSet.Name || candidate.Owner != entity.Owner || candidate.JobID == started.JobID {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return "", false, nil
	}
	sort.Slice(candidates, func(left, right int) bool {
		if !candidates[left].UpdatedAt.Equal(candidates[right].UpdatedAt) {
			return candidates[left].UpdatedAt.Before(candidates[right].UpdatedAt)
		}
		return candidates[left].Key < candidates[right].Key
	})
	replacement, err := queueIntentFromLifecycle(
		config, scaleSet, entity, started, now, time.Duration(config.ExecutionTTLSeconds)*time.Second,
	)
	if err != nil {
		return "", false, err
	}
	reservation := candidates[0]
	delete(journal.Intents, reservation.Key)
	replacement.QueueTime = reservation.QueueTime
	replacement.Priority = reservation.Priority
	replacement.State = queueStateRunning
	replacement.StateEnteredAt = now
	replacement.UpdatedAt = now
	replacement.ExpiresAt = expiryForState(config, queueStateRunning, now)
	journal.Intents[replacement.Key] = replacement
	ensureRepositoryState(journal, config, replacement.Repository)
	return reservation.JobID, true, nil
}

func admitQueuedToBudget(journal *queueIntentJournal, config queueAdmissionConfig, now time.Time) {
	for {
		globalInFlight, repositoryInFlight := queueInFlight(journal)
		if globalInFlight >= config.MaxInFlight {
			return
		}
		candidates := eligibleQueueCandidates(journal, config, repositoryInFlight, now)
		if len(candidates) == 0 {
			return
		}
		selected, exists := resourceFitCandidate(journal, config, candidates, now)
		if !exists {
			return
		}
		admitted, err := tryAdmitQueueIntent(journal, config, selected.Key, now)
		if err != nil || !admitted {
			return
		}
	}
}

func tryAdmitQueueIntent(journal *queueIntentJournal, config queueAdmissionConfig, key string, now time.Time) (bool, error) {
	intent, exists := journal.Intents[key]
	if !exists {
		return false, fmt.Errorf("queue intent %q is missing", key)
	}
	if intent.State != queueStateQueued {
		return true, nil
	}
	globalInFlight, repositoryInFlight := queueInFlight(journal)
	if globalInFlight >= config.MaxInFlight {
		return false, nil
	}
	candidates := eligibleQueueCandidates(journal, config, repositoryInFlight, now)
	selected, exists := resourceFitCandidate(journal, config, candidates, now)
	if !exists || selected.Key != key {
		return false, nil
	}
	intent.State = queueStateAssigned
	intent.StateEnteredAt = now
	intent.UpdatedAt = now
	intent.ExpiresAt = expiryForState(config, queueStateAssigned, now)
	journal.Intents[key] = intent
	repository := ensureRepositoryState(journal, config, intent.Repository)
	stride := queueSchedulerStride / repository.Weight
	if stride == 0 || ^uint64(0)-repository.Pass < stride {
		return false, fmt.Errorf("repository scheduler pass overflow for %q", intent.Repository)
	}
	repository.Pass += stride
	journal.Repositories[intent.Repository] = repository
	return true, nil
}

func (c *queueIntentCoordinator) loadConfig() (queueAdmissionConfig, error) {
	if !filepath.IsAbs(c.configPath) || filepath.Clean(c.configPath) == string(filepath.Separator) {
		return queueAdmissionConfig{}, fmt.Errorf("%s must be an absolute bounded path", queueConfigEnvironment)
	}
	file, err := openPrivateRegular(c.configPath, "queue admission config")
	if err != nil {
		return queueAdmissionConfig{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 64*1024+1))
	if err != nil {
		return queueAdmissionConfig{}, fmt.Errorf("read queue admission config: %w", err)
	}
	if len(data) > 64*1024 {
		return queueAdmissionConfig{}, fmt.Errorf("queue admission config exceeds 65536 bytes")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var config queueAdmissionConfig
	if err := decoder.Decode(&config); err != nil {
		return queueAdmissionConfig{}, fmt.Errorf("decode queue admission config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return queueAdmissionConfig{}, fmt.Errorf("queue admission config has trailing data")
	}
	if err := config.Validate(); err != nil {
		return queueAdmissionConfig{}, err
	}
	return config, nil
}

func (c *queueIntentCoordinator) update(config queueAdmissionConfig, mutate func(*queueIntentJournal, time.Time) error) error {
	if err := validateQueueStatePaths(c.journalPath, c.lockPath); err != nil {
		return err
	}
	lockFD, err := syscall.Open(c.lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open queue-intent lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), c.lockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock queue-intent journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck
	if info, err := lock.Stat(); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("queue-intent lock must be a private regular file")
	}
	journal, err := readQueueIntentJournal(c.journalPath)
	if errors.Is(err, os.ErrNotExist) {
		journal = newQueueIntentJournal()
	} else if err != nil {
		return err
	}
	before, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("snapshot queue-intent journal: %w", err)
	}
	now := c.nowUTC()
	migrateLegacyQueueIntentOwnership(&journal, config, now)
	cleanupExpiredQueueIntents(&journal, now)
	// A restart can occur after JobAssigned was durably acknowledged but before
	// the scale-up worker observed its provisional token. Promote from durable
	// queue state on every serialized journal transaction so recovery never
	// depends on GitHub redelivering an already acknowledged lifecycle message.
	admitQueuedToBudget(&journal, config, now)
	if err := validateQueueBudget(&journal, config); err != nil {
		return err
	}
	if err := mutate(&journal, now); err != nil {
		return err
	}
	if err := journal.Validate(); err != nil {
		return err
	}
	if err := validateQueueBudget(&journal, config); err != nil {
		return err
	}
	after, err := json.Marshal(journal)
	if err != nil {
		return fmt.Errorf("encode queue-intent journal: %w", err)
	}
	if bytes.Equal(before, after) && fileExists(c.journalPath) {
		return nil
	}
	journal.Generation++
	journal.UpdatedAt = now
	return writeQueueIntentJournal(c.journalPath, journal)
}

func migrateLegacyQueueIntentOwnership(journal *queueIntentJournal, config queueAdmissionConfig, now time.Time) {
	for key, intent := range journal.Intents {
		switch {
		case intent.State == queueStateAssigned && intent.RunnerRequestID == 0 &&
			intent.ExpiresAt.After(now.Add(time.Duration(config.AcquiredTTLSeconds)*time.Second)):
			// Before nddev.41, sparse JobAssigned consumed a slot and received the
			// execution horizon. Current provisional ownership has the bounded
			// acquiring horizon already and must not be downgraded on every read.
			intent.State = queueStateQueued
			intent.ExpiresAt = expiryForState(config, queueStateQueued, now)
		case intent.State == queueStateAssigned:
			deadline := expiryForState(config, queueStateAssigned, now)
			if intent.ExpiresAt.After(deadline) {
				intent.ExpiresAt = deadline
			}
		case intent.State == queueStateAcquired:
			deadline := expiryForState(config, queueStateAcquired, now)
			if intent.ExpiresAt.After(deadline) {
				intent.ExpiresAt = deadline
			}
		}
		journal.Intents[key] = intent
	}
}

func (c queueAdmissionConfig) Validate() error {
	if c.SchemaVersion != queueAdmissionSchemaVersion ||
		c.MaxInFlight < 1 || c.MaxInFlight > queueMaxInFlightCeiling ||
		c.MaxBackgroundInFlight < 1 || c.MaxBackgroundInFlight > c.MaxInFlight ||
		c.DefaultWeight == 0 || c.DefaultWeight > 100 {
		return fmt.Errorf("queue admission identity or global limits are invalid")
	}
	// A per-repository limit above the global one cannot be honoured and would
	// read as a promise the queue never keeps.
	if c.DefaultRepositoryLimit < 1 || c.DefaultRepositoryLimit > c.MaxInFlight {
		return fmt.Errorf("queue admission default repository limit %d is outside 1..%d", c.DefaultRepositoryLimit, c.MaxInFlight)
	}
	if c.QueuedTTLSeconds != 600 || c.AcquiringTTLSeconds != 120 || c.AcquiredTTLSeconds != 600 ||
		c.ExecutionTTLSeconds != 86400 || c.PriorityAgingSeconds != 300 ||
		c.MaxRepositorySharePercent != 75 {
		return fmt.Errorf("queue admission pilot TTL/aging policy is invalid")
	}
	if c.Capacity.CPUUnits < 1 || c.Capacity.CPUUnits > 1024 ||
		c.Capacity.MemoryMiB < 1024 || c.Capacity.MemoryMiB > 1<<24 {
		return fmt.Errorf("queue admission resource capacity is invalid")
	}
	if len(c.ScaleSets) == 0 {
		return fmt.Errorf("queue admission scale-set resources must not be empty")
	}
	for name, resources := range c.ScaleSets {
		reservation := resources.reservation()
		if !validQueueText(name) || resources.CPUUnits < 1 || resources.CPUUnits > c.Capacity.CPUUnits ||
			resources.MemoryMiB < 1 || resources.MemoryMiB > c.Capacity.MemoryMiB ||
			reservation.CPUUnits < 1 || reservation.CPUUnits > resources.CPUUnits ||
			reservation.MemoryMiB < 256 || reservation.MemoryMiB%256 != 0 ||
			reservation.MemoryMiB > resources.MemoryMiB ||
			resources.Priority < 0 || resources.Priority > 2 {
			return fmt.Errorf("queue admission scale-set resources %q are invalid", name)
		}
	}
	if c.Repositories == nil {
		return fmt.Errorf("queue admission repositories must not be null")
	}
	for repository, policy := range c.Repositories {
		if !validRepository(repository) || policy.Weight == 0 || policy.Weight > 100 ||
			policy.MaxInFlight < 1 || policy.MaxInFlight > c.MaxInFlight {
			return fmt.Errorf("queue admission repository policy %q is invalid", repository)
		}
	}
	return nil
}

func (j queueIntentJournal) Validate() error {
	if j.SchemaVersion != queueIntentSchemaVersion || j.Intents == nil || j.Repositories == nil {
		return fmt.Errorf("queue-intent journal identity or maps are invalid")
	}
	for key, intent := range j.Intents {
		if key == "" || intent.Key != key || intent.Key != queueIntentKey(intent.ScaleSetID, intent.JobID) ||
			intent.ScaleSetID <= 0 || !validQueueText(intent.JobID) || intent.RunnerRequestID < 0 || intent.WorkflowRunID < 0 || intent.GitHubRunnerID < 0 || !validQueueText(intent.ScaleSetName) ||
			(intent.JobDisplayName != "" && !validQueueText(intent.JobDisplayName)) ||
			(intent.RunnerName != "" && !validQueueText(intent.RunnerName)) ||
			!validQueueAccountOrRepository(intent.Repository) || !validQueueText(intent.WorkflowRef) || !validQueueText(intent.EventName) || intent.QueueTime.IsZero() ||
			intent.StateEnteredAt.IsZero() || intent.StateEnteredAt.After(intent.UpdatedAt) ||
			intent.UpdatedAt.IsZero() || intent.ExpiresAt.Before(intent.UpdatedAt) || intent.Priority < 0 || intent.Priority > 3 {
			return fmt.Errorf("queue intent %q is invalid", key)
		}
		// An intent still carrying its account must say which account, or the
		// binding rule would have nothing to check the repository against. A
		// bound intent written before Owner existed has none, and needs none.
		if !queueIntentRepositoryBound(intent) && intent.Repository != intent.Owner {
			return fmt.Errorf("queue intent %q is unbound without naming its account", key)
		}
		switch intent.State {
		case queueStateQueued, queueStateAcquiring, queueStateAcquired, queueStateAssigned, queueStateRunning:
		default:
			return fmt.Errorf("queue intent %q has invalid state %q", key, intent.State)
		}
	}
	for key, state := range j.Repositories {
		if !validQueueAccountOrRepository(key) || state.Repository != key || state.Weight == 0 || state.Weight > 100 {
			return fmt.Errorf("queue repository state %q is invalid", key)
		}
	}
	return nil
}

func queueIntentFromJob(config queueAdmissionConfig, scaleSet params.ScaleSet, job params.ScaleSetJobMessage, now time.Time, ttl time.Duration) (queueIntent, error) {
	repository := job.OwnerName + "/" + job.RepositoryName
	if scaleSet.ScaleSetID <= 0 || scaleSet.MaxRunners < 1 || !validQueueText(scaleSet.Name) || !validQueueText(job.JobID) || job.RunnerRequestID <= 0 || !validRepository(repository) ||
		job.QueueTime.IsZero() || job.QueueTime.After(now.Add(time.Minute)) || !validQueueText(job.JobWorkflowRef) || !validQueueText(job.EventName) {
		return queueIntent{}, fmt.Errorf("job %d has incomplete queue admission identity", job.RunnerRequestID)
	}
	return queueIntent{
		Key:             queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID),
		ScaleSetID:      int64(scaleSet.ScaleSetID),
		JobID:           job.JobID,
		RunnerRequestID: job.RunnerRequestID,
		WorkflowRunID:   job.WorkflowRunID,
		JobDisplayName:  job.JobDisplayName,
		GitHubRunnerID:  job.RunnerID,
		ScaleSetName:    scaleSet.Name,
		Owner:           job.OwnerName,
		Repository:      repository,
		WorkflowRef:     job.JobWorkflowRef,
		EventName:       job.EventName,
		QueueTime:       job.QueueTime.UTC(),
		State:           queueStateQueued,
		Priority:        baseQueuePriority(config, scaleSet.Name, job),
		StateEnteredAt:  now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(ttl),
	}, nil
}

func queueIntentFromLifecycle(config queueAdmissionConfig, scaleSet params.ScaleSet, entity params.ForgeEntity, job params.ScaleSetJobMessage, now time.Time, ttl time.Duration) (queueIntent, error) {
	// A live JobAssigned message carries the job ID and nothing else -- no
	// owner, no repository, no workflow ref, no queue time. The entity is
	// therefore the only identity available here. A repository entity is one,
	// so the intent is complete; an organization entity is not, so the intent
	// is admitted against the account and binds its repository when
	// JobAvailable arrives, exactly as RunnerRequestID already does.
	var repository string
	switch entity.EntityType {
	case params.ForgeEntityTypeRepository:
		repository = entity.Owner + "/" + entity.Name
		if !validRepository(repository) {
			return queueIntent{}, fmt.Errorf("job %q has incomplete queue admission identity", job.JobID)
		}
	case params.ForgeEntityTypeOrganization:
		repository = entity.Owner
		if !validQueueText(repository) || strings.Contains(repository, "/") {
			return queueIntent{}, fmt.Errorf("job %q has incomplete queue admission identity", job.JobID)
		}
	default:
		return queueIntent{}, fmt.Errorf("job %q has incomplete queue admission identity", job.JobID)
	}
	if scaleSet.ScaleSetID <= 0 || scaleSet.MaxRunners < 1 ||
		!validQueueText(scaleSet.Name) || !validQueueText(job.JobID) || !validQueueText(entity.Owner) {
		return queueIntent{}, fmt.Errorf("job %q has incomplete queue admission identity", job.JobID)
	}
	runnerName := ""
	if job.MessageType == params.MessageTypeJobStarted && validQueueText(job.RunnerName) {
		runnerName = job.RunnerName
	}
	return queueIntent{
		Key:             queueIntentKey(int64(scaleSet.ScaleSetID), job.JobID),
		ScaleSetID:      int64(scaleSet.ScaleSetID),
		JobID:           job.JobID,
		RunnerRequestID: 0,
		WorkflowRunID:   job.WorkflowRunID,
		JobDisplayName:  job.JobDisplayName,
		GitHubRunnerID:  job.RunnerID,
		ScaleSetName:    scaleSet.Name,
		RunnerName:      runnerName,
		Owner:           entity.Owner,
		Repository:      repository,
		WorkflowRef:     "unavailable-before-job-available",
		EventName:       "unavailable-before-job-available",
		QueueTime:       now,
		State:           queueStateQueued,
		Priority:        baseQueuePriority(config, scaleSet.Name, params.ScaleSetJobMessage{}),
		StateEnteredAt:  now,
		UpdatedAt:       now,
		ExpiresAt:       now.Add(ttl),
	}, nil
}

func baseQueuePriority(config queueAdmissionConfig, scaleSetName string, job params.ScaleSetJobMessage) int {
	resources, exists := config.ScaleSets[scaleSetName]
	if !exists {
		return 1
	}
	// High priority is an owner decision expressed by private scale-set policy.
	// The public engine never contains a tenant or repository identity.
	if resources.Priority == 0 {
		return 0
	}
	// Scheduled work is background even on an ordinary capability class. A
	// dedicated background scale set also remains background for manual runs,
	// whose sparse JobAssigned event carries no workflow metadata yet.
	if job.EventName == "schedule" || resources.Priority == 2 {
		return 2
	}
	return 1
}

func eligibleQueueCandidates(journal *queueIntentJournal, config queueAdmissionConfig, inFlight map[string]int, now time.Time) []queueIntent {
	candidates := make([]queueIntent, 0)
	backgroundInFlight := queueBackgroundInFlight(journal)
	for _, intent := range journal.Intents {
		limit := repositoryPolicy(config, intent.Repository).MaxInFlight
		if queueHasCompetingRepository(journal, intent.Repository) {
			limit = min(limit, percentageCeiling(config.MaxInFlight, config.MaxRepositorySharePercent))
		}
		if intent.State != queueStateQueued ||
			inFlight[intent.Repository] >= limit ||
			(intent.Priority == 2 && backgroundInFlight >= config.MaxBackgroundInFlight) {
			continue
		}
		candidates = append(candidates, intent)
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftPriority := effectiveQueuePriority(candidates[left], config, now)
		rightPriority := effectiveQueuePriority(candidates[right], config, now)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		leftRepository := ensureRepositoryState(journal, config, candidates[left].Repository)
		rightRepository := ensureRepositoryState(journal, config, candidates[right].Repository)
		if leftRepository.Pass != rightRepository.Pass {
			return leftRepository.Pass < rightRepository.Pass
		}
		if !candidates[left].QueueTime.Equal(candidates[right].QueueTime) {
			return candidates[left].QueueTime.Before(candidates[right].QueueTime)
		}
		return candidates[left].Key < candidates[right].Key
	})
	return candidates
}

func percentageCeiling(total, percent int) int {
	return (total*percent + 99) / 100
}

func queueHasCompetingRepository(journal *queueIntentJournal, repository string) bool {
	for _, intent := range journal.Intents {
		if intent.State == queueStateQueued && intent.Repository != repository {
			return true
		}
	}
	return false
}

// queueBackgroundInFlight counts the immutable admission class, not its aged
// scheduling priority. Aging decides which queued background job receives the
// next background slot; it must not let long-running maintenance escape the
// configured concurrency envelope and occupy the whole production fleet.
func queueBackgroundInFlight(journal *queueIntentJournal) int {
	total := 0
	for _, intent := range journal.Intents {
		if intent.State != queueStateQueued && intent.Priority == 2 {
			total++
		}
	}
	return total
}

func effectiveQueuePriority(intent queueIntent, config queueAdmissionConfig, now time.Time) int {
	if intent.Priority <= 1 || !now.After(intent.QueueTime) {
		return intent.Priority
	}
	aged := int(now.Sub(intent.QueueTime) / (time.Duration(config.PriorityAgingSeconds) * time.Second))
	return maxQueuePriority(1, intent.Priority-aged)
}

func queueInFlight(journal *queueIntentJournal) (int, map[string]int) {
	total := 0
	byRepository := make(map[string]int)
	for _, intent := range journal.Intents {
		if intent.State == queueStateQueued {
			continue
		}
		total++
		byRepository[intent.Repository]++
	}
	return total, byRepository
}

type queueResourceUsage struct {
	CPUUnits  int
	MemoryMiB int
}

func queueResourcesInFlight(journal *queueIntentJournal, config queueAdmissionConfig) (queueResourceUsage, map[string]queueResourceUsage, bool) {
	usage := queueResourceUsage{}
	byRepository := make(map[string]queueResourceUsage)
	for _, intent := range journal.Intents {
		if intent.State == queueStateQueued {
			continue
		}
		resources, exists := config.ScaleSets[intent.ScaleSetName]
		if !exists {
			return queueResourceUsage{}, nil, false
		}
		reservation := resources.reservation()
		usage.CPUUnits += reservation.CPUUnits
		usage.MemoryMiB += reservation.MemoryMiB
		repository := byRepository[intent.Repository]
		repository.CPUUnits += reservation.CPUUnits
		repository.MemoryMiB += reservation.MemoryMiB
		byRepository[intent.Repository] = repository
	}
	return usage, byRepository, true
}

func resourceFitCandidate(journal *queueIntentJournal, config queueAdmissionConfig, candidates []queueIntent, now time.Time) (queueIntent, bool) {
	usage, byRepository, valid := queueResourcesInFlight(journal, config)
	if !valid {
		return queueIntent{}, false
	}
	for index, candidate := range candidates {
		resources, exists := config.ScaleSets[candidate.ScaleSetName]
		if !exists {
			return queueIntent{}, false
		}
		reservation := resources.reservation()
		fitsGlobal := usage.CPUUnits+reservation.CPUUnits <= config.Capacity.CPUUnits &&
			usage.MemoryMiB+reservation.MemoryMiB <= config.Capacity.MemoryMiB
		repository := byRepository[candidate.Repository]
		fitsRepository := true
		if queueHasCompetingRepository(journal, candidate.Repository) {
			// Capacity is discrete. A percentage smaller than one job must still
			// admit that job; otherwise a one-slot fixture or a future very large
			// class would deadlock every repository under contention.
			cpuLimit := max(reservation.CPUUnits, percentageCeiling(config.Capacity.CPUUnits, config.MaxRepositorySharePercent))
			memoryLimit := max(reservation.MemoryMiB, percentageCeiling(config.Capacity.MemoryMiB, config.MaxRepositorySharePercent))
			fitsRepository = repository.CPUUnits+reservation.CPUUnits <= cpuLimit &&
				repository.MemoryMiB+reservation.MemoryMiB <= memoryLimit
		}
		if fitsGlobal && fitsRepository {
			return candidate, true
		}
		// Priority zero is a bounded reservation. Once an old/release candidate
		// reaches it, backfilling smaller work stops until enough running
		// resources complete. Before that point, smaller jobs may consume holes
		// that the first candidate cannot use, preserving useful throughput.
		if !fitsGlobal && index == 0 && effectiveQueuePriority(candidate, config, now) == 0 {
			return queueIntent{}, false
		}
	}
	return queueIntent{}, false
}

func validateQueueBudget(journal *queueIntentJournal, config queueAdmissionConfig) error {
	// Budgets constrain reservations we still control. GitHub JobStarted is
	// authoritative state that may arrive after a provisional intent expired or
	// after a configuration reduction. Refusing to persist that fact does not
	// stop the job; it only blinds admission and lease correlation. Validate the
	// controllable pre-execution reservations here, while resourceFitCandidate
	// continues to count both reservations and running truth before admitting
	// any additional work.
	reserved := 0
	reservedByRepository := map[string]int{}
	reservedResources := queueResourceUsage{}
	for _, intent := range journal.Intents {
		if intent.State == queueStateQueued || intent.State == queueStateRunning {
			continue
		}
		reserved++
		reservedByRepository[intent.Repository]++
		resources, exists := config.ScaleSets[intent.ScaleSetName]
		if !exists {
			return fmt.Errorf("queue journal contains a reservation without a resource contract")
		}
		reservation := resources.reservation()
		reservedResources.CPUUnits += reservation.CPUUnits
		reservedResources.MemoryMiB += reservation.MemoryMiB
	}
	if reserved > config.MaxInFlight {
		return fmt.Errorf("queue journal exceeds global reservation budget: %d > %d", reserved, config.MaxInFlight)
	}
	for repository, count := range reservedByRepository {
		limit := repositoryPolicy(config, repository).MaxInFlight
		if count > limit {
			return fmt.Errorf("queue journal exceeds repository %q reservation budget: %d > %d", repository, count, limit)
		}
	}
	if reservedResources.CPUUnits > config.Capacity.CPUUnits || reservedResources.MemoryMiB > config.Capacity.MemoryMiB {
		return fmt.Errorf(
			"queue journal exceeds reservation resource budget: cpu=%d/%d memory_mib=%d/%d",
			reservedResources.CPUUnits, config.Capacity.CPUUnits,
			reservedResources.MemoryMiB, config.Capacity.MemoryMiB,
		)
	}
	return nil
}

func ensureRepositoryState(journal *queueIntentJournal, config queueAdmissionConfig, repository string) queueRepositoryState {
	policy := repositoryPolicy(config, repository)
	state, exists := journal.Repositories[repository]
	if !exists {
		state = queueRepositoryState{Repository: repository, Weight: policy.Weight}
	} else {
		state.Weight = policy.Weight
	}
	journal.Repositories[repository] = state
	return state
}

func repositoryPolicy(config queueAdmissionConfig, repository string) queueRepositoryPolicy {
	if policy, exists := config.Repositories[repository]; exists {
		return policy
	}
	return queueRepositoryPolicy{Weight: config.DefaultWeight, MaxInFlight: config.DefaultRepositoryLimit}
}

func cleanupExpiredQueueIntents(journal *queueIntentJournal, now time.Time) {
	for key, intent := range journal.Intents {
		if !intent.ExpiresAt.After(now) {
			delete(journal.Intents, key)
		}
	}
}

func expiryForState(config queueAdmissionConfig, state queueIntentState, now time.Time) time.Time {
	seconds := config.QueuedTTLSeconds
	switch state {
	case queueStateAcquiring:
		seconds = config.AcquiringTTLSeconds
	case queueStateAcquired:
		// AcquireJobs succeeded, but JobStarted has not proved execution. Bound
		// provider/JIT/registration stalls to their explicit phase horizon.
		seconds = config.AcquiredTTLSeconds
	case queueStateAssigned:
		// JobAssigned is the provisional token that bootstraps an empty scale
		// set. It can own a real cold provider create before JobAvailable exists;
		// measured release/container registration crossed 150 seconds, so the
		// 120-second API-call horizon expired valid ownership and hid running
		// work. Use the bounded pre-start horizon, never the execution horizon.
		seconds = config.AcquiredTTLSeconds
	case queueStateRunning:
		// Running is the one state that legitimately lasts as long as a job.
		seconds = config.ExecutionTTLSeconds
	}
	return now.Add(time.Duration(seconds) * time.Second)
}

func readQueueIntentJournal(path string) (queueIntentJournal, error) {
	file, err := openPrivateRegular(path, "queue-intent journal")
	if err != nil {
		return queueIntentJournal{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, queueIntentMaxBytes+1))
	if err != nil {
		return queueIntentJournal{}, fmt.Errorf("read queue-intent journal: %w", err)
	}
	if len(data) > queueIntentMaxBytes {
		return queueIntentJournal{}, fmt.Errorf("queue-intent journal exceeds %d bytes", queueIntentMaxBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal queueIntentJournal
	if err := decoder.Decode(&journal); err != nil {
		return queueIntentJournal{}, fmt.Errorf("decode queue-intent journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return queueIntentJournal{}, fmt.Errorf("queue-intent journal has trailing data")
	}
	switch journal.SchemaVersion {
	case queueIntentLegacySchemaVersion, 2:
		// v2 adds only the JobStarted runner identity. Old active intents have no
		// value to synthesize; they remain explicitly uncorrelated until their
		// lifecycle completes, and the next writer transaction upgrades the file.
		journal.SchemaVersion = queueIntentSchemaVersion
		for key, intent := range journal.Intents {
			intent.StateEnteredAt = intent.UpdatedAt
			journal.Intents[key] = intent
		}
	case queueIntentPreviousSchemaVersion:
		// v4 adds correlation fields only. Existing intents remain explicitly
		// incomplete until a later JobAvailable or JobStarted binds them.
		journal.SchemaVersion = queueIntentSchemaVersion
	case queueIntentSchemaVersion:
	default:
		return queueIntentJournal{}, fmt.Errorf("queue-intent journal schema_version must be %d", queueIntentSchemaVersion)
	}
	if err := journal.Validate(); err != nil {
		return queueIntentJournal{}, err
	}
	return journal, nil
}

func writeQueueIntentJournal(path string, journal queueIntentJournal) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".queue-intents-*")
	if err != nil {
		return fmt.Errorf("create queue-intent temporary file: %w", err)
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
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(journal); err != nil {
		return fmt.Errorf("encode queue-intent journal: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync queue-intent journal: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close queue-intent journal: %w", err)
	}
	closed = true
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace queue-intent journal: %w", err)
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open queue-intent directory: %w", err)
	}
	defer directoryHandle.Close()
	if err := directoryHandle.Sync(); err != nil {
		return fmt.Errorf("sync queue-intent directory: %w", err)
	}
	return nil
}

func openPrivateRegular(path, description string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return nil, fmt.Errorf("%s path must be absolute and bounded", description)
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolvedParent) != filepath.Clean(parent) {
		return nil, fmt.Errorf("%s parent must be a real directory", description)
	}
	fileFD, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", description, err)
	}
	file := os.NewFile(uintptr(fileFD), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o027 != 0 {
		file.Close()
		return nil, fmt.Errorf("%s must be a private regular file", description)
	}
	return file, nil
}

func validateQueueStatePaths(journalPath, lockPath string) error {
	if !filepath.IsAbs(journalPath) || !filepath.IsAbs(lockPath) ||
		filepath.Clean(journalPath) == string(filepath.Separator) || filepath.Clean(lockPath) == string(filepath.Separator) ||
		filepath.Clean(journalPath) == filepath.Clean(lockPath) || filepath.Dir(journalPath) != filepath.Dir(lockPath) {
		return fmt.Errorf("queue-intent journal and lock paths must be distinct bounded siblings")
	}
	parent := filepath.Dir(journalPath)
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(parent) {
		return fmt.Errorf("queue-intent state parent must be a real directory")
	}
	return nil
}

func newQueueIntentJournal() queueIntentJournal {
	return queueIntentJournal{
		SchemaVersion: queueIntentSchemaVersion,
		Intents:       make(map[string]queueIntent),
		Repositories:  make(map[string]queueRepositoryState),
	}
}

func queueIntentKey(scaleSetID int64, jobID string) string {
	return "github-scale-set-job:v2:" + strconv.FormatInt(scaleSetID, 10) + ":" + jobID
}

func queueIntentKeyForRequest(journal *queueIntentJournal, scaleSetID, runnerRequestID int64) (string, error) {
	var found string
	for key, intent := range journal.Intents {
		if intent.ScaleSetID != scaleSetID || intent.RunnerRequestID != runnerRequestID {
			continue
		}
		if found != "" {
			return "", fmt.Errorf("runner request ID %d is not unique in scale set %d", runnerRequestID, scaleSetID)
		}
		found = key
	}
	if found == "" {
		return "", fmt.Errorf("runner request ID %d is missing in scale set %d", runnerRequestID, scaleSetID)
	}
	return found, nil
}

// validQueueAccountOrRepository accepts the two shapes an accounting key may
// take: a repository, and the account an organization intent is admitted
// against before JobAvailable names one.
func validQueueAccountOrRepository(value string) bool {
	if validRepository(value) {
		return true
	}
	return validQueueText(value) && !strings.Contains(value, "/")
}

func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && validQueueText(parts[0]) && validQueueText(parts[1])
}

func validQueueText(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

// queueIntentCoreIdentityEqual takes the stored intent first and the observed
// one second, because the repository rule is directional.
func queueIntentCoreIdentityEqual(existing, incoming queueIntent) bool {
	return existing.Key == incoming.Key && existing.ScaleSetID == incoming.ScaleSetID &&
		existing.JobID == incoming.JobID && existing.ScaleSetName == incoming.ScaleSetName &&
		existing.Owner == incoming.Owner &&
		queueIntentRepositoryCompatible(existing, incoming)
}

// queueIntentRepositoryCompatible allows exactly one change: an intent still
// carrying its account learns the repository the account owns. Everything else
// is an immutable-identity violation, including a bound repository changing,
// and including a binding to a repository some other account owns -- which is
// what keeps one tenant's admitted slot from being spent on another's job.
func queueIntentRepositoryCompatible(existing, incoming queueIntent) bool {
	if existing.Repository == incoming.Repository {
		return true
	}
	if queueIntentRepositoryBound(existing) || !queueIntentRepositoryBound(incoming) {
		return false
	}
	return existing.Repository == existing.Owner &&
		strings.HasPrefix(incoming.Repository, existing.Owner+"/")
}

func queueIntentRepositoryBound(intent queueIntent) bool {
	return strings.Contains(intent.Repository, "/")
}

func queueIntentMetadataEqual(left, right queueIntent) bool {
	return left.WorkflowRunID == right.WorkflowRunID && left.JobDisplayName == right.JobDisplayName &&
		left.GitHubRunnerID == right.GitHubRunnerID && left.WorkflowRef == right.WorkflowRef && left.EventName == right.EventName &&
		left.QueueTime.Equal(right.QueueTime) && left.Priority == right.Priority
}

func queueStateRank(state queueIntentState) int {
	switch state {
	case queueStateQueued:
		return 0
	case queueStateAssigned:
		return 1
	case queueStateAcquiring:
		return 2
	case queueStateAcquired:
		return 3
	case queueStateRunning:
		return 4
	default:
		return -1
	}
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func maxQueuePriority(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (c *queueIntentCoordinator) nowUTC() time.Time {
	if c.now != nil {
		return c.now().UTC()
	}
	return time.Now().UTC()
}
