package queueintent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

const (
	LegacySchemaVersion   = 1
	PreviousSchemaVersion = 3
	SchemaVersion         = 4
	maxJournalBytes       = 4 * 1024 * 1024
)

type State string

const (
	StateQueued    State = "queued"
	StateAcquiring State = "acquiring"
	StateAcquired  State = "acquired"
	StateAssigned  State = "assigned"
	StateRunning   State = "running"
)

// Intent is the durable queue identity observed at JobAssigned before GARM
// changes desired capacity. RunnerRequestID is bound later at JobAvailable.
// GARM is the sole writer; provider and observer processes consume immutable
// fsync+rename generations without taking the writer lock.
type Intent struct {
	Key             string `json:"key"`
	ScaleSetID      int64  `json:"scale_set_id"`
	JobID           string `json:"job_id"`
	RunnerRequestID int64  `json:"runner_request_id"`
	WorkflowRunID   int64  `json:"workflow_run_id,omitempty"`
	JobDisplayName  string `json:"job_display_name,omitempty"`
	GitHubRunnerID  int64  `json:"github_runner_id,omitempty"`
	ScaleSetName    string `json:"scale_set_name"`
	RunnerName      string `json:"runner_name,omitempty"`
	// Owner is the forge account the scale set hangs from, written by GARM.
	// GitHub scopes a scale set name to one entity, so the name alone stopped
	// being an identity when the fleet gained a second tenant. Empty reads as
	// the fleet's own account, which is what every journal written before this
	// field existed meant.
	//
	// This is the journal's own vocabulary, not the tenant registry's: GARM
	// knows the account it serves and has no reason to know the registry ID
	// the fleet files it under. Translating one into the other is the
	// provider's job, at the one place that compares them.
	Owner          string    `json:"owner,omitempty"`
	Repository     string    `json:"repository"`
	WorkflowRef    string    `json:"workflow_ref"`
	EventName      string    `json:"event_name"`
	QueueTime      time.Time `json:"queue_time"`
	State          State     `json:"state"`
	Priority       int       `json:"priority"`
	StateEnteredAt time.Time `json:"state_entered_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

type RepositoryState struct {
	Repository string `json:"repository"`
	Weight     uint64 `json:"weight"`
	Pass       uint64 `json:"pass"`
}

type Journal struct {
	SchemaVersion int                        `json:"schema_version"`
	Generation    uint64                     `json:"generation"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	Intents       map[string]Intent          `json:"intents"`
	Repositories  map[string]RepositoryState `json:"repositories"`
}

type Snapshot struct {
	Generation uint64
	Stored     int
	Expired    int
	Active     []Intent
}

func (r Reader) HasActive(ctx context.Context) (bool, error) {
	snapshot, err := r.ReadActive(ctx)
	if err != nil {
		return false, err
	}
	return len(snapshot.Active) > 0, nil
}

type Reader struct {
	Path string
	Now  func() time.Time
}

func (r Reader) ReadActive(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fmt.Errorf("read queue intents: %w", err)
	}
	journal, err := readJournal(r.Path)
	if err != nil {
		return Snapshot{}, err
	}
	now := r.now()
	active := make([]Intent, 0, len(journal.Intents))
	expired := 0
	for _, intent := range journal.Intents {
		if intent.ExpiresAt.After(now) {
			active = append(active, intent)
		} else {
			expired++
		}
	}
	sort.Slice(active, func(left, right int) bool {
		if active[left].Priority != active[right].Priority {
			return active[left].Priority < active[right].Priority
		}
		if !active[left].QueueTime.Equal(active[right].QueueTime) {
			return active[left].QueueTime.Before(active[right].QueueTime)
		}
		return active[left].Key < active[right].Key
	})
	return Snapshot{Generation: journal.Generation, Stored: len(journal.Intents), Expired: expired, Active: active}, nil
}

// ActiveForScaleSet reports whether the named scale set of the named tenant
// holds an intent that has passed central admission. Both halves are required:
// two tenants may serve the same scale set class name, and answering from the
// name alone would let one tenant's admitted job authorize the other's create.
func (r Reader) ActiveForScaleSet(ctx context.Context, owner, scaleSet string) (bool, error) {
	if !validText(scaleSet) {
		return false, fmt.Errorf("scale set must be exact and non-empty")
	}
	if !validText(owner) {
		return false, fmt.Errorf("owner must be exact and non-empty")
	}
	snapshot, err := r.ReadActive(ctx)
	if err != nil {
		return false, err
	}
	for _, intent := range snapshot.Active {
		if intent.ScaleSetName == scaleSet && intent.OwnerAccount() == owner && intent.State != StateQueued {
			return true, nil
		}
	}
	return false, nil
}

// RepositoryForScaleSet returns the one concrete repository carried by an
// admitted intent. Organization entities give providers only the account URL,
// so the durable pre-AcquireJobs journal is the authoritative narrowing point.
func (r Reader) RepositoryForScaleSet(ctx context.Context, owner, scaleSet string) (string, error) {
	if !validText(owner) || !validText(scaleSet) {
		return "", fmt.Errorf("owner and scale set must be exact and non-empty")
	}
	snapshot, err := r.ReadActive(ctx)
	if err != nil {
		return "", err
	}
	repository := ""
	for _, intent := range snapshot.Active {
		if intent.OwnerAccount() != owner || intent.ScaleSetName != scaleSet || intent.State == StateQueued {
			continue
		}
		if !strings.HasPrefix(intent.Repository, owner+"/") {
			continue
		}
		if repository != "" && repository != intent.Repository {
			return "", fmt.Errorf("multiple repositories are active for account %q scale set %q", owner, scaleSet)
		}
		repository = intent.Repository
	}
	if repository == "" {
		return "", fmt.Errorf("no concrete repository is active for account %q scale set %q", owner, scaleSet)
	}
	return repository, nil
}

