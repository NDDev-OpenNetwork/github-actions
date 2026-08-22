package fleetobserve

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerretry"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

const (
	// 14 separates first-observed created-to-deleting inventory convergence from
	// persistent missing ownership. 13 adds bounded GitHub run/request/runner correlation completeness. 12 separates bounded successful-delete convergence from persistent missing
	// runner ownership. 11 separated exact image builder/smoke inventory from orphan job runners.
	// 9 added exact JobStarted runner correlation and symmetric created-without-
	// running identity telemetry. Version 8 added role-correct central exporter
	// health, container admission readiness, phase ages and rollback-compatible
	// WAL progress semantics.
	SchemaVersion                   = 14
	diagnosticExportStatusMaxAge    = 3 * time.Minute
	diagnosticExportSyncGracePeriod = 90 * time.Second
	deletingVisibilityGrace         = 30 * time.Second
	createdVisibilityGrace          = 30 * time.Second
)

const (
	diagnosticExportSyncUnavailable  = "unavailable"
	diagnosticExportSyncSynchronized = "synchronized"
	diagnosticExportSyncGrace        = "convergence-grace"
	diagnosticExportSyncInvalid      = "invalid"
)

// requiredServices is what a serving host must have running for the observer to
// call itself healthy. The diagnostic exporter timer and the collector were
// missing while the observer depended on both -- it reads the exporter's state
// file and ships its own metrics through the collector -- so a host could lose
// either and still report healthy.
var computeHostServices = []string{
	"garm",
	"gha-fleet-gateway",
	"gha-rustfs",
	"gha-warm-pool.timer",
	"gha-zot",
	"gha-pressure-gate.timer",
	"gha-diagnostic-exporter.service",
	"gha-diagnostic-exporter.timer",
	"otelcol-fleet",
}

func ServiceNames() []string {
	return append([]string(nil), computeHostServices...)
}

func serviceNamesForConfig(platform config.Config) []string {
	if !platform.Incus.Cluster.Enabled {
		return []string{
			"garm", "gha-fleet-gateway", "gha-cache-broker", "gha-diagnostic-exporter.service",
			"gha-diagnostic-exporter.timer", "otelcol-fleet",
		}
	}
	return ServiceNames()
}

type HostSource func(context.Context) (hostprobe.Snapshot, error)
type JournalSource func(context.Context) (providerjournal.Journal, error)
type ProviderRetrySource func(time.Time) (providerretry.Snapshot, error)
type QueueIntentSource func(context.Context) (queueintent.Snapshot, error)
type InstanceSource func(context.Context) ([]string, error)
type DiagnosticsSource func(time.Time) (workerdiagnostics.SpoolStats, error)
type DiagnosticExportSource func() (diagnosticexport.Status, error)
type ServiceSource func(context.Context, string) (string, error)

type Collector struct {
	Config             config.Config
	Host               HostSource
	Journal            JournalSource
	ProviderRetry      ProviderRetrySource
	Queue              QueueIntentSource
	Instances          InstanceSource
	Diagnostics        DiagnosticsSource
	Export             DiagnosticExportSource
	Service            ServiceSource
	DiagnosticMaxBytes int64
	Now                func() time.Time
	CreatedVisibility  *CreatedVisibilityTracker
}

// CreatedVisibilityTracker remembers when a created lease was first observed
// absent from Incus. Lease age cannot answer this question: a worker can run for
// hours before terminal teardown creates a short journal/inventory race.
type CreatedVisibilityTracker struct {
	mu        sync.Mutex
	firstSeen map[string]time.Time
	grace     time.Duration
}

func NewCreatedVisibilityTracker() *CreatedVisibilityTracker {
	return &CreatedVisibilityTracker{
		firstSeen: make(map[string]time.Time),
		grace:     createdVisibilityGrace,
	}
}

