package fleetobserve

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/psimetrics"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
)

func RenderPrometheus(snapshot Snapshot, now time.Time, maxStaleness time.Duration) string {
	now = now.UTC()
	age := now.Sub(snapshot.CapturedAt)
	future := age < 0
	if age < 0 {
		age = 0
	}
	stale := future || maxStaleness <= 0 || age > maxStaleness
	collectionSuccess := len(snapshot.CollectionErrors) == 0
	observerUp := collectionSuccess && !stale

	var output strings.Builder
	gauge(&output, "gha_fleet_observer_up", "Whether the latest observer sample is fresh and complete.", boolFloat(observerUp))
	gauge(&output, "gha_fleet_platform_healthy", "Whether services and exact provider inventory are healthy.", boolFloat(snapshot.Healthy && !stale))
	gauge(&output, "gha_fleet_snapshot_age_seconds", "Age of the latest observer sample.", age.Seconds())
	gauge(&output, "gha_fleet_snapshot_stale", "Whether the latest observer sample exceeded its freshness budget.", boolFloat(stale))
	gauge(&output, "gha_fleet_collection_errors", "Number of essential source collection errors.", float64(len(snapshot.CollectionErrors)))

	gauge(&output, "gha_fleet_host_physical_cpu_units", "Physical or provider-guaranteed CPU units on the host.", float64(snapshot.Host.CPU.PhysicalCores))
	gauge(&output, "gha_fleet_host_load1", "One-minute host load average.", snapshot.Host.CPU.Load1)
	gauge(&output, "gha_fleet_host_memory_total_bytes", "Total host memory in bytes.", float64(snapshot.Host.Memory.TotalMiB)*1024*1024)
	gauge(&output, "gha_fleet_host_memory_available_bytes", "Available host memory in bytes.", float64(snapshot.Host.Memory.AvailableMiB)*1024*1024)
	counter(&output, "gha_fleet_host_oom_kills_total", "Kernel OOM kills observed since host boot.", float64(snapshot.Host.Memory.OOMKillsTotal))
	output.WriteString(psimetrics.Render(snapshot.Host.Pressure))
	gauge(&output, "gha_fleet_host_root_available_bytes", "Available bytes on the root filesystem.", float64(snapshot.Host.RootFilesystem.AvailableMiB)*1024*1024)
	gauge(&output, "gha_fleet_host_root_free_percent", "Free block percentage on the root filesystem.", float64(snapshot.Host.RootFilesystem.FreePercent))
	gauge(&output, "gha_fleet_host_root_free_inodes_percent", "Free inode percentage on the root filesystem.", float64(snapshot.Host.RootFilesystem.FreeInodesPercent))
	gauge(&output, "gha_fleet_host_kvm_present", "Whether /dev/kvm is present in the observer service.", boolFloat(snapshot.Host.KVM.Present))
	gauge(&output, "gha_fleet_host_kvm_accessible", "Whether /dev/kvm is accessible to the observer service.", boolFloat(snapshot.Host.KVM.Accessible))
	gauge(&output, "gha_fleet_legacy_runner_listeners", "Legacy runner listeners retained during migration.", float64(snapshot.Host.LegacyRunners.Listeners))
	gauge(&output, "gha_fleet_legacy_runner_workers", "Active legacy runner workers observed during migration.", float64(snapshot.Host.LegacyRunners.Workers))

	labeledGaugeHeader(&output, "gha_fleet_pool_container_admission_ready", "Whether the current container pool has healthy control-plane, provider inventory, resource journal and diagnostic WAL inputs for admission.")
	pools := append([]PoolStatus(nil), snapshot.Pools...)
	sort.SliceStable(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	for _, pool := range pools {
		metric(&output, "gha_fleet_pool_container_admission_ready", map[string]string{"pool": pool.Name}, boolFloat(pool.ContainerAdmissionReady))
	}

	gauge(&output, "gha_fleet_provider_journal_generation", "Current fsync-backed provider journal generation.", float64(snapshot.Journal.Generation))
	gauge(&output, "gha_fleet_provider_retry_journal_generation", "Current fsync-backed provider-create retry journal generation.", float64(snapshot.ProviderRetry.Generation))
	gauge(&output, "gha_fleet_provider_retry_records", "Stored durable provider-create retry records, including retained completed backoff history.", float64(snapshot.ProviderRetry.Records))
	gauge(&output, "gha_fleet_provider_retry_reservations", "Active pre-job instance reservations bound to stable queue-intent retry identities.", float64(snapshot.ProviderRetry.Reservations))
	gauge(&output, "gha_fleet_provider_retry_deferred_records", "Provider-create retry records whose backoff or terminal circuit is active now.", float64(snapshot.ProviderRetry.DeferredRecords))
	gauge(&output, "gha_fleet_provider_terminal_circuits", "Terminal scale-set provider-create circuits currently blocking new capacity.", float64(snapshot.ProviderRetry.TerminalCircuits))
	gauge(&output, "gha_fleet_provider_terminal_circuit_oldest_age_seconds", "Age of the oldest terminal scale-set provider-create circuit.", float64(snapshot.ProviderRetry.OldestTerminalAgeSeconds))
	gauge(&output, "gha_fleet_provider_retry_next_delay_seconds", "Delay until the earliest deferred provider-create retry may proceed.", float64(snapshot.ProviderRetry.NextRetryDelaySeconds))
	gauge(&output, "gha_fleet_shared_capacity_saturated", "Whether a provider refusal has proven the shared measured-capacity domain saturated.", boolFloat(snapshot.ProviderRetry.SharedCapacitySaturated))
	gauge(&output, "gha_fleet_shared_capacity_probe_owned", "Whether one oldest waiting domain durably owns the next shared-capacity probe.", boolFloat(snapshot.ProviderRetry.SharedCapacityProbeOwned))
	gauge(&output, "gha_fleet_shared_capacity_probe_active", "Whether the shared-capacity owner currently holds the bounded provider-call lease.", boolFloat(snapshot.ProviderRetry.SharedCapacityProbeActive))
	gauge(&output, "gha_fleet_shared_capacity_waiters", "Scale-set capacity domains waiting behind the shared saturation record.", float64(snapshot.ProviderRetry.SharedCapacityWaiters))
	gauge(&output, "gha_fleet_shared_capacity_state_age_seconds", "Age of the current shared-capacity decision.", float64(snapshot.ProviderRetry.SharedCapacityAgeSeconds))
	labeledGaugeHeader(&output, "gha_fleet_shared_capacity_wake_reason", "Current shared-capacity transition by bounded wake reason.")
	for _, reason := range []string{"capacity-refused", "worker-deleted", "probe-leased", "probe-succeeded", "none"} {
		active := snapshot.ProviderRetry.SharedCapacityWakeReason == reason
		if snapshot.ProviderRetry.SharedCapacityWakeReason == "" && reason == "none" {
			active = true
		}
		metric(&output, "gha_fleet_shared_capacity_wake_reason", map[string]string{"reason": reason}, boolFloat(active))
	}
	labeledGaugeHeader(&output, "gha_fleet_provider_retry_records_by_error_class", "Provider-create retry records by bounded error class.")
	for _, class := range []string{"capacity", "identity", "intent", "provider", "timeout", "unknown"} {
		metric(&output, "gha_fleet_provider_retry_records_by_error_class", map[string]string{"error_class": class}, float64(snapshot.ProviderRetry.ByErrorClass[class]))
	}
	labeledGaugeHeader(&output, "gha_fleet_provider_retry_deferred_records_by_error_class", "Currently deferred provider-create retry records by bounded error class.")
	for _, class := range []string{"capacity", "identity", "intent", "provider", "timeout", "unknown"} {
		metric(&output, "gha_fleet_provider_retry_deferred_records_by_error_class", map[string]string{"error_class": class}, float64(snapshot.ProviderRetry.DeferredByErrorClass[class]))
	}
	gauge(&output, "gha_fleet_provider_journal_leases", "Total provider admission leases.", float64(snapshot.Journal.Leases))
	gauge(&output, "gha_fleet_provider_journal_claims", "Durable GARM job-to-warm-VM claims.", float64(snapshot.Journal.Claims))
	gauge(&output, "gha_fleet_provider_warm_preemptions", "Warm VM teardown reservations atomically owned by admitted cold jobs.", float64(snapshot.Journal.WarmPreemptions))
	counter(&output, "gha_fleet_provider_warm_preemptions_total", "Warm VM teardown reservations durably committed since journal creation.", float64(snapshot.Journal.WarmPreemptionsTotal))
	labeledGaugeHeader(&output, "gha_fleet_provider_journal_leases_by_state", "Provider admission leases by bounded lifecycle state.")
	for _, state := range []providerjournal.LeaseState{
		providerjournal.StateAdmitted,
		providerjournal.StateCreated,
		providerjournal.StateDeleting,
		providerjournal.StateWarmReady,
		providerjournal.StateWarmClaimed,
	} {
		metric(&output, "gha_fleet_provider_journal_leases_by_state", map[string]string{"state": string(state)}, float64(snapshot.Journal.ByState[string(state)]))
	}
	labeledGaugeHeader(&output, "gha_fleet_provider_lease_oldest_state_age_seconds", "Age since the oldest provider lease last progressed in its current state.")
	for _, state := range []providerjournal.LeaseState{
		providerjournal.StateAdmitted, providerjournal.StateCreated, providerjournal.StateDeleting,
		providerjournal.StateWarmReady, providerjournal.StateWarmClaimed,
	} {
		metric(&output, "gha_fleet_provider_lease_oldest_state_age_seconds", map[string]string{"state": string(state)}, float64(snapshot.Journal.OldestStateAgeSeconds[string(state)]))
	}
	gauge(&output, "gha_fleet_queue_journal_generation", "Current fsync-backed pre-AcquireJobs queue journal generation.", float64(snapshot.Queue.Generation))
	gauge(&output, "gha_fleet_queue_intents_stored", "Queue intents stored in the current journal generation.", float64(snapshot.Queue.Stored))
	gauge(&output, "gha_fleet_queue_intents_active", "Unexpired queue intents owned by central admission.", float64(snapshot.Queue.Active))
	gauge(&output, "gha_fleet_queue_intents_expired", "Expired queue intents awaiting the next writer cleanup transaction.", float64(snapshot.Queue.Expired))
	gauge(&output, "gha_fleet_queue_terminal_tombstones", "Completed job identities retained to suppress delayed lifecycle redelivery.", float64(snapshot.Queue.TerminalJobs))
	gauge(&output, "gha_fleet_queue_terminal_tombstone_next_expiry_seconds", "Seconds until the next bounded terminal job tombstone expires.", float64(snapshot.Queue.TerminalNextExpirySeconds))
	gauge(&output, "gha_fleet_queue_intents_in_flight", "Acquiring, acquired, assigned or running queue intents.", float64(snapshot.Queue.InFlight))
	gauge(&output, "gha_fleet_queue_oldest_age_seconds", "Age of the oldest active GitHub queue intent.", float64(snapshot.Queue.OldestQueueAgeSeconds))
	gauge(&output, "gha_fleet_queue_uncovered_running", "Running queue intents whose runner holds no provider execution lease or claim.", float64(snapshot.Queue.UncoveredRunning))
	// The total is always emitted so the stream exists from the first scrape:
	// an alert can only be reconciled over a stream that exists, and a rule
	// that guards a suppression must be installable before the first hold.
	gauge(&output, "gha_fleet_visibility_held_out_members", "Cluster members deliberately held out by a drain while offline; inventory gaps are unattributable and suppressed while this is above zero.", float64(len(snapshot.HeldOutMembers)))
	labeledGaugeHeader(&output, "gha_fleet_visibility_degraded", "One row per held-out member, for attribution.")
	for _, member := range snapshot.HeldOutMembers {
		metric(&output, "gha_fleet_visibility_degraded", map[string]string{"member": member}, 1)
	}
	gauge(&output, "gha_fleet_queue_uncovered_running_beyond_grace", "Running queue intents still uncovered after the broker reclaim window, which is the gap nothing is coming to close.", float64(snapshot.Queue.UncoveredRunningBeyondGrace))
	gauge(&output, "gha_fleet_queue_uncovered_running_oldest_age_seconds", "Age of the oldest running queue intent whose runner holds no execution lease.", float64(snapshot.Queue.OldestUncoveredRunningAgeSeconds))
	gauge(&output, "gha_fleet_queue_running_without_runner_identity", "Running queue intents written before or without an exact JobStarted runner identity.", float64(snapshot.Queue.RunningWithoutRunnerIdentity))
	gauge(&output, "gha_fleet_queue_unbound_repository", "Active intents still scoped only to their forge account before JobAvailable binding.", float64(snapshot.Queue.UnboundRepository))
	gauge(&output, "gha_fleet_queue_unbound_repository_beyond_grace", "Active intents whose repository identity remains unbound after the bounded two-minute correlation convergence window.", float64(snapshot.Queue.UnboundRepositoryBeyondGrace))
	gauge(&output, "gha_fleet_queue_missing_runner_request_id", "Active intents with incomplete correlation before their execution path is authoritative.", float64(snapshot.Queue.MissingRunnerRequestID))
	gauge(&output, "gha_fleet_queue_direct_jit_without_runner_request_id", "Running direct-JIT intents whose authoritative UUID, run and runner identities make an AcquireJobs runner request ID inapplicable.", float64(snapshot.Queue.DirectJITWithoutRequestID))
	gauge(&output, "gha_fleet_queue_missing_workflow_run_id", "Active intents awaiting their GitHub workflow run identity.", float64(snapshot.Queue.MissingWorkflowRunID))
	gauge(&output, "gha_fleet_queue_missing_workflow_run_id_beyond_grace", "Active intents whose GitHub workflow run identity remains missing after the bounded two-minute correlation convergence window.", float64(snapshot.Queue.MissingWorkflowRunIDBeyondGrace))
	gauge(&output, "gha_fleet_queue_running_missing_github_runner_id", "Running intents without GitHub's numeric runner identity.", float64(snapshot.Queue.RunningMissingGitHubRunnerID))
	labeledGaugeHeader(&output, "gha_fleet_queue_intents_by_state", "Active central queue intents by bounded state.")
	for _, state := range []queueintent.State{
		queueintent.StateQueued,
		queueintent.StateAcquiring,
		queueintent.StateAcquired,
		queueintent.StateAssigned,
		queueintent.StateRunning,
	} {
		metric(&output, "gha_fleet_queue_intents_by_state", map[string]string{"state": string(state)}, float64(snapshot.Queue.ByState[string(state)]))
	}
	labeledGaugeHeader(&output, "gha_fleet_queue_intent_oldest_state_age_seconds", "Age since the oldest active queue intent last progressed in its current state.")
	for _, state := range []queueintent.State{
		queueintent.StateQueued, queueintent.StateAcquiring, queueintent.StateAcquired,
		queueintent.StateAssigned, queueintent.StateRunning,
	} {
		metric(&output, "gha_fleet_queue_intent_oldest_state_age_seconds", map[string]string{"state": string(state)}, float64(snapshot.Queue.OldestStateAgeSeconds[string(state)]))
	}
	labeledGaugeHeader(&output, "gha_fleet_queue_intents_by_priority", "Active central queue intents by bounded priority lane.")
	for priority := range 4 {
		metric(&output, "gha_fleet_queue_intents_by_priority", map[string]string{"priority": strconv.Itoa(priority)}, float64(snapshot.Queue.ByPriority[priority]))
	}
	labeledGaugeHeader(&output, "gha_fleet_queue_intents_by_scale_set", "Active central queue intents by configured scale set.")
	scaleSets := make([]string, 0, len(snapshot.Queue.ByScaleSet))
	for scaleSet := range snapshot.Queue.ByScaleSet {
		scaleSets = append(scaleSets, scaleSet)
	}
	sort.Strings(scaleSets)
	for _, scaleSet := range scaleSets {
		metric(&output, "gha_fleet_queue_intents_by_scale_set", map[string]string{"scale_set": scaleSet}, float64(snapshot.Queue.ByScaleSet[scaleSet]))
	}
	// The wait, not the state clock. gha_fleet_queue_intent_oldest_state_age_seconds
	// resets when an assigned intent returns to queued, so it has a ceiling near
	// the assignment timeout and cannot carry a wait threshold.
	gauge(&output, "gha_fleet_queue_oldest_queued_wait_seconds", "Longest wait since GitHub queued an intent that is still queued.", float64(snapshot.Queue.OldestQueuedWaitSeconds))
	// Read beside the wait, never alone. While this is non-zero the wait above
	// is a lower bound for that many intents, so "under the threshold" and
	// "under the threshold with nothing to measure by" stay distinguishable.
	gauge(&output, "gha_fleet_queue_queued_without_first_stamp", "Waiting intents with no immutable first-queued stamp, whose wait is therefore a lower bound.", float64(snapshot.Queue.QueuedWithoutFirstStamp))
	labeledGaugeHeader(&output, "gha_fleet_queue_oldest_queued_wait_seconds_by_scale_set", "Longest wait since GitHub queued a still-queued intent, per configured scale set.")
	for _, scaleSet := range scaleSets {
		metric(&output, "gha_fleet_queue_oldest_queued_wait_seconds_by_scale_set", map[string]string{"scale_set": scaleSet}, float64(snapshot.Queue.OldestQueuedWaitSecondsByScaleSet[scaleSet]))
	}
	gauge(&output, "gha_fleet_incus_visible_instances", "Instances visible in the restricted Incus project.", float64(snapshot.Incus.VisibleInstances))
	gauge(&output, "gha_fleet_incus_visible_maintenance_instances", "Visible exact image builder or smoke instances; observable maintenance capacity, never GitHub job runners.", float64(snapshot.Incus.VisibleMaintenanceInstances))
	gauge(&output, "gha_fleet_incus_orphan_instances", "Visible Incus instances without an admitted, created, deleting or warm journal lease.", float64(snapshot.Incus.OrphanInstances))
	gauge(&output, "gha_fleet_incus_orphan_instances_within_grace", "Instances visible in Incus whose lease has not been journaled yet, inside the create grace.", float64(snapshot.Incus.OrphanInstancesWithinGrace))
	gauge(&output, "gha_fleet_journal_missing_instances", "Created or deleting journal leases without a visible Incus instance.", float64(snapshot.Incus.MissingInstances))
	gauge(&output, "gha_fleet_journal_missing_deleting_within_grace", "Absent deleting runner leases inside the bounded Incus inventory convergence grace.", float64(snapshot.Incus.MissingDeletingWithinGrace))
	gauge(&output, "gha_fleet_journal_missing_created_within_grace", "Created runner leases first observed absent from Incus inside the bounded terminal inventory convergence grace.", float64(snapshot.Incus.MissingCreatedWithinGrace))
	gauge(&output, "gha_fleet_journal_missing_created_within_grace_oldest_age_seconds", "Age since first observation of the oldest absent created lease still inside convergence grace.", float64(snapshot.Incus.OldestMissingCreatedAgeSecs))
	gauge(&output, "gha_fleet_journal_missing_maintenance_instances", "Completed image-maintenance leases awaiting the next provider inventory reconciliation.", float64(snapshot.Incus.MissingMaintenanceInstances))
	gauge(&output, "gha_fleet_provider_created_without_running_identity", "Created provider leases not named by an active running queue intent; alert only after bounded pre-start or teardown grace.", float64(snapshot.Journal.CreatedWithoutRunningIdentity))
	gauge(&output, "gha_fleet_provider_created_without_running_identity_oldest_age_seconds", "Age from admission of the oldest created provider lease not named by an active running queue intent.", float64(snapshot.Journal.OldestCreatedWithoutRunningIdentityAgeSeconds))

	gauge(&output, "gha_fleet_diagnostic_bundles", "Private retained worker diagnostic bundle count.", float64(snapshot.Diagnostics.Bundles))
	gauge(&output, "gha_fleet_diagnostic_bytes", "Private retained worker diagnostic bundle bytes.", float64(snapshot.Diagnostics.Bytes))
	gauge(&output, "gha_fleet_diagnostic_oldest_age_seconds", "Age of the oldest retained worker diagnostic bundle.", float64(snapshot.Diagnostics.OldestAgeSeconds))
	gauge(&output, "gha_fleet_diagnostic_export_source_bundles", "Diagnostic bundles remaining in the local durable export WAL.", float64(snapshot.DiagnosticExport.SourceBundles))
	gauge(&output, "gha_fleet_diagnostic_export_exported_bundles", "Diagnostic bundles durably confirmed and drained by the latest exporter run.", float64(snapshot.DiagnosticExport.ExportedBundles))
	gauge(&output, "gha_fleet_diagnostic_export_pending_bundles", "Diagnostic bundles pending durable RustFS confirmation.", float64(snapshot.DiagnosticExport.PendingBundles))
	gauge(&output, "gha_fleet_diagnostic_export_consecutive_failures", "Consecutive diagnostic exporter failures.", float64(snapshot.DiagnosticExport.ConsecutiveFailures))
	gauge(&output, "gha_fleet_diagnostic_export_source_bytes", "Diagnostic bytes remaining in the local durable export WAL.", float64(snapshot.DiagnosticExport.SourceBytes))
	gauge(&output, "gha_fleet_diagnostic_export_exported_bytes", "Diagnostic bytes durably confirmed and drained by the latest exporter run.", float64(snapshot.DiagnosticExport.ExportedBytes))
	gauge(&output, "gha_fleet_diagnostic_export_scanned_bundles", "Diagnostic bundles scanned in the latest bounded exporter batch.", float64(snapshot.DiagnosticExport.ScannedBundles))
	gauge(&output, "gha_fleet_diagnostic_export_deleted_bundles", "Remotely confirmed diagnostic bundles safely deleted from the local WAL in the latest run.", float64(snapshot.DiagnosticExport.DeletedBundles))
	labeledGaugeHeader(&output, "gha_fleet_diagnostic_export_sync_state", "Current bounded reconciliation state between the local diagnostic spool and exporter status.")
	for _, state := range []string{
		diagnosticExportSyncUnavailable,
		diagnosticExportSyncSynchronized,
		diagnosticExportSyncGrace,
		diagnosticExportSyncInvalid,
	} {
		metric(&output, "gha_fleet_diagnostic_export_sync_state", map[string]string{"state": state}, boolFloat(snapshot.DiagnosticExportSync.State == state))
	}
	gauge(&output, "gha_fleet_diagnostic_export_sync_grace_remaining_seconds", "Seconds remaining in the bounded diagnostic export convergence grace.", float64(snapshot.DiagnosticExportSync.GraceRemainingSeconds))
	gauge(&output, "gha_fleet_diagnostic_export_local_bundle_delta", "Signed local bundle count minus the exporter source view.", float64(snapshot.DiagnosticExportSync.LocalBundleDelta))
	gauge(&output, "gha_fleet_diagnostic_export_local_byte_delta", "Signed local diagnostic bytes minus the exporter source view.", float64(snapshot.DiagnosticExportSync.LocalByteDelta))
	observedAt, observedErr := snapshot.DiagnosticExport.ObservedTime()
	lastSuccessAt, lastSuccessErr := snapshot.DiagnosticExport.LastSuccessTime()
	lastProgressAt, lastProgressErr := snapshot.DiagnosticExport.LastProgressTime()
	lastFullSyncAt, lastFullSyncErr := snapshot.DiagnosticExport.LastFullSyncTime()
	gauge(&output, "gha_fleet_diagnostic_export_observed_age_seconds", "Age of the latest diagnostic exporter observation, or -1 when unavailable.", diagnosticTimestampAge(observedAt, observedErr, now))
	gauge(&output, "gha_fleet_diagnostic_export_last_success_age_seconds", "Age of the latest successful diagnostic export, or -1 when unavailable.", diagnosticTimestampAge(lastSuccessAt, lastSuccessErr, now))
	gauge(&output, "gha_fleet_diagnostic_export_last_progress_age_seconds", "Age of the latest confirmed local-WAL drain progress, or -1 when unavailable.", diagnosticTimestampAge(lastProgressAt, lastProgressErr, now))
	gauge(&output, "gha_fleet_diagnostic_export_last_full_sync_age_seconds", "Age of the latest zero-backlog full synchronization, or -1 when unavailable.", diagnosticTimestampAge(lastFullSyncAt, lastFullSyncErr, now))

	labeledGaugeHeader(&output, "gha_fleet_service_up", "Whether a required fleet systemd unit is active.")
	services := append([]ServiceStatus(nil), snapshot.Services...)
	sort.SliceStable(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	for _, service := range services {
		metric(&output, "gha_fleet_service_up", map[string]string{"service": service.Name}, boolFloat(service.Active))
	}
	return output.String()
}

func diagnosticTimestampAge(timestamp time.Time, err error, now time.Time) float64 {
	if err != nil || timestamp.IsZero() || timestamp.After(now) {
		return -1
	}
	return now.Sub(timestamp).Seconds()
}

func gauge(output *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, formatFloat(value))
}

func counter(output *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s counter\n%s %s\n", name, help, name, name, formatFloat(value))
}

func labeledGaugeHeader(output *strings.Builder, name, help string) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
}

func labeledCounterHeader(output *strings.Builder, name, help string) {
	fmt.Fprintf(output, "# HELP %s %s\n# TYPE %s counter\n", name, help, name)
}

func metric(output *strings.Builder, name string, labels map[string]string, value float64) {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output.WriteString(name)
	if len(keys) != 0 {
		output.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				output.WriteByte(',')
			}
			fmt.Fprintf(output, `%s="%s"`, key, escapeLabel(labels[key]))
		}
		output.WriteByte('}')
	}
	output.WriteByte(' ')
	output.WriteString(formatFloat(value))
	output.WriteByte('\n')
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
