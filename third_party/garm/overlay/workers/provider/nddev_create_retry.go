package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/cloudbase/garm/params"
)

const (
	nddevRetrySchemaVersion         = 2
	nddevRetryPreviousSchemaVersion = 1
	nddevRetryMaximumBytes          = 1024 * 1024
	nddevRetryMaximum               = 3
	nddevRetryAttemptLease          = 2 * time.Minute
	nddevRetryExecutionTTL          = 24 * time.Hour
	nddevRetryStaleTTL              = time.Hour
	nddevRetryBase                  = 5 * time.Second
	nddevRetryCap                   = 5 * time.Minute
	nddevCapacityRetryCap           = 5 * time.Minute
	nddevRetryFileEnv               = "GARM_NDDEV_CREATE_RETRY_FILE"
	nddevRetryLockEnv               = "GARM_NDDEV_CREATE_RETRY_LOCK_FILE"
	nddevQueueIntentFileEnv         = "GARM_NDDEV_QUEUE_INTENT_FILE"
	nddevCapacityDomainKey          = "capacity-domain:measured-fleet"
)

var nddevRetryNow = func() time.Time { return time.Now().UTC() }

type nddevRetryRecord struct {
	JobID          string    `json:"job_id"`
	Attempts       int       `json:"attempts"`
	LastErrorClass string    `json:"last_error_class,omitempty"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextAllowedAt  time.Time `json:"next_allowed_at"`
	TerminalUntil  time.Time `json:"terminal_until,omitempty"`
	ProbeOwner     string    `json:"probe_owner,omitempty"`
	WakeReason     string    `json:"wake_reason,omitempty"`
	ScaleSetName   string    `json:"scale_set_name,omitempty"`
}

type nddevRetryJournal struct {
	SchemaVersion int                              `json:"schema_version"`
	Generation    uint64                           `json:"generation"`
	UpdatedAt     time.Time                        `json:"updated_at"`
	Records       map[string]nddevRetryRecord      `json:"records"`
	Reservations  map[string]nddevRetryReservation `json:"reservations"`
}

type nddevRetryReservation struct {
	RetryKey     string    `json:"retry_key"`
	ScaleSetName string    `json:"scale_set_name"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type nddevQueueRetryIntent struct {
	Key          string    `json:"key"`
	JobID        string    `json:"job_id"`
	ScaleSetID   int64     `json:"scale_set_id"`
	ScaleSetName string    `json:"scale_set_name"`
	Owner        string    `json:"owner"`
	State        string    `json:"state"`
	QueueTime    time.Time `json:"queue_time"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func nddevProviderRetryKey(instance params.Instance, scaleSet params.ScaleSet, entity params.ForgeEntity) string {
	domain := nddevScaleSetRetryKey(scaleSet, entity)
	if domain == "" {
		return ""
	}
	if instance.Job != nil && strings.TrimSpace(instance.Job.ScaleSetJobID) != "" {
		return domain + ":job:" + strings.TrimSpace(instance.Job.ScaleSetJobID)
	}
	if name := strings.TrimSpace(instance.Name); name != "" {
		return domain + ":instance:" + name
	}
	return domain
}

func nddevReserveProviderRetryKey(ctx context.Context, instance params.Instance, scaleSet params.ScaleSet, entity params.ForgeEntity) (string, error) {
	if key := nddevProviderRetryKey(instance, scaleSet, entity); instance.Job != nil || !strings.Contains(key, ":instance:") {
		return key, nil
	}
	instanceName := strings.TrimSpace(instance.Name)
	if instanceName == "" {
		return "", fmt.Errorf("pre-job provider retry identity requires an instance name")
	}
	domain := nddevScaleSetRetryKey(scaleSet, entity)
	if domain == "" {
		return "", nil
	}
	selected := ""
	err := nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		if reservation, exists := journal.Reservations[instanceName]; exists {
			reservation.UpdatedAt = now
			journal.Reservations[instanceName] = reservation
			selected = reservation.RetryKey
			return nil
		}
		intents, err := nddevActiveQueueIntents(now)
		if err != nil {
			return err
		}
		claimed := make(map[string]bool, len(journal.Reservations))
		for _, reservation := range journal.Reservations {
			claimed[reservation.RetryKey] = true
		}
		candidates := make([]nddevQueueRetryIntent, 0)
		for _, intent := range intents {
			if intent.ScaleSetID != int64(scaleSet.ScaleSetID) || intent.ScaleSetName != scaleSet.Name ||
				(intent.Owner != "" && intent.Owner != entity.Owner) {
				continue
			}
			retryKey := domain + ":job:" + strings.TrimSpace(intent.JobID)
			if intent.JobID == "" || claimed[retryKey] {
				continue
			}
			candidates = append(candidates, intent)
		}
		sort.Slice(candidates, func(left, right int) bool {
			if !candidates[left].QueueTime.Equal(candidates[right].QueueTime) {
				return candidates[left].QueueTime.Before(candidates[right].QueueTime)
			}
			return candidates[left].Key < candidates[right].Key
		})
		if len(candidates) == 0 {
			return fmt.Errorf("no unclaimed admitted queue intent can own pre-job provider create")
		}
		selected = domain + ":job:" + candidates[0].JobID
		journal.Reservations[instanceName] = nddevRetryReservation{
			RetryKey: selected, ScaleSetName: scaleSet.Name, UpdatedAt: now,
		}
		return nil
	})
	return selected, err
}

func nddevReleaseProviderRetryReservation(ctx context.Context, instanceName string) error {
	instanceName = strings.TrimSpace(instanceName)
	if instanceName == "" {
		return nil
	}
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, _ time.Time) error {
		delete(journal.Reservations, instanceName)
		return nil
	})
}

func nddevScaleSetRetryKey(scaleSet params.ScaleSet, entity params.ForgeEntity) string {
	if entity.ID == "" || scaleSet.ScaleSetID == 0 {
		return ""
	}
	return fmt.Sprintf("scale-set:%s:%d", entity.ID, scaleSet.ScaleSetID)
}

func nddevRetryDomainKey(key string) string {
	for _, marker := range []string{":instance:", ":job:"} {
		if index := strings.Index(key, marker); index > 0 {
			return key[:index]
		}
	}
	return key
}

// NDDevScaleSetCreateAllowed performs a read-only preflight before GARM creates
// a JIT registration or database instance. The provider path still reserves the
// authoritative attempt lease immediately before its external create call.
func NDDevScaleSetCreateAllowed(ctx context.Context, scaleSet params.ScaleSet, entity params.ForgeEntity) error {
	key := nddevScaleSetRetryKey(scaleSet, entity)
	if key == "" {
		return nil
	}
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		if err := nddevSharedCapacityCreateAllowed(journal, now, key, scaleSet.Name, false); err != nil {
			return err
		}
		record, exists := journal.Records[key]
		if !exists {
			return nil
		}
		if record.TerminalUntil.After(now) {
			return fmt.Errorf("provider create circuit is open until %s after %d attempts", record.TerminalUntil.Format(time.RFC3339), record.Attempts)
		}
		if record.NextAllowedAt.After(now) {
			return fmt.Errorf("provider create retry is deferred until %s", record.NextAllowedAt.Format(time.RFC3339))
		}
		return nil
	})
}

func nddevBeforeProviderCreate(ctx context.Context, key string, scaleSetNames ...string) error {
	if key == "" {
		return nil
	}
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		domainKey := nddevRetryDomainKey(key)
		scaleSetName := ""
		if len(scaleSetNames) > 0 {
			scaleSetName = strings.TrimSpace(scaleSetNames[0])
		}
		if err := nddevSharedCapacityCreateAllowed(journal, now, key, scaleSetName, true); err != nil {
			return err
		}
		if domain := journal.Records[domainKey]; domainKey != key {
			if domain.TerminalUntil.After(now) {
				return fmt.Errorf("provider create circuit is open until %s after %d attempts", domain.TerminalUntil.Format(time.RFC3339), domain.Attempts)
			}
			if domain.NextAllowedAt.After(now) {
				return fmt.Errorf("provider create retry is deferred until %s", domain.NextAllowedAt.Format(time.RFC3339))
			}
			// The first attempt after a capacity backoff owns the retry domain
			// until its provider call succeeds, fails, or the bounded attempt lease
			// expires. Without this reservation every pending worker in the same
			// scale set observes the expired backoff at once and all of them reach
			// the provider, reproducing a create herd while capacity is unchanged.
			// Initial unconstrained creates remain parallel; serialization begins
			// only after the provider has proved this domain capacity-bound.
			if domain.LastErrorClass == "capacity" {
				domain.UpdatedAt = now
				domain.NextAllowedAt = now.Add(nddevRetryAttemptLease)
				journal.Records[domainKey] = domain
			}
		}
		record := journal.Records[key]
		if record.JobID != "" && record.JobID != key {
			return fmt.Errorf("provider retry key changed identity")
		}
		if record.TerminalUntil.After(now) {
			return fmt.Errorf("provider create circuit is open until %s after %d attempts", record.TerminalUntil.Format(time.RFC3339), record.Attempts)
		}
		if record.NextAllowedAt.After(now) {
			return fmt.Errorf("provider create retry is deferred until %s", record.NextAllowedAt.Format(time.RFC3339))
		}
		capacityBackpressure := record.LastErrorClass == "capacity" && !strings.Contains(key, ":job:")
		if record.Attempts >= nddevRetryMaximum && !capacityBackpressure && record.LastErrorClass != "intent" {
			record.TerminalUntil = now.Add(nddevRetryExecutionTTL)
			record.UpdatedAt = now
			journal.Records[key] = record
			return fmt.Errorf("provider create circuit opened after %d attempts", record.Attempts)
		}
		record.JobID = key
		if scaleSetName != "" {
			record.ScaleSetName = scaleSetName
		}
		if record.Attempts < nddevRetryMaximum {
			record.Attempts++
		}
		record.UpdatedAt = now
		record.NextAllowedAt = now.Add(nddevRetryAttemptLease)
		journal.Records[key] = record
		return nil
	})
}

func nddevRecordProviderCreateFailure(ctx context.Context, key string, providerErr error) error {
	if key == "" {
		return nil
	}
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		record, exists := journal.Records[key]
		if !exists || record.Attempts < 1 {
			return fmt.Errorf("provider retry failure has no reserved attempt")
		}
		record.LastErrorClass = nddevProviderErrorClass(providerErr)
		record.UpdatedAt = now
		if record.LastErrorClass == "capacity" || record.LastErrorClass == "intent" {
			// Saturation and a canceled job whose pre-AcquireJobs intent has
			// already disappeared are bounded backpressure, not a broken provider.
			// Capacity accumulates delay without ever opening a circuit; a completed
			// provider deletion clears it immediately. Intent cancellation stays on
			// the short fixed delay because no scarce resource must be awaited.
			if record.LastErrorClass == "capacity" && strings.Contains(key, ":job:") && record.Attempts >= nddevRetryMaximum {
				record.NextAllowedAt = now
				record.TerminalUntil = now.Add(nddevRetryExecutionTTL)
			} else if record.LastErrorClass == "capacity" {
				record.NextAllowedAt = now.Add(nddevCapacityRetryDelay(key, record.Attempts))
				nddevRecordSharedCapacityFailure(journal, now, record.ScaleSetName)
			} else {
				// A missing intent is a cancellation race, not load. Keep it cheap
				// and responsive without teaching it capacity's accumulating delay.
				record.Attempts = 1
				record.NextAllowedAt = now.Add(nddevRetryBase)
			}
			if record.LastErrorClass != "capacity" || !strings.Contains(key, ":job:") || record.Attempts < nddevRetryMaximum {
				record.TerminalUntil = time.Time{}
			}
			journal.Records[key] = record
			domainKey := nddevRetryDomainKey(key)
			if domainKey != key {
				domain := journal.Records[domainKey]
				domain.JobID = domainKey
				domain.ScaleSetName = record.ScaleSetName
				domain.LastErrorClass = record.LastErrorClass
				domain.UpdatedAt = now
				if record.LastErrorClass == "capacity" {
					if domain.Attempts < nddevRetryMaximum {
						domain.Attempts++
					}
					domain.NextAllowedAt = now.Add(nddevCapacityRetryDelay(domainKey, domain.Attempts))
				} else {
					domain.Attempts = 1
					domain.NextAllowedAt = now.Add(nddevRetryBase)
				}
				domain.TerminalUntil = time.Time{}
				journal.Records[domainKey] = domain
			}
			return nil
		}
		if record.Attempts >= nddevRetryMaximum {
			record.NextAllowedAt = now
			record.TerminalUntil = now.Add(nddevRetryExecutionTTL)
		} else {
			record.NextAllowedAt = now.Add(nddevRetryDelay(key, record.Attempts))
		}
		journal.Records[key] = record

		domainKey := nddevRetryDomainKey(key)
		if domainKey != key {
			domain := journal.Records[domainKey]
			domain.JobID = domainKey
			domain.ScaleSetName = record.ScaleSetName
			if domain.Attempts < nddevRetryMaximum {
				domain.Attempts++
			}
			domain.LastErrorClass = record.LastErrorClass
			domain.UpdatedAt = now
			if domain.Attempts >= nddevRetryMaximum {
				domain.NextAllowedAt = now
				domain.TerminalUntil = now.Add(nddevRetryExecutionTTL)
			} else {
				domain.NextAllowedAt = now.Add(nddevRetryDelay(domainKey, domain.Attempts))
			}
			journal.Records[domainKey] = domain
		}
		return nil
	})
}

// NDDevProviderCapacityReleased grants one waiting retry domain after one
// provider deletion has actually completed. One deleted VM releases capacity
// for at most one replacement VM; clearing every saturation record wakes every
// scale set at once and turns one free slot into a create storm. The oldest
// waiting domain wins, with its oldest concrete attempt, while transport,
// identity, intent, and every other capacity backoff remain intact.
func NDDevProviderCapacityReleased(ctx context.Context) error {
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		activeScaleSets, filterActive, err := nddevActiveQueueScaleSets()
		if err != nil {
			return err
		}
		domainKey := nddevOldestEligibleCapacityDomain(journal, activeScaleSets, filterActive)
		if domainKey == "" {
			if filterActive {
				nddevGrantSharedCapacityProbe(journal, now, "", "", "worker-deleted")
				return nil
			}
			// Schema-v1 journals written before domain backoff existed may carry
			// only concrete records. Releasing one of those remains bounded.
			domainKey = nddevOldestCapacityRecord(journal, false, "")
			if domainKey == "" {
				nddevGrantSharedCapacityProbe(journal, now, "", "", "worker-deleted")
				return nil
			}
			nddevGrantSharedCapacityProbe(journal, now, nddevRetryDomainKey(domainKey), journal.Records[domainKey].ScaleSetName, "worker-deleted")
			delete(journal.Records, domainKey)
			return nil
		}
		scaleSetName := journal.Records[domainKey].ScaleSetName
		delete(journal.Records, domainKey)
		if concreteKey := nddevOldestCapacityRecord(journal, false, domainKey); concreteKey != "" {
			delete(journal.Records, concreteKey)
		}
		nddevGrantSharedCapacityProbe(journal, now, domainKey, scaleSetName, "worker-deleted")
		return nil
	})
}

func nddevOldestCapacityRecord(journal *nddevRetryJournal, domainOnly bool, domain string) string {
	selected := ""
	for key, record := range journal.Records {
		if key == nddevCapacityDomainKey {
			continue
		}
		isDomain := nddevRetryDomainKey(key) == key
		if record.LastErrorClass != "capacity" || isDomain != domainOnly ||
			(domain != "" && nddevRetryDomainKey(key) != domain) {
			continue
		}
		if selected == "" || record.UpdatedAt.Before(journal.Records[selected].UpdatedAt) ||
			(record.UpdatedAt.Equal(journal.Records[selected].UpdatedAt) && key < selected) {
			selected = key
		}
	}
	return selected
}

func nddevRecordProviderCreateSuccess(ctx context.Context, key string) error {
	if key == "" {
		return nil
	}
	return nddevUpdateRetryJournal(ctx, func(journal *nddevRetryJournal, now time.Time) error {
		delete(journal.Records, key)
		delete(journal.Records, nddevRetryDomainKey(key))
		if shared, exists := journal.Records[nddevCapacityDomainKey]; exists {
			ownerDomain := nddevRetryDomainKey(shared.ProbeOwner)
			if shared.ProbeOwner == "" || shared.ProbeOwner == key || ownerDomain == nddevRetryDomainKey(key) {
				// A successful shared probe proves the measured envelope has
				// capacity again. Leave saturated mode entirely so independent
				// scale sets can resume bounded parallel creates. The next typed
				// capacity refusal recreates the shared single-probe domain.
				delete(journal.Records, nddevCapacityDomainKey)
			}
		}
		return nil
	})
}

// nddevSharedCapacityCreateAllowed turns a proven fleet-wide saturation event
// into one durable retry lease shared by every scale set. Before the first
// capacity refusal there is no shared record and independent cold creates stay
// parallel. Afterwards the oldest waiting scale-set domain owns the next probe;
// a completed deletion may grant that owner immediately.
func nddevSharedCapacityCreateAllowed(journal *nddevRetryJournal, now time.Time, key, scaleSetName string, reserve bool) error {
	shared, saturated := journal.Records[nddevCapacityDomainKey]
	if !saturated {
		return nil
	}
	domainKey := nddevRetryDomainKey(key)
	activeScaleSets, filterActive, err := nddevActiveQueueScaleSets()
	if err != nil {
		return err
	}
	owner := shared.ProbeOwner
	if owner != "" && filterActive && (shared.ScaleSetName == "" || !activeScaleSets[shared.ScaleSetName]) {
		owner = ""
		shared.ProbeOwner = ""
		shared.ScaleSetName = ""
		journal.Records[nddevCapacityDomainKey] = shared
	}
	if owner != "" && !shared.NextAllowedAt.After(now) &&
		(strings.Contains(owner, ":instance:") || strings.Contains(owner, ":job:")) {
		owner = ""
		shared.ProbeOwner = ""
		shared.ScaleSetName = ""
		shared.WakeReason = "worker-deleted"
		shared.UpdatedAt = now
		shared.NextAllowedAt = now
		journal.Records[nddevCapacityDomainKey] = shared
	}
	if owner == "" {
		owner = nddevOldestEligibleCapacityDomain(journal, activeScaleSets, filterActive)
		if owner != "" && reserve {
			shared.ProbeOwner = owner
			shared.ScaleSetName = journal.Records[owner].ScaleSetName
		}
	}
	ownerDomain := nddevRetryDomainKey(owner)
	if owner != "" && domainKey != ownerDomain {
		return fmt.Errorf("shared capacity retry is deferred behind oldest owner %s", ownerDomain)
	}
	if shared.NextAllowedAt.After(now) {
		return fmt.Errorf("shared capacity retry is deferred until %s after %s", shared.NextAllowedAt.Format(time.RFC3339), shared.WakeReason)
	}
	if !reserve {
		return nil
	}
	if owner != "" && (strings.Contains(owner, ":instance:") || strings.Contains(owner, ":job:")) {
		if key != owner {
			return fmt.Errorf("shared capacity probe is owned by %s", owner)
		}
	}
	shared.ProbeOwner = key
	shared.ScaleSetName = scaleSetName
	shared.WakeReason = "probe-leased"
	shared.UpdatedAt = now
	shared.NextAllowedAt = now.Add(nddevRetryAttemptLease)
	journal.Records[nddevCapacityDomainKey] = shared
	return nil
}

func nddevRecordSharedCapacityFailure(journal *nddevRetryJournal, now time.Time, scaleSetName string) {
	shared := journal.Records[nddevCapacityDomainKey]
	shared.JobID = nddevCapacityDomainKey
	if shared.Attempts < nddevRetryMaximum {
		shared.Attempts++
	}
	shared.LastErrorClass = "capacity"
	shared.UpdatedAt = now
	shared.NextAllowedAt = now.Add(nddevCapacityRetryDelay(nddevCapacityDomainKey, shared.Attempts))
	shared.TerminalUntil = time.Time{}
	shared.ProbeOwner = ""
	shared.ScaleSetName = scaleSetName
	shared.WakeReason = "capacity-refused"
	journal.Records[nddevCapacityDomainKey] = shared
}

func nddevGrantSharedCapacityProbe(journal *nddevRetryJournal, now time.Time, owner, scaleSetName, reason string) {
	shared := journal.Records[nddevCapacityDomainKey]
	if shared.Attempts < 1 {
		shared.Attempts = 1
	}
	shared.JobID = nddevCapacityDomainKey
	shared.LastErrorClass = "capacity"
	shared.UpdatedAt = now
	shared.NextAllowedAt = now
	shared.TerminalUntil = time.Time{}
	shared.ProbeOwner = owner
	shared.ScaleSetName = scaleSetName
	shared.WakeReason = reason
	journal.Records[nddevCapacityDomainKey] = shared
}

func nddevOldestEligibleCapacityDomain(journal *nddevRetryJournal, activeScaleSets map[string]bool, filterActive bool) string {
	selected := ""
	for key, record := range journal.Records {
		if key == nddevCapacityDomainKey || nddevRetryDomainKey(key) != key || record.LastErrorClass != "capacity" {
			continue
		}
		if filterActive && (record.ScaleSetName == "" || !activeScaleSets[record.ScaleSetName]) {
			continue
		}
		if selected == "" || record.UpdatedAt.Before(journal.Records[selected].UpdatedAt) ||
			(record.UpdatedAt.Equal(journal.Records[selected].UpdatedAt) && key < selected) {
			selected = key
		}
	}
	return selected
}

func nddevActiveQueueScaleSets() (map[string]bool, bool, error) {
	intents, configured, err := nddevReadQueueRetryIntents(nddevRetryNow().UTC())
	if err != nil || !configured {
		return nil, configured, err
	}
	active := map[string]bool{}
	for _, intent := range intents {
		if name := strings.TrimSpace(intent.ScaleSetName); name != "" {
			active[name] = true
		}
	}
	return active, true, nil
}

func nddevActiveQueueIntents(now time.Time) ([]nddevQueueRetryIntent, error) {
	intents, configured, err := nddevReadQueueRetryIntents(now)
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, fmt.Errorf("queue intent path is required for pre-job retry identity")
	}
	return intents, nil
}

func nddevReadQueueRetryIntents(now time.Time) ([]nddevQueueRetryIntent, bool, error) {
	path := strings.TrimSpace(os.Getenv(nddevQueueIntentFileEnv))
	if path == "" {
		return nil, false, nil
	}
	if !nddevBoundedAbsolutePath(path) {
		return nil, true, fmt.Errorf("queue intent path is unavailable or unsafe")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, true, fmt.Errorf("open queue intent journal: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, true, fmt.Errorf("queue intent journal must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, nddevRetryMaximumBytes+1))
	if err != nil || len(data) > nddevRetryMaximumBytes {
		return nil, true, fmt.Errorf("read queue intent journal: invalid bounded content")
	}
	var queue struct {
		Intents map[string]nddevQueueRetryIntent `json:"intents"`
	}
	if err := json.Unmarshal(data, &queue); err != nil {
		return nil, true, fmt.Errorf("decode queue intent journal: %w", err)
	}
	active := make([]nddevQueueRetryIntent, 0, len(queue.Intents))
	for key, intent := range queue.Intents {
		switch intent.State {
		case "acquiring", "acquired", "assigned":
			if intent.Key == "" {
				intent.Key = key
			}
			if intent.ExpiresAt.After(now) {
				active = append(active, intent)
			}
		}
	}
	return active, true, nil
}

func nddevRetryDelay(key string, attempt int) time.Duration {
	shift := max(0, min(attempt-1, 6))
	delay := nddevRetryBase * time.Duration(1<<shift)
	if delay > nddevRetryCap {
		delay = nddevRetryCap
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d", key, attempt)))
	jitterWindow := max(time.Second, delay/4)
	jitter := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(jitterWindow))
	return delay + jitter
}

func nddevCapacityRetryDelay(key string, attempt int) time.Duration {
	delay := nddevRetryDelay(key, attempt)
	if delay > nddevCapacityRetryCap {
		return nddevCapacityRetryCap
	}
	return delay
}

func nddevProviderErrorClass(err error) string {
	message := strings.ToLower(fmt.Sprint(err))
	switch {
	case strings.Contains(message, "pool-saturated"),
		strings.Contains(message, "insufficient-cpu"),
		strings.Contains(message, "insufficient-memory"),
		strings.Contains(message, "memory-pressure"),
		strings.Contains(message, "cpu-pressure"),
		strings.Contains(message, "io-pressure"),
		strings.Contains(message, "disk-pressure"),
		strings.Contains(message, "pressure-unavailable"),
		strings.Contains(message, "host-unhealthy"),
		strings.Contains(message, "recent-oom"),
		strings.Contains(message, "project memory limit"),
		strings.Contains(message, "project disk limit"),
		strings.Contains(message, "instance-count limit"),
		strings.Contains(message, "storage high-watermark"),
		strings.Contains(message, "storage-high-watermark"),
		strings.Contains(message, "no eligible member"):
		return "capacity"
	case strings.Contains(message, "incomplete instance metadata"):
		return "capacity"
	case strings.Contains(message, "no active pre-acquirejobs"),
		strings.Contains(message, "instance stopped during canceled create"),
		strings.Contains(message, "instance is not running"):
		return "intent"
	case strings.Contains(message, "provider identity"), strings.Contains(message, "provider-commit"):
		return "identity"
	case strings.Contains(message, "timeout"), strings.Contains(message, "deadline"):
		return "timeout"
	default:
		return "provider"
	}
}

func nddevUpdateRetryJournal(ctx context.Context, mutate func(*nddevRetryJournal, time.Time) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, lockPath := os.Getenv(nddevRetryFileEnv), os.Getenv(nddevRetryLockEnv)
	if !nddevBoundedAbsolutePath(path) || !nddevBoundedAbsolutePath(lockPath) || filepath.Clean(path) == filepath.Clean(lockPath) {
		return fmt.Errorf("provider retry journal paths are unavailable or unsafe")
	}
	lockFD, err := syscall.Open(lockPath, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open provider retry lock: %w", err)
	}
	lock := os.NewFile(uintptr(lockFD), lockPath)
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock provider retry journal: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	journal, err := nddevReadRetryJournal(path)
	if errors.Is(err, os.ErrNotExist) {
		journal = nddevRetryJournal{
			SchemaVersion: nddevRetrySchemaVersion,
			Records:       map[string]nddevRetryRecord{},
			Reservations:  map[string]nddevRetryReservation{},
		}
	} else if err != nil {
		return err
	}
	before, _ := json.Marshal(journal)
	now := nddevRetryNow().UTC()
	for key, record := range journal.Records {
		terminalExpired := !record.TerminalUntil.IsZero() && !record.TerminalUntil.After(now)
		staleNonTerminal := record.TerminalUntil.IsZero() && !record.NextAllowedAt.After(now) &&
			!record.UpdatedAt.Add(nddevRetryStaleTTL).After(now)
		if terminalExpired || staleNonTerminal {
			delete(journal.Records, key)
		}
	}
	for instanceName, reservation := range journal.Reservations {
		if !reservation.UpdatedAt.Add(nddevRetryAttemptLease).After(now) {
			delete(journal.Reservations, instanceName)
		}
	}
	if err := mutate(&journal, now); err != nil {
		return err
	}
	if err := journal.Validate(); err != nil {
		return err
	}
	after, _ := json.Marshal(journal)
	if bytes.Equal(before, after) && nddevFileExists(path) {
		return nil
	}
	journal.Generation++
	journal.UpdatedAt = now
	return nddevWriteRetryJournal(path, journal)
}

func (j nddevRetryJournal) Validate() error {
	if j.SchemaVersion != nddevRetrySchemaVersion || j.Records == nil || j.Reservations == nil {
		return fmt.Errorf("provider retry journal identity is invalid")
	}
	for key, record := range j.Records {
		if key == "" || len(key) > 256 || record.JobID != key || record.Attempts < 1 || record.Attempts > nddevRetryMaximum ||
			record.UpdatedAt.IsZero() || record.NextAllowedAt.IsZero() || record.NextAllowedAt.Before(record.UpdatedAt) {
			return fmt.Errorf("provider retry record %q is invalid", key)
		}
		if record.LastErrorClass != "" && record.LastErrorClass != "capacity" && record.LastErrorClass != "intent" &&
			record.LastErrorClass != "identity" && record.LastErrorClass != "timeout" && record.LastErrorClass != "provider" {
			return fmt.Errorf("provider retry record %q has invalid error class", key)
		}
		if !record.TerminalUntil.IsZero() && record.TerminalUntil.Before(record.UpdatedAt) {
			return fmt.Errorf("provider retry record %q has invalid circuit expiry", key)
		}
		if len(record.ProbeOwner) > 256 || (record.ProbeOwner != "" && key != nddevCapacityDomainKey) {
			return fmt.Errorf("provider retry record %q has invalid shared probe owner", key)
		}
		if record.WakeReason != "" && record.WakeReason != "capacity-refused" && record.WakeReason != "worker-deleted" &&
			record.WakeReason != "probe-leased" && record.WakeReason != "probe-succeeded" {
			return fmt.Errorf("provider retry record %q has invalid shared wake reason", key)
		}
		if record.WakeReason != "" && key != nddevCapacityDomainKey {
			return fmt.Errorf("provider retry record %q carries shared wake state", key)
		}
		if len(record.ScaleSetName) > 128 || strings.TrimSpace(record.ScaleSetName) != record.ScaleSetName ||
			strings.ContainsAny(record.ScaleSetName, "\r\n\t") {
			return fmt.Errorf("provider retry record %q has invalid scale-set name", key)
		}
	}
	claimed := make(map[string]string, len(j.Reservations))
	for instanceName, reservation := range j.Reservations {
		if instanceName == "" || len(instanceName) > 128 || strings.TrimSpace(instanceName) != instanceName ||
			strings.ContainsAny(instanceName, "\r\n\t") || reservation.UpdatedAt.IsZero() ||
			!strings.Contains(reservation.RetryKey, ":job:") || len(reservation.RetryKey) > 256 ||
			reservation.ScaleSetName == "" || len(reservation.ScaleSetName) > 128 ||
			strings.TrimSpace(reservation.ScaleSetName) != reservation.ScaleSetName || strings.ContainsAny(reservation.ScaleSetName, "\r\n\t") {
			return fmt.Errorf("provider retry reservation %q is invalid", instanceName)
		}
		if previous, exists := claimed[reservation.RetryKey]; exists && previous != instanceName {
			return fmt.Errorf("provider retry key %q is reserved by multiple instances", reservation.RetryKey)
		}
		claimed[reservation.RetryKey] = instanceName
	}
	return nil
}

func nddevReadRetryJournal(path string) (nddevRetryJournal, error) {
	file, err := os.Open(path)
	if err != nil {
		return nddevRetryJournal{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nddevRetryJournal{}, fmt.Errorf("provider retry journal must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, nddevRetryMaximumBytes+1))
	if err != nil || len(data) > nddevRetryMaximumBytes {
		return nddevRetryJournal{}, fmt.Errorf("read provider retry journal: invalid bounded content")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var journal nddevRetryJournal
	if err := decoder.Decode(&journal); err != nil {
		return nddevRetryJournal{}, fmt.Errorf("decode provider retry journal: %w", err)
	}
	if journal.SchemaVersion == nddevRetryPreviousSchemaVersion {
		journal.SchemaVersion = nddevRetrySchemaVersion
		journal.Reservations = map[string]nddevRetryReservation{}
	}
	if err := journal.Validate(); err != nil {
		return nddevRetryJournal{}, err
	}
	return journal, nil
}

func nddevWriteRetryJournal(path string, journal nddevRetryJournal) error {
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".create-retries-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
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
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer directoryHandle.Close()
	return directoryHandle.Sync()
}

func nddevBoundedAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) != string(filepath.Separator)
}

func nddevFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