func (t *CreatedVisibilityTracker) observe(names []string, now time.Time) (map[string]bool, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	current := make(map[string]struct{}, len(names))
	within := make(map[string]bool, len(names))
	for _, name := range names {
		current[name] = struct{}{}
		first, exists := t.firstSeen[name]
		if !exists || first.After(now) {
			first = now
			t.firstSeen[name] = first
		}
		age := now.Sub(first)
		within[name] = age <= t.grace
	}
	for name := range t.firstSeen {
		if _, exists := current[name]; !exists {
			delete(t.firstSeen, name)
		}
	}
	var oldest int64
	for name, isWithin := range within {
		if !isWithin {
			continue
		}
		age := int64(now.Sub(t.firstSeen[name]) / time.Second)
		if age > oldest {
			oldest = age
		}
	}
	return within, oldest
}

type Snapshot struct {
	SchemaVersion        int                          `json:"schema_version"`
	CapturedAt           time.Time                    `json:"captured_at"`
	Healthy              bool                         `json:"healthy"`
	Host                 hostprobe.Snapshot           `json:"host"`
	Pools                []PoolStatus                 `json:"pools"`
	Journal              JournalSummary               `json:"journal"`
	ProviderRetry        providerretry.Snapshot       `json:"provider_retry"`
	Queue                QueueSummary                 `json:"queue"`
	Incus                IncusSummary                 `json:"incus"`
	Diagnostics          workerdiagnostics.SpoolStats `json:"diagnostics"`
	DiagnosticExport     diagnosticexport.Status      `json:"diagnostic_export"`
	DiagnosticExportSync DiagnosticExportSync         `json:"diagnostic_export_sync"`
	Services             []ServiceStatus              `json:"services"`
	CollectionErrors     []string                     `json:"collection_errors"`
}

type DiagnosticExportSync struct {
	State                 string `json:"state"`
	GracePeriodSeconds    int64  `json:"grace_period_seconds"`
	GraceRemainingSeconds int64  `json:"grace_remaining_seconds"`
	LocalBundleDelta      int    `json:"local_bundle_delta"`
	LocalByteDelta        int64  `json:"local_byte_delta"`
}

type PoolStatus struct {
	Name                    string `json:"name"`
	PilotReady              bool   `json:"pilot_ready"`
	ContainerAdmissionReady bool   `json:"container_admission_ready"`
	Blockers                int    `json:"blockers"`
	Warnings                int    `json:"warnings"`
	Info                    int    `json:"info"`
}

type JournalSummary struct {
	SchemaVersion                                 int              `json:"schema_version"`
	Generation                                    uint64           `json:"generation"`
	UpdatedAt                                     time.Time        `json:"updated_at"`
	Leases                                        int              `json:"leases"`
	Claims                                        int              `json:"claims"`
	WarmPreemptions                               int              `json:"warm_preemptions"`
	WarmPreemptionsTotal                          uint64           `json:"warm_preemptions_total"`
	ByState                                       map[string]int   `json:"by_state"`
	OldestStateAgeSeconds                         map[string]int64 `json:"oldest_state_age_seconds"`
	CreatedWithoutRunningIdentity                 int              `json:"created_without_running_identity"`
	OldestCreatedWithoutRunningIdentityAgeSeconds int64            `json:"oldest_created_without_running_identity_age_seconds"`
}

type QueueSummary struct {
	Generation                   uint64           `json:"generation"`
	Stored                       int              `json:"stored"`
	Active                       int              `json:"active"`
	Expired                      int              `json:"expired"`
	InFlight                     int              `json:"in_flight"`
	OldestQueueAgeSeconds        int64            `json:"oldest_queue_age_seconds"`
	UncoveredRunning             int              `json:"uncovered_running"`
	RunningWithoutRunnerIdentity int              `json:"running_without_runner_identity"`
	UnboundRepository            int              `json:"unbound_repository"`
	MissingRunnerRequestID       int              `json:"missing_runner_request_id"`
	MissingWorkflowRunID         int              `json:"missing_workflow_run_id"`
	RunningMissingGitHubRunnerID int              `json:"running_missing_github_runner_id"`
	ByState                      map[string]int   `json:"by_state"`
	OldestStateAgeSeconds        map[string]int64 `json:"oldest_state_age_seconds"`
	ByPriority                   map[int]int      `json:"by_priority"`
	ByScaleSet                   map[string]int   `json:"by_scale_set"`
}