// defaultOwner is the account of the fleet's own tenant, resolved once so the
// registry stays the single place an account is written down.
var defaultOwner = func() string {
	fleet, err := tenant.ByID(tenant.DefaultID)
	if err != nil {
		panic("fleet tenant is missing from the registry: " + err.Error())
	}
	return fleet.Owner
}()

// OwnerAccount resolves the intent's forge account, defaulting to the fleet's
// own so a journal written before the field existed keeps its original meaning.
func (i Intent) OwnerAccount() string {
	if i.Owner == "" {
		return defaultOwner
	}
	return i.Owner
}

func readJournal(path string) (Journal, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return Journal{}, fmt.Errorf("queue-intent journal path must be absolute and bounded")
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return Journal{}, fmt.Errorf("resolve queue-intent journal parent: %w", err)
	}
	if filepath.Clean(resolvedParent) != filepath.Clean(parent) {
		return Journal{}, fmt.Errorf("queue-intent journal parent must not traverse symlinks")
	}
	fileFD, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		// GARM creates the journal at its first admission, so on a freshly
		// provisioned host it does not exist yet. Treating that as a failure
		// deadlocks the rollout: the observer must be healthy before a scale
		// set may be enabled, and no job can run to create the journal until
		// one is. A fleet that has admitted nothing is a well-defined empty
		// state, and it is the only error tolerated here — a journal that
		// exists but is group-readable, symlinked, oversized or malformed
		// still fails closed below.
		if errors.Is(err, fs.ErrNotExist) {
			return Journal{
				SchemaVersion: SchemaVersion,
				Intents:       map[string]Intent{},
				Repositories:  map[string]RepositoryState{},
			}, nil
		}
		return Journal{}, fmt.Errorf("open queue-intent journal: %w", err)
	}
	file := os.NewFile(uintptr(fileFD), path)
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Journal{}, fmt.Errorf("stat queue-intent journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return Journal{}, fmt.Errorf("queue-intent journal must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxJournalBytes+1))
	if err != nil {
		return Journal{}, fmt.Errorf("read queue-intent journal: %w", err)
	}
	if len(data) > maxJournalBytes {
		return Journal{}, fmt.Errorf("queue-intent journal exceeds %d bytes", maxJournalBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal Journal
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, fmt.Errorf("decode queue-intent journal: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Journal{}, fmt.Errorf("queue-intent journal contains multiple JSON values")
		}
		return Journal{}, fmt.Errorf("decode trailing queue-intent journal data: %w", err)
	}
	switch journal.SchemaVersion {
	case LegacySchemaVersion, 2:
		journal.SchemaVersion = SchemaVersion
		for key, intent := range journal.Intents {
			intent.StateEnteredAt = intent.UpdatedAt
			journal.Intents[key] = intent
		}
	case PreviousSchemaVersion:
		journal.SchemaVersion = SchemaVersion
	case SchemaVersion:
	default:
		return Journal{}, fmt.Errorf("queue-intent journal schema_version must be %d", SchemaVersion)
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (j Journal) Validate() error {
	if j.SchemaVersion != SchemaVersion {
		return fmt.Errorf("queue-intent journal schema_version must be %d", SchemaVersion)
	}
	if j.Intents == nil || j.Repositories == nil {
		return fmt.Errorf("queue-intent journal maps must not be null")
	}
	for key, intent := range j.Intents {
		if key == "" || intent.Key != key || intent.Key != intentKey(intent.ScaleSetID, intent.JobID) {
			return fmt.Errorf("queue intent key %q does not match embedded key %q", key, intent.Key)
		}
		if intent.ScaleSetID <= 0 || !validText(intent.JobID) || intent.RunnerRequestID < 0 || intent.WorkflowRunID < 0 || intent.GitHubRunnerID < 0 || !validText(intent.ScaleSetName) ||
			(intent.JobDisplayName != "" && !validText(intent.JobDisplayName)) ||
			(intent.RunnerName != "" && !validText(intent.RunnerName)) ||
			!validAccountOrRepository(intent.Repository) || !validText(intent.WorkflowRef) || !validText(intent.EventName) {
			return fmt.Errorf("queue intent %q has incomplete identity", key)
		}
		if intent.QueueTime.IsZero() || intent.StateEnteredAt.IsZero() || intent.UpdatedAt.IsZero() || intent.ExpiresAt.IsZero() ||
			intent.StateEnteredAt.After(intent.UpdatedAt) || intent.ExpiresAt.Before(intent.UpdatedAt) {
			return fmt.Errorf("queue intent %q has invalid timestamps", key)
		}
		if intent.Priority < 0 || intent.Priority > 3 {
			return fmt.Errorf("queue intent %q has invalid priority", key)
		}
		switch intent.State {
		case StateQueued, StateAcquiring, StateAcquired, StateAssigned, StateRunning:
		default:
			return fmt.Errorf("queue intent %q has invalid state %q", key, intent.State)
		}
	}
	for key, repository := range j.Repositories {
		if !validAccountOrRepository(key) || repository.Repository != key || repository.Weight == 0 || repository.Weight > 100 {
			return fmt.Errorf("repository scheduler state %q is invalid", key)
		}
	}
	return nil
}

func intentKey(scaleSetID int64, jobID string) string {
	return "github-scale-set-job:v2:" + strconv.FormatInt(scaleSetID, 10) + ":" + jobID
}

func validRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && validText(parts[0]) && validText(parts[1])
}

func validAccountOrRepository(value string) bool {
	if validRepository(value) {
		return true
	}
	return !strings.Contains(value, "/") && validText(value)
}

func validText(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func (r Reader) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
