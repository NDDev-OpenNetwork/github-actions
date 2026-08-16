package fleetobserve

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
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
	gauge(&output, "gha_fleet_host_root_available_bytes", "Available bytes on the root filesystem.", float64(snapshot.Host.RootFilesystem.AvailableMiB)*1024*1024)
	gauge(&output, "gha_fleet_host_root_free_percent", "Free block percentage on the root filesystem.", float64(snapshot.Host.RootFilesystem.FreePercent))
	gauge(&output, "gha_fleet_host_root_free_inodes_percent", "Free inode percentage on the root filesystem.", float64(snapshot.Host.RootFilesystem.FreeInodesPercent))
	gauge(&output, "gha_fleet_host_kvm_present", "Whether /dev/kvm is present in the observer service.", boolFloat(snapshot.Host.KVM.Present))
	gauge(&output, "gha_fleet_host_kvm_accessible", "Whether /dev/kvm is accessible to the observer service.", boolFloat(snapshot.Host.KVM.Accessible))
	gauge(&output, "gha_fleet_legacy_runner_listeners", "Legacy runner listeners retained during migration.", float64(snapshot.Host.LegacyRunners.Listeners))
	gauge(&output, "gha_fleet_legacy_runner_workers", "Active legacy runner workers observed during migration.", float64(snapshot.Host.LegacyRunners.Workers))

	labeledGaugeHeader(&output, "gha_fleet_pool_pilot_ready", "Whether a cold worker in the pool passes current host admission preflight.")
	labeledGaugeHeader(&output, "gha_fleet_pool_findings", "Current preflight finding count by pool and severity.")
	pools := append([]PoolStatus(nil), snapshot.Pools...)
	sort.SliceStable(pools, func(i, j int) bool { return pools[i].Name < pools[j].Name })
	for _, pool := range pools {
		metric(&output, "gha_fleet_pool_pilot_ready", map[string]string{"pool": pool.Name}, boolFloat(pool.PilotReady))
		for _, finding := range []struct {
			severity string
			count    int
		}{
			{severity: "blocker", count: pool.Blockers},
			{severity: "warning", count: pool.Warnings},
			{severity: "info", count: pool.Info},
		} {
			metric(&output, "gha_fleet_pool_findings", map[string]string{"pool": pool.Name, "severity": finding.severity}, float64(finding.count))
		}
	}

	gauge(&output, "gha_fleet_provider_journal_generation", "Current fsync-backed provider journal generation.", float64(snapshot.Journal.Generation))
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
	gauge(&output, "gha_fleet_queue_journal_generation", "Current fsync-backed pre-AcquireJobs queue journal generation.", float64(snapshot.Queue.Generation))
	gauge(&output, "gha_fleet_queue_intents_stored", "Queue intents stored in the current journal generation.", float64(snapshot.Queue.Stored))
	gauge(&output, "gha_fleet_queue_intents_active", "Unexpired queue intents owned by central admission.", float64(snapshot.Queue.Active))
	gauge(&output, "gha_fleet_queue_intents_expired", "Expired queue intents awaiting the next writer cleanup transaction.", float64(snapshot.Queue.Expired))
	gauge(&output, "gha_fleet_queue_intents_in_flight", "Acquiring, acquired, assigned or running queue intents.", float64(snapshot.Queue.InFlight))
	gauge(&output, "gha_fleet_queue_oldest_age_seconds", "Age of the oldest active GitHub queue intent.", float64(snapshot.Queue.OldestQueueAgeSeconds))
	gauge(&output, "gha_fleet_queue_uncovered_running", "Running queue intents without a durable created or warm-claimed execution lease.", float64(snapshot.Queue.UncoveredRunning))
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
	gauge(&output, "gha_fleet_incus_visible_instances", "Instances visible in the restricted Incus project.", float64(snapshot.Incus.VisibleInstances))
	gauge(&output, "gha_fleet_incus_orphan_instances", "Visible Incus instances without a created or deleting journal lease.", float64(snapshot.Incus.OrphanInstances))
	gauge(&output, "gha_fleet_journal_missing_instances", "Created or deleting journal leases without a visible Incus instance.", float64(snapshot.Incus.MissingInstances))

	gauge(&output, "gha_fleet_diagnostic_bundles", "Private retained worker diagnostic bundle count.", float64(snapshot.Diagnostics.Bundles))
	gauge(&output, "gha_fleet_diagnostic_bytes", "Private retained worker diagnostic bundle bytes.", float64(snapshot.Diagnostics.Bytes))
	gauge(&output, "gha_fleet_diagnostic_oldest_age_seconds", "Age of the oldest retained worker diagnostic bundle.", float64(snapshot.Diagnostics.OldestAgeSeconds))
	gauge(&output, "gha_fleet_diagnostic_export_source_bundles", "Diagnostic bundles observed by the RustFS exporter.", float64(snapshot.DiagnosticExport.SourceBundles))
	gauge(&output, "gha_fleet_diagnostic_export_exported_bundles", "Diagnostic bundles durably confirmed in RustFS.", float64(snapshot.DiagnosticExport.ExportedBundles))
	gauge(&output, "gha_fleet_diagnostic_export_pending_bundles", "Diagnostic bundles pending durable RustFS confirmation.", float64(snapshot.DiagnosticExport.PendingBundles))
	gauge(&output, "gha_fleet_diagnostic_export_consecutive_failures", "Consecutive diagnostic exporter failures.", float64(snapshot.DiagnosticExport.ConsecutiveFailures))
	gauge(&output, "gha_fleet_diagnostic_export_source_bytes", "Diagnostic spool bytes observed by the RustFS exporter.", float64(snapshot.DiagnosticExport.SourceBytes))
	gauge(&output, "gha_fleet_diagnostic_export_exported_bytes", "Diagnostic bytes durably confirmed in RustFS.", float64(snapshot.DiagnosticExport.ExportedBytes))
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
	gauge(&output, "gha_fleet_diagnostic_export_observed_age_seconds", "Age of the latest diagnostic exporter observation, or -1 when unavailable.", diagnosticTimestampAge(observedAt, observedErr, now))
	gauge(&output, "gha_fleet_diagnostic_export_last_success_age_seconds", "Age of the latest successful diagnostic export, or -1 when unavailable.", diagnosticTimestampAge(lastSuccessAt, lastSuccessErr, now))

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