type IncusSummary struct {
	VisibleInstances            int   `json:"visible_instances"`
	VisibleMaintenanceInstances int   `json:"visible_maintenance_instances"`
	OrphanInstances             int   `json:"orphan_instances"`
	MissingInstances            int   `json:"missing_instances"`
	MissingDeletingWithinGrace  int   `json:"missing_deleting_within_grace"`
	MissingCreatedWithinGrace   int   `json:"missing_created_within_grace"`
	OldestMissingCreatedAgeSecs int64 `json:"oldest_missing_created_within_grace_age_seconds"`
	MissingMaintenanceInstances int   `json:"missing_maintenance_instances"`
}

type ServiceStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	Active bool   `json:"active"`
}

func (c Collector) Collect(ctx context.Context) Snapshot {
	now := c.now()
	requiredServices := serviceNamesForConfig(c.Config)
	snapshot := Snapshot{
		SchemaVersion:    SchemaVersion,
		CapturedAt:       now,
		Pools:            make([]PoolStatus, 0, len(c.Config.Pools)),
		Services:         make([]ServiceStatus, len(requiredServices)),
		CollectionErrors: make([]string, 0),
		DiagnosticExportSync: DiagnosticExportSync{
			State:              diagnosticExportSyncUnavailable,
			GracePeriodSeconds: int64(diagnosticExportSyncGracePeriod / time.Second),
		},
	}

	var host hostprobe.Snapshot
	var hostErr error
	var journal providerjournal.Journal
	var journalErr error
	var providerRetry providerretry.Snapshot
	var providerRetryErr error
	var queue queueintent.Snapshot
	var queueErr error
	var instances []string
	var instancesErr error
	var diagnostics workerdiagnostics.SpoolStats
	var diagnosticsErr error
	var diagnosticExport diagnosticexport.Status
	var diagnosticExportErr error
	serviceErrors := make([]error, len(requiredServices))

	var group sync.WaitGroup
	sourceCount := 6
	if c.Export != nil {
		sourceCount++
	}
	group.Add(sourceCount + len(requiredServices))
	go func() {
		defer group.Done()
		host, hostErr = c.Host(ctx)
	}()
	go func() {
		defer group.Done()
		journal, journalErr = c.Journal(ctx)
	}()
	go func() {
		defer group.Done()
		providerRetry, providerRetryErr = c.ProviderRetry(now)
	}()
	go func() {
		defer group.Done()
		queue, queueErr = c.Queue(ctx)
	}()
	go func() {
		defer group.Done()
		instances, instancesErr = c.Instances(ctx)
	}()
	go func() {
		defer group.Done()
		diagnostics, diagnosticsErr = c.Diagnostics(now)
	}()
	if c.Export != nil {
		go func() {
			defer group.Done()
			diagnosticExport, diagnosticExportErr = c.Export()
		}()
	}
	for index, name := range requiredServices {
		index, name := index, name
		go func() {
			defer group.Done()
			state, err := c.Service(ctx, name)
			serviceErrors[index] = err
			snapshot.Services[index] = ServiceStatus{Name: name, State: state, Active: serviceHealthy(name, state)}
		}()
	}
	group.Wait()

	if hostErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("host", hostErr))
	} else {
		snapshot.Host = host
		for _, pool := range c.Config.Pools {
			decision := hostprobe.EvaluateColdPilot(host, c.Config.HostReserve, pool)
			status := PoolStatus{Name: pool.Name, PilotReady: decision.PilotReady}
			for _, finding := range decision.Findings {
				switch finding.Severity {
				case hostprobe.SeverityBlocker:
					status.Blockers++
				case hostprobe.SeverityWarning:
					status.Warnings++
				case hostprobe.SeverityInfo:
					status.Info++
				}
			}
			snapshot.Pools = append(snapshot.Pools, status)
		}
	}

	coveredNames := make(map[string]struct{})
	expectedNames := make(map[string]providerjournal.Lease)
	expectedMaintenanceNames := make(map[string]struct{})
	if journalErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("journal", journalErr))
	} else {
		snapshot.Journal = summarizeJournal(journal, now)
		for name, lease := range journal.Leases {
			// Incus can publish the instance before the provider's final created
			// transaction. An admitted lease therefore covers a visible name but
			// does not yet require one: both sides of that transition are valid.
			if lease.State == providerjournal.StateAdmitted {
				coveredNames[name] = struct{}{}
			}
			if lease.State == providerjournal.StateCreated || lease.State == providerjournal.StateDeleting ||
				lease.State == providerjournal.StateWarmReady || lease.State == providerjournal.StateWarmClaimed {
				coveredNames[name] = struct{}{}
				if strings.HasPrefix(lease.PoolID, "image-maintenance/") {
					expectedMaintenanceNames[name] = struct{}{}
				} else {
					expectedNames[name] = lease
				}
			}
		}
	}
	if providerRetryErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("provider retry", providerRetryErr))
	} else {
		snapshot.ProviderRetry = providerRetry
		if providerRetry.TerminalCircuits > 0 {
			snapshot.CollectionErrors = append(snapshot.CollectionErrors, "provider retry: terminal create circuit blocks a scale set")
		}
	}
	if queueErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("queue intent", queueErr))
	} else {
		queueSummary, err := summarizeQueue(queue, c.Config, now)
		if err != nil {
			snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("queue intent", err))
		} else {
			snapshot.Queue = queueSummary
		}
	}

	if instancesErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("incus", instancesErr))
	} else {
		visibleNames, err := validateInstanceNames(instances)
		if err != nil {
			snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("incus", err))
		} else {
			snapshot.Incus.VisibleInstances = len(visibleNames)
			if journalErr == nil {
				createdWithinGrace := map[string]bool{}
				if c.CreatedVisibility != nil {
					missingCreated := make([]string, 0)
					for name, lease := range expectedNames {
						if lease.State != providerjournal.StateCreated {
							continue
						}
						if _, exists := visibleNames[name]; !exists {
							missingCreated = append(missingCreated, name)
						}
					}
					createdWithinGrace, snapshot.Incus.OldestMissingCreatedAgeSecs =
						c.CreatedVisibility.observe(missingCreated, now)
				}
				for name := range visibleNames {
					if _, exists := coveredNames[name]; !exists {
						if providerjournal.IsImageMaintenanceInstance(name) {
							snapshot.Incus.VisibleMaintenanceInstances++
						} else {
							snapshot.Incus.OrphanInstances++
						}
					}
				}
				for name, lease := range expectedNames {
					if _, exists := visibleNames[name]; !exists {
						age := now.Sub(lease.UpdatedAt)
						if lease.State == providerjournal.StateDeleting && age >= 0 && age <= deletingVisibilityGrace {
							snapshot.Incus.MissingDeletingWithinGrace++
						} else if createdWithinGrace[name] {
							snapshot.Incus.MissingCreatedWithinGrace++
						} else {
							snapshot.Incus.MissingInstances++
						}
					}
				}
				for name := range expectedMaintenanceNames {
					if _, exists := visibleNames[name]; !exists {
						snapshot.Incus.MissingMaintenanceInstances++
					}
				}
			}
		}
	}

	if diagnosticsErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("diagnostics", diagnosticsErr))
	} else {
		snapshot.Diagnostics = diagnostics
		if workerdiagnostics.AtDurableWALHighWatermark(diagnostics, c.DiagnosticMaxBytes) {
			snapshot.CollectionErrors = append(snapshot.CollectionErrors, "diagnostics: durable WAL high-watermark reached")
		}
	}
	if c.Export == nil {
		// The queue host receives diagnostic bundles but does not own the
		// credentialed object-store exporter. Compute hosts retain this gate.
	} else if diagnosticExportErr != nil {
		snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("diagnostic export", diagnosticExportErr))
	} else {
		snapshot.DiagnosticExport = diagnosticExport
		if diagnosticsErr == nil {
			syncStatus, err := validateDiagnosticExport(diagnosticExport, diagnostics, now)
			snapshot.DiagnosticExportSync = syncStatus
			if err != nil {
				snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("diagnostic export", err))
			}
		}
	}
	for index, err := range serviceErrors {
		if err != nil {
			snapshot.CollectionErrors = append(snapshot.CollectionErrors, safeError("service "+requiredServices[index], err))
		}
	}
	if journalErr == nil && queueErr == nil {
		correlation := summarizeExecutionCorrelation(journal, queue, now)
		snapshot.Queue.RunningWithoutRunnerIdentity = correlation.RunningWithoutRunnerIdentity
		snapshot.Journal.CreatedWithoutRunningIdentity = correlation.CreatedWithoutRunningIdentity
		snapshot.Journal.OldestCreatedWithoutRunningIdentityAgeSeconds = correlation.OldestCreatedWithoutRunningIdentityAgeSeconds
		executionLeases := snapshot.Journal.ByState[string(providerjournal.StateCreated)] +
			snapshot.Journal.ByState[string(providerjournal.StateWarmClaimed)]
		running := snapshot.Queue.ByState[string(queueintent.StateRunning)]
		if running > executionLeases {
			snapshot.Queue.UncoveredRunning = running - executionLeases
		}
	}
	sort.Strings(snapshot.CollectionErrors)
	snapshot.Healthy = len(snapshot.CollectionErrors) == 0 &&
		snapshot.Incus.OrphanInstances == 0 && snapshot.Incus.MissingInstances == 0 &&
		snapshot.Queue.UncoveredRunning == 0
	for _, service := range snapshot.Services {
		if !service.Active {
			snapshot.Healthy = false
		}
	}
	for index := range snapshot.Pools {
		pool, exists := c.Config.Pool(snapshot.Pools[index].Name)
		if !exists {
			continue
		}
		backend, exists := c.Config.Backend(pool.Backend)
		snapshot.Pools[index].ContainerAdmissionReady = exists && backend.Implementation == "incus-container" && snapshot.Healthy
	}
	return snapshot
}

type executionCorrelation struct {
	RunningWithoutRunnerIdentity                  int
	CreatedWithoutRunningIdentity                 int
	OldestCreatedWithoutRunningIdentityAgeSeconds int64
}

func summarizeExecutionCorrelation(journal providerjournal.Journal, queue queueintent.Snapshot, now time.Time) executionCorrelation {
	runningNames := make(map[string]struct{})
	result := executionCorrelation{}
	for _, intent := range queue.Active {
		if intent.State != queueintent.StateRunning {
			continue
		}
		if intent.RunnerName == "" {
			result.RunningWithoutRunnerIdentity++
			continue
		}
		runningNames[intent.RunnerName] = struct{}{}
	}
	for name, lease := range journal.Leases {
		if lease.State != providerjournal.StateCreated {
			continue
		}
		// Image builders and smoke instances are maintenance capacity, not
		// GitHub job runners. Direct Incus teardown can precede the next
		// provider reconciliation while the fleet is idle. Keep that gap
		// observable without reporting a job with a missing run identity.
		if strings.HasPrefix(lease.PoolID, "image-maintenance/") {
			continue
		}
		if _, covered := runningNames[name]; covered {
			continue
		}
		result.CreatedWithoutRunningIdentity++
		age := max(int64(0), int64(now.Sub(lease.AdmittedAt)/time.Second))
		if age > result.OldestCreatedWithoutRunningIdentityAgeSeconds {
			result.OldestCreatedWithoutRunningIdentityAgeSeconds = age
		}
	}
	return result
}

func (c Collector) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	if c.Host == nil || c.Journal == nil || c.ProviderRetry == nil || c.Queue == nil || c.Instances == nil || c.Diagnostics == nil || c.Service == nil {
		return fmt.Errorf("every observer source is required")
	}
	if c.DiagnosticMaxBytes <= 0 {
		return fmt.Errorf("observer diagnostic WAL byte boundary is required")
	}
	if !c.Config.Incus.Cluster.Enabled && c.Export == nil {
		return fmt.Errorf("services observer diagnostic export source is required")
	}
	// The inventory used to be asserted at exactly five, which made adding a
	// service two edits and stated a count rather than a property. What matters
	// is that it is not empty; which services belong in it is decided by the
	// list above and held by the deployment contract.
	if len(serviceNamesForConfig(c.Config)) == 0 {
		return fmt.Errorf("observer service inventory is empty")
	}
	return nil
}

func summarizeQueue(snapshot queueintent.Snapshot, platform config.Config, now time.Time) (QueueSummary, error) {
	knownScaleSets := make(map[string]struct{}, len(platform.Pools))
	for _, pool := range platform.Pools {
		knownScaleSets[pool.ScaleSetName] = struct{}{}
	}
	summary := QueueSummary{
		Generation:            snapshot.Generation,
		Stored:                snapshot.Stored,
		Active:                len(snapshot.Active),
		Expired:               snapshot.Expired,
		ByState:               make(map[string]int),
		OldestStateAgeSeconds: make(map[string]int64),
		ByPriority:            make(map[int]int),
		ByScaleSet:            make(map[string]int),
	}
	for scaleSet := range knownScaleSets {
		summary.ByScaleSet[scaleSet] = 0
	}
	for _, intent := range snapshot.Active {
		if _, exists := knownScaleSets[intent.ScaleSetName]; !exists {
			return QueueSummary{}, fmt.Errorf("active intent targets unknown scale set %q", intent.ScaleSetName)
		}
		if intent.QueueTime.After(now.Add(time.Second)) {
			return QueueSummary{}, fmt.Errorf("active intent %q has a future queue timestamp", intent.Key)
		}
		summary.ByState[string(intent.State)]++
		if intent.StateEnteredAt.After(now.Add(time.Second)) {
			return QueueSummary{}, fmt.Errorf("active intent %q has a future state timestamp", intent.Key)
		}
		stateAge := int64(now.Sub(intent.StateEnteredAt).Seconds())
		if stateAge > summary.OldestStateAgeSeconds[string(intent.State)] {
			summary.OldestStateAgeSeconds[string(intent.State)] = stateAge
		}
		summary.ByPriority[intent.Priority]++
		summary.ByScaleSet[intent.ScaleSetName]++
		if !strings.Contains(intent.Repository, "/") {
			summary.UnboundRepository++
		}
		if intent.RunnerRequestID == 0 {
			summary.MissingRunnerRequestID++
		}
		if intent.WorkflowRunID == 0 {
			summary.MissingWorkflowRunID++
		}
		if intent.State == queueintent.StateRunning && intent.GitHubRunnerID == 0 {
			summary.RunningMissingGitHubRunnerID++
		}
		if intent.State != queueintent.StateQueued {
			summary.InFlight++
		}
		age := int64(now.Sub(intent.QueueTime).Seconds())
		if age > summary.OldestQueueAgeSeconds {
			summary.OldestQueueAgeSeconds = age
		}
	}
	return summary, nil
}

func validateDiagnosticExport(
	status diagnosticexport.Status,
	spool workerdiagnostics.SpoolStats,
	now time.Time,
) (DiagnosticExportSync, error) {
	syncStatus := DiagnosticExportSync{
		State:              diagnosticExportSyncInvalid,
		GracePeriodSeconds: int64(diagnosticExportSyncGracePeriod / time.Second),
		LocalBundleDelta:   spool.Bundles - status.SourceBundles,
		LocalByteDelta:     spool.Bytes - status.SourceBytes,
	}
	if status.SchemaVersion != 1 || !diagnosticexport.StageAccepted(status.DeploymentStage) {
		return syncStatus, fmt.Errorf("status schema or deployment stage is invalid")
	}
	observedAt, err := status.ObservedTime()
	if err != nil || observedAt.After(now.Add(5*time.Second)) || now.Sub(observedAt) > diagnosticExportStatusMaxAge {
		return syncStatus, fmt.Errorf("status timestamp is invalid or stale")
	}
	legacyCounters := status.ExportedBundles+status.PendingBundles == status.SourceBundles
	walCounters := status.PendingBundles == status.SourceBundles
	if status.SourceBundles < 0 || status.ExportedBundles < 0 || status.PendingBundles < 0 ||
		(!legacyCounters && !walCounters) || status.SourceBytes < 0 || status.ExportedBytes < 0 {
		return syncStatus, fmt.Errorf("status counters are inconsistent")
	}
	if status.LastErrorCode != "" || status.ConsecutiveFailures != 0 {
		return syncStatus, fmt.Errorf("status reports failed exports")
	}
	lastSuccess, err := status.LastSuccessTime()
	if err != nil || lastSuccess.IsZero() || lastSuccess.After(now.Add(5*time.Second)) {
		return syncStatus, fmt.Errorf("last successful export timestamp is invalid")
	}
	statusAge := now.Sub(observedAt)
	if statusAge < 0 {
		statusAge = 0
	}
	if status.PendingBundles != 0 {
		if statusAge >= diagnosticExportSyncGracePeriod {
			return syncStatus, fmt.Errorf("pending exports exceeded the %s convergence grace", diagnosticExportSyncGracePeriod)
		}
		syncStatus.State = diagnosticExportSyncGrace
		remaining := diagnosticExportSyncGracePeriod - statusAge
		syncStatus.GraceRemainingSeconds = int64((remaining + time.Second - 1) / time.Second)
		return syncStatus, nil
	}
	if syncStatus.LocalBundleDelta == 0 && syncStatus.LocalByteDelta == 0 {
		syncStatus.State = diagnosticExportSyncSynchronized
		return syncStatus, nil
	}
	if (syncStatus.LocalBundleDelta > 0) != (syncStatus.LocalByteDelta > 0) ||
		(syncStatus.LocalBundleDelta < 0) != (syncStatus.LocalByteDelta < 0) ||
		syncStatus.LocalBundleDelta == 0 || syncStatus.LocalByteDelta == 0 {
		return syncStatus, fmt.Errorf("status and local spool diverge incoherently")
	}
	if statusAge >= diagnosticExportSyncGracePeriod {
		return syncStatus, fmt.Errorf("status and local spool exceeded the %s convergence grace", diagnosticExportSyncGracePeriod)
	}
	syncStatus.State = diagnosticExportSyncGrace
	remaining := diagnosticExportSyncGracePeriod - statusAge
	syncStatus.GraceRemainingSeconds = int64((remaining + time.Second - 1) / time.Second)
	return syncStatus, nil
}

func serviceHealthy(name, state string) bool {
	if name == "gha-diagnostic-exporter.service" {
		// A successful oneshot is normally inactive between timer firings. Failed
		// is the state that must make services-host health false.
		return state == "active" || state == "activating" || state == "deactivating" || state == "inactive"
	}
	return state == "active"
}

func (c Collector) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func summarizeJournal(journal providerjournal.Journal, now time.Time) JournalSummary {
	byState := map[string]int{
		string(providerjournal.StateAdmitted):    0,
		string(providerjournal.StateCreated):     0,
		string(providerjournal.StateDeleting):    0,
		string(providerjournal.StateWarmReady):   0,
		string(providerjournal.StateWarmClaimed): 0,
	}
	preemptions := 0
	oldestStateAge := make(map[string]int64, len(byState))
	for _, lease := range journal.Leases {
		byState[string(lease.State)]++
		age := int64(now.Sub(lease.UpdatedAt).Seconds())
		if age > oldestStateAge[string(lease.State)] {
			oldestStateAge[string(lease.State)] = age
		}
		if lease.PreemptedBy != "" {
			preemptions++
		}
	}
	return JournalSummary{
		SchemaVersion:         journal.SchemaVersion,
		Generation:            journal.Generation,
		UpdatedAt:             journal.UpdatedAt,
		Leases:                len(journal.Leases),
		Claims:                len(journal.Claims),
		WarmPreemptions:       preemptions,
		WarmPreemptionsTotal:  journal.WarmPreemptionsTotal,
		ByState:               byState,
		OldestStateAgeSeconds: oldestStateAge,
	}
}

func validateInstanceNames(names []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "\r\n\x00") {
			return nil, fmt.Errorf("unsafe or empty visible instance name")
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("duplicate visible instance name %q", name)
		}
		result[name] = struct{}{}
	}
	return result, nil
}

func safeError(source string, err error) string {
	message := strings.TrimSpace(string(workerdiagnostics.Redact([]byte(err.Error()))))
	message = strings.Map(func(character rune) rune {
		if character == '\r' || character == '\n' || character == '\x00' {
			return ' '
		}
		return character
	}, message)
	if len(message) > 512 {
		message = message[:512]
	}
	return source + ": " + message
}
