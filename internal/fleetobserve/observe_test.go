package fleetobserve

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerretry"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

var observationTime = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func testPlatform(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func healthyHost() hostprobe.Snapshot {
	return hostprobe.Snapshot{
		SchemaVersion: 1,
		CapturedAt:    observationTime.Format(time.RFC3339),
		Hostname:      "example-runner-1",
		OperatingSystem: hostprobe.OperatingSystem{
			ID: "ubuntu", VersionID: "24.04", Architecture: "x86_64",
		},
		Virtualization: "kvm",
		CPU:            hostprobe.CPU{PhysicalCores: 8, Logical: 8, Sockets: 1, ThreadsPerCore: 1, NUMANodes: 1, Load1: 1},
		Memory:         hostprobe.Memory{TotalMiB: 31744, AvailableMiB: 30000},
		RootFilesystem: hostprobe.Filesystem{TotalMiB: 640000, AvailableMiB: 320000, FreePercent: 50, FreeInodesPercent: 80},
		KVM:            hostprobe.KVM{Present: true, Accessible: true, Nested: true},
		Maintenance:    hostprobe.Maintenance{SystemState: "running"},
		Software:       hostprobe.Software{Incus: hostprobe.SoftwareVersion{Present: true, Version: "6.0.0"}},
		LegacyRunners:  hostprobe.LegacyRunners{Listeners: 12},
	}
}

func testJournal() providerjournal.Journal {
	return providerjournal.Journal{
		SchemaVersion: providerjournal.SchemaVersion,
		Generation:    3,
		UpdatedAt:     observationTime.Add(-time.Second),
		Leases: map[string]providerjournal.Lease{
			"runner-one": {
				InstanceName: "runner-one", ControllerID: "controller", PoolID: "pool",
				PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 10240,
				ImageFingerprint: strings.Repeat("a", 64), State: providerjournal.StateCreated,
				AdmittedAt: observationTime.Add(-time.Minute), UpdatedAt: observationTime.Add(-time.Second),
				ExpiresAt: observationTime.Add(time.Minute),
			},
		},
		Claims: map[string]providerjournal.Claim{},
	}
}

func healthyCollector(t *testing.T) Collector {
	t.Helper()
	return Collector{
		Config:             testPlatform(t),
		DiagnosticMaxBytes: 1024 * 1024 * 1024,
		Host: func(context.Context) (hostprobe.Snapshot, error) {
			return healthyHost(), nil
		},
		Journal: func(context.Context) (providerjournal.Journal, error) {
			return testJournal(), nil
		},
		ProviderRetry: func(time.Time) (providerretry.Snapshot, error) {
			return providerretry.Snapshot{Generation: 1, ByErrorClass: map[string]int{}}, nil
		},
		Queue: func(context.Context) (queueintent.Snapshot, error) {
			return queueintent.Snapshot{Generation: 1}, nil
		},
		Instances: func(context.Context) ([]string, error) {
			return []string{"runner-one"}, nil
		},
		Diagnostics: func(time.Time) (workerdiagnostics.SpoolStats, error) {
			return workerdiagnostics.SpoolStats{Bundles: 2, Bytes: 4096, OldestAgeSeconds: 60}, nil
		},
		Export: func() (diagnosticexport.Status, error) {
			return diagnosticexport.Status{
				SchemaVersion: 1, DeploymentStage: "canary",
				ObservedAt:    observationTime.Format(time.RFC3339Nano),
				LastSuccessAt: observationTime.Format(time.RFC3339Nano),
				SourceBundles: 2, ExportedBundles: 2, SourceBytes: 4096, ExportedBytes: 4096,
			}, nil
		},
		Service: func(_ context.Context, _ string) (string, error) {
			return "active", nil
		},
		Now: func() time.Time { return observationTime },
	}
}

func TestCollectorBuildsHealthyExactInventorySnapshot(t *testing.T) {
	collector := healthyCollector(t)
	if err := collector.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.SchemaVersion != SchemaVersion || !snapshot.Healthy || len(snapshot.CollectionErrors) != 0 {
		t.Fatalf("unhealthy snapshot: %#v", snapshot)
	}
	if snapshot.Journal.Leases != 1 || snapshot.Journal.ByState["created"] != 1 {
		t.Fatalf("journal summary: %#v", snapshot.Journal)
	}
	if snapshot.Incus.VisibleInstances != 1 || snapshot.Incus.OrphanInstances != 0 || snapshot.Incus.MissingInstances != 0 {
		t.Fatalf("Incus summary: %#v", snapshot.Incus)
	}
	if len(snapshot.Pools) != len(collector.Config.Pools) || len(snapshot.Services) != len(ServiceNames()) || snapshot.Diagnostics.Bundles != 2 ||
		snapshot.DiagnosticExport.ExportedBundles != 2 ||
		snapshot.DiagnosticExportSync.State != diagnosticExportSyncSynchronized {
		t.Fatalf("incomplete snapshot: %#v", snapshot)
	}
}

func TestCollectorFailsHealthForTerminalProviderCircuit(t *testing.T) {
	collector := healthyCollector(t)
	collector.ProviderRetry = func(time.Time) (providerretry.Snapshot, error) {
		return providerretry.Snapshot{Generation: 2, TerminalCircuits: 1, ByErrorClass: map[string]int{"provider": 1}}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || !slices.Contains(snapshot.CollectionErrors, "provider retry: terminal create circuit blocks a scale set") {
		t.Fatalf("terminal provider circuit was accepted as healthy: %#v", snapshot)
	}
}

func TestQueueHostOwnsCentralDiagnosticExporterHealth(t *testing.T) {
	collector := healthyCollector(t)
	queueConfig, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	collector.Config = queueConfig
	if err := collector.Validate(); err != nil {
		t.Fatal(err)
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || len(snapshot.Services) != 7 {
		t.Fatalf("queue host snapshot = %#v", snapshot)
	}
	seen := make(map[string]bool, len(snapshot.Services))
	for _, service := range snapshot.Services {
		seen[service.Name] = true
	}
	for _, name := range []string{"garm", "gha-fleet-gateway", "gha-cache-broker", "gha-diagnostic-exporter.service", "gha-diagnostic-exporter.timer", "gha-services-rustfs-route.timer", "otelcol-fleet"} {
		if !seen[name] {
			t.Fatalf("queue host omitted required service %q", name)
		}
	}
	for _, name := range []string{"gha-rustfs", "gha-zot", "gha-warm-pool.timer"} {
		if seen[name] {
			t.Fatalf("queue host required compute service %q", name)
		}
	}
}

func TestServicesHostRequiresDiagnosticExporterSource(t *testing.T) {
	collector := healthyCollector(t)
	queueConfig, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	collector.Config = queueConfig
	collector.Export = nil
	if err := collector.Validate(); err == nil || !strings.Contains(err.Error(), "services observer diagnostic export source") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestFailedCentralExporterMakesQueueHostUnhealthy(t *testing.T) {
	collector := healthyCollector(t)
	queueConfig, err := config.Load("../../config/example-services.yaml")
	if err != nil {
		t.Fatal(err)
	}
	collector.Config = queueConfig
	collector.Service = func(_ context.Context, name string) (string, error) {
		if name == "gha-diagnostic-exporter.service" {
			return "failed", nil
		}
		return "active", nil
	}
	if snapshot := collector.Collect(context.Background()); snapshot.Healthy {
		t.Fatalf("failed central exporter reported healthy: %#v", snapshot.Services)
	}
}

func TestDiagnosticWALHighWatermarkMakesFleetUnhealthy(t *testing.T) {
	collector := healthyCollector(t)
	collector.DiagnosticMaxBytes = 100
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 1, Bytes: 80}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || !slices.Contains(snapshot.CollectionErrors, "diagnostics: durable WAL high-watermark reached") {
		t.Fatalf("high WAL watermark was accepted: %#v", snapshot.CollectionErrors)
	}
}

func TestCollectorSummarizesBoundedCentralQueueIntent(t *testing.T) {
	collector := healthyCollector(t)
	collector.Queue = func(context.Context) (queueintent.Snapshot, error) {
		return queueintent.Snapshot{
			Generation: 9,
			Stored:     2,
			Expired:    1,
			Active: []queueintent.Intent{{
				Key: "intent", ScaleSetID: 11, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43", RunnerRequestID: 101,
				ScaleSetName: "nddev-linux-integration", Repository: "owner/repo",
				WorkflowRef: "workflow", EventName: "pull_request",
				QueueTime: observationTime.Add(-2 * time.Minute), State: queueintent.StateAcquired,
				Priority: 2, UpdatedAt: observationTime.Add(-time.Minute), ExpiresAt: observationTime.Add(time.Minute),
			}},
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Queue.Generation != 9 || snapshot.Queue.Stored != 2 || snapshot.Queue.Active != 1 ||
		snapshot.Queue.Expired != 1 || snapshot.Queue.InFlight != 1 || snapshot.Queue.OldestQueueAgeSeconds != 120 ||
		snapshot.Queue.ByState["acquired"] != 1 || snapshot.Queue.ByPriority[2] != 1 ||
		snapshot.Queue.ByScaleSet["nddev-linux-integration"] != 1 {
		t.Fatalf("queue summary = %#v, errors=%v", snapshot.Queue, snapshot.CollectionErrors)
	}
}

func TestQueueSummarySeparatesDirectJITFromMissingCorrelation(t *testing.T) {
	base := queueintent.Intent{
		Key: "assigned", ScaleSetID: 11, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43",
		ScaleSetName: "nddev-linux-standard", Repository: "owner/repo",
		WorkflowRef: "unavailable-before-job-available", EventName: "unavailable-before-job-available",
		QueueTime: observationTime.Add(-time.Minute), State: queueintent.StateAssigned, Priority: 1,
		StateEnteredAt: observationTime.Add(-time.Minute), UpdatedAt: observationTime.Add(-time.Minute),
		ExpiresAt: observationTime.Add(time.Minute),
	}
	running := base
	running.Key = "running"
	running.JobID = "6c3077ba-3664-5824-b2cf-e22a31b25f44"
	running.State = queueintent.StateRunning
	running.WorkflowRunID = 123
	running.GitHubRunnerID = 456
	running.RunnerName = "runner-direct-jit"
	summary, err := summarizeQueue(queueintent.Snapshot{Active: []queueintent.Intent{base, running}}, testPlatform(t), observationTime)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MissingRunnerRequestID != 1 || summary.DirectJITWithoutRequestID != 1 {
		t.Fatalf("runner request classification = %#v", summary)
	}
}

func TestQueueSummarySeparatesTransientAndPersistentCorrelationGaps(t *testing.T) {
	transient := queueintent.Intent{
		Key: "transient", ScaleSetID: 11, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43",
		ScaleSetName: "nddev-linux-standard", Repository: "owner",
		WorkflowRef: "unavailable-before-job-available", EventName: "unavailable-before-job-available",
		QueueTime: observationTime.Add(-queueCorrelationGracePeriod + time.Second),
		State:     queueintent.StateAssigned, Priority: 1,
		StateEnteredAt: observationTime.Add(-time.Minute), UpdatedAt: observationTime.Add(-time.Second),
		ExpiresAt: observationTime.Add(time.Minute),
	}
	persistent := transient
	persistent.Key = "persistent"
	persistent.JobID = "6c3077ba-3664-5824-b2cf-e22a31b25f44"
	persistent.QueueTime = observationTime.Add(-queueCorrelationGracePeriod)
	persistent.StateEnteredAt = observationTime.Add(-queueCorrelationGracePeriod)
	summary, err := summarizeQueue(queueintent.Snapshot{Active: []queueintent.Intent{transient, persistent}}, testPlatform(t), observationTime)
	if err != nil {
		t.Fatal(err)
	}
	if summary.UnboundRepository != 2 || summary.MissingWorkflowRunID != 2 ||
		summary.UnboundRepositoryBeyondGrace != 1 || summary.MissingWorkflowRunIDBeyondGrace != 1 {
		t.Fatalf("correlation convergence classification = %#v", summary)
	}
}

func TestQueueSummaryDoesNotPageOnRehydratedJobWaitingForCapacity(t *testing.T) {
	queued := queueintent.Intent{
		Key: "rehydrated", ScaleSetID: 11, JobID: "5c3077ba-3664-5824-b2cf-e22a31b25f43",
		ScaleSetName: "nddev-linux-integration", Repository: "owner",
		WorkflowRef: "authoritative-rehydration", EventName: "push",
		QueueTime: observationTime.Add(-10 * queueCorrelationGracePeriod),
		State:     queueintent.StateQueued, Priority: 1,
		StateEnteredAt: observationTime.Add(-time.Minute), UpdatedAt: observationTime.Add(-time.Second),
		ExpiresAt: observationTime.Add(time.Minute),
	}
	summary, err := summarizeQueue(queueintent.Snapshot{Active: []queueintent.Intent{queued}}, testPlatform(t), observationTime)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MissingWorkflowRunID != 1 || summary.MissingWorkflowRunIDBeyondGrace != 0 ||
		summary.UnboundRepository != 1 || summary.UnboundRepositoryBeyondGrace != 0 {
		t.Fatalf("queued correlation classification = %#v", summary)
	}
}

func TestQueueSummaryStartsCorrelationGraceWhenCapacityWaitEnds(t *testing.T) {
	assigned := queueintent.Intent{
		Key: "assigned-rehydrated", ScaleSetID: 11, JobID: "6c3077ba-3664-5824-b2cf-e22a31b25f44",
		ScaleSetName: "nddev-linux-integration", Repository: "owner",
		WorkflowRef: "authoritative-rehydration", EventName: "push",
		QueueTime: observationTime.Add(-10 * queueCorrelationGracePeriod),
		State:     queueintent.StateAssigned, Priority: 1,
		StateEnteredAt: observationTime.Add(-queueCorrelationGracePeriod + time.Second), UpdatedAt: observationTime.Add(-time.Second),
		ExpiresAt: observationTime.Add(time.Minute),
	}
	summary, err := summarizeQueue(queueintent.Snapshot{Active: []queueintent.Intent{assigned}}, testPlatform(t), observationTime)
	if err != nil {
		t.Fatal(err)
	}
	if summary.MissingWorkflowRunIDBeyondGrace != 0 || summary.UnboundRepositoryBeyondGrace != 0 {
		t.Fatalf("newly assigned correlation classification = %#v", summary)
	}
}

func TestCollectorMarksRunningIntentWithoutExecutionLeaseUnhealthy(t *testing.T) {
	collector := healthyCollector(t)
	collector.Journal = func(context.Context) (providerjournal.Journal, error) {
		return providerjournal.Journal{
			SchemaVersion: providerjournal.SchemaVersion,
			Leases:        map[string]providerjournal.Lease{},
			Claims:        map[string]providerjournal.Claim{},
		}, nil
	}
	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }
	collector.Queue = func(context.Context) (queueintent.Snapshot, error) {
		return queueintent.Snapshot{Active: []queueintent.Intent{{
			Key: "stuck-running", ScaleSetID: 1,
			JobID: "da4c5ef8-1e3a-54a9-ab12-14c34f8dfd71", ScaleSetName: "nddev-linux-standard",
			Repository: "owner/repo", QueueTime: observationTime.Add(-time.Minute),
			State: queueintent.StateRunning, Priority: 2,
			UpdatedAt: observationTime.Add(-time.Minute), ExpiresAt: observationTime.Add(time.Minute),
		}}}, nil
	}

	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Queue.UncoveredRunning != 1 {
		t.Fatalf("uncovered running intent was accepted: %#v", snapshot)
	}
}

func TestCollectorRejectsQueueIntentForUnknownScaleSet(t *testing.T) {
	collector := healthyCollector(t)
	collector.Queue = func(context.Context) (queueintent.Snapshot, error) {
		return queueintent.Snapshot{Active: []queueintent.Intent{{
			Key: "intent", ScaleSetName: "foreign", QueueTime: observationTime,
		}}}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || len(snapshot.CollectionErrors) != 1 || !strings.Contains(snapshot.CollectionErrors[0], "unknown scale set") {
		t.Fatalf("foreign queue intent was accepted: %#v", snapshot)
	}
}

func TestCollectorCountsDurableWarmPreemptionOwnership(t *testing.T) {
	journal := testJournal()
	journal.Leases["runner-cold"] = providerjournal.Lease{
		InstanceName: "runner-cold", ControllerID: "controller", PoolID: "pool-integration",
		PoolName: "nddev-linux-integration", VCPU: 4, MemoryMiB: 10240,
		ImageFingerprint: strings.Repeat("b", 64), State: providerjournal.StateAdmitted,
		AdmittedAt: observationTime.Add(-time.Minute), UpdatedAt: observationTime.Add(-time.Second),
		ExpiresAt: observationTime.Add(time.Minute),
	}
	warm := journal.Leases["runner-one"]
	warm.PoolID = "warm/nddev-linux-standard"
	warm.State = providerjournal.StateDeleting
	warm.PreemptedBy = "runner-cold"
	journal.Leases["runner-one"] = warm
	journal.WarmPreemptionsTotal = 7

	summary := summarizeJournal(journal, observationTime)
	if summary.WarmPreemptions != 1 || summary.WarmPreemptionsTotal != 7 || summary.ByState[string(providerjournal.StateDeleting)] != 1 {
		t.Fatalf("preemption summary: %#v", summary)
	}
}

func TestCollectorAllowsBoundedConcurrentExporterAheadView(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 1, Bytes: 2048}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-15 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-15 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 2, SourceBytes: 4096, ExportedBytes: 4096,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncGrace ||
		snapshot.DiagnosticExportSync.LocalBundleDelta != -1 || snapshot.DiagnosticExportSync.LocalByteDelta != -2048 ||
		snapshot.DiagnosticExportSync.GraceRemainingSeconds != 75 {
		t.Fatalf("bounded concurrent exporter view was rejected: %#v", snapshot)
	}
}

func TestCollectorAllowsOnlyBoundedHealthyDiagnosticConvergence(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 3, Bytes: 5120, OldestAgeSeconds: 60, NewestAgeSeconds: 5}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 2, SourceBytes: 4096, ExportedBytes: 4096,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || len(snapshot.CollectionErrors) != 0 ||
		snapshot.DiagnosticExportSync.State != diagnosticExportSyncGrace ||
		snapshot.DiagnosticExportSync.GracePeriodSeconds != 90 ||
		snapshot.DiagnosticExportSync.GraceRemainingSeconds != 60 ||
		snapshot.DiagnosticExportSync.LocalBundleDelta != 1 ||
		snapshot.DiagnosticExportSync.LocalByteDelta != 1024 {
		t.Fatalf("bounded convergence was not represented safely: %#v", snapshot)
	}
}

func TestCollectorFailsAfterDiagnosticConvergenceGraceExpires(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 3, Bytes: 5120}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-91 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-91 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 2, SourceBytes: 4096, ExportedBytes: 4096,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncInvalid ||
		len(snapshot.CollectionErrors) != 1 || !strings.Contains(snapshot.CollectionErrors[0], "exceeded the 1m30s convergence grace") {
		t.Fatalf("expired convergence grace was accepted: %#v", snapshot)
	}
}

func TestCollectorRejectsIncoherentDiagnosticDivergenceImmediately(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 2, Bytes: 5120}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncInvalid ||
		len(snapshot.CollectionErrors) != 1 || !strings.Contains(snapshot.CollectionErrors[0], "diverge incoherently") {
		t.Fatalf("incoherent diagnostic divergence was accepted: %#v", snapshot)
	}
}

func TestCollectorAllowsPendingExportDuringConvergenceGrace(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 3, Bytes: 5120}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 1, PendingBundles: 1,
			SourceBytes: 4096, ExportedBytes: 2048,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncGrace ||
		len(snapshot.CollectionErrors) != 0 || snapshot.DiagnosticExportSync.GraceRemainingSeconds != 60 {
		t.Fatalf("bounded pending exporter state was rejected: %#v", snapshot)
	}
}

func TestCollectorRejectsPendingExportAfterConvergenceGrace(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 3, Bytes: 5120}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-91 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-91 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 1, PendingBundles: 1,
			SourceBytes: 4096, ExportedBytes: 2048,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncInvalid ||
		len(snapshot.CollectionErrors) != 1 || !strings.Contains(snapshot.CollectionErrors[0], "pending exports exceeded") {
		t.Fatalf("expired pending exporter state was accepted: %#v", snapshot)
	}
}

func TestCollectorReportsExactOrphanAndMissingCounts(t *testing.T) {
	collector := healthyCollector(t)
	collector.Instances = func(context.Context) ([]string, error) {
		return []string{"orphan-one", "orphan-two"}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Incus.OrphanInstances != 2 || snapshot.Incus.MissingInstances != 1 {
		t.Fatalf("unexpected reconciliation result: %#v", snapshot.Incus)
	}
}

func TestCollectorKeepsBoundedDeletingVisibilityConvergenceHealthy(t *testing.T) {
	collector := healthyCollector(t)
	journal, err := collector.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases["runner-one"]
	lease.State = providerjournal.StateDeleting
	lease.UpdatedAt = observationTime.Add(-2 * time.Second)
	lease.ExpiresAt = observationTime.Add(5 * time.Minute)
	journal.Leases["runner-one"] = lease
	collector.Journal = func(context.Context) (providerjournal.Journal, error) { return journal, nil }
	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }

	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingInstances != 0 || snapshot.Incus.MissingDeletingWithinGrace != 1 {
		t.Fatalf("bounded delete convergence poisoned fleet health: %#v", snapshot)
	}

	lease.UpdatedAt = observationTime.Add(-deletingVisibilityGrace - time.Second)
	journal.Leases["runner-one"] = lease
	snapshot = collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Incus.MissingInstances != 1 || snapshot.Incus.MissingDeletingWithinGrace != 0 {
		t.Fatalf("persistent missing delete escaped health blocker: %#v", snapshot)
	}
}

func TestCollectorBoundsFirstObservedCreatedVisibilityConvergence(t *testing.T) {
	collector := healthyCollector(t)
	now := observationTime
	collector.Now = func() time.Time { return now }
	collector.CreatedVisibility = NewCreatedVisibilityTracker()
	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }

	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingInstances != 0 ||
		snapshot.Incus.MissingCreatedWithinGrace != 1 || snapshot.Incus.OldestMissingCreatedAgeSecs != 0 {
		t.Fatalf("first absent created sample poisoned health: %#v", snapshot.Incus)
	}

	now = now.Add(20 * time.Second)
	snapshot = collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingCreatedWithinGrace != 1 ||
		snapshot.Incus.OldestMissingCreatedAgeSecs != 20 {
		t.Fatalf("bounded created convergence was not retained: %#v", snapshot.Incus)
	}

	now = now.Add(createdVisibilityGrace - 20*time.Second + time.Second)
	snapshot = collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Incus.MissingInstances != 1 || snapshot.Incus.MissingCreatedWithinGrace != 0 {
		t.Fatalf("persistent missing created lease escaped health blocker: %#v", snapshot.Incus)
	}

	collector.Instances = func(context.Context) ([]string, error) { return []string{"runner-one"}, nil }
	snapshot = collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingInstances != 0 {
		t.Fatalf("returned instance did not clear missing state: %#v", snapshot.Incus)
	}

	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }
	snapshot = collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingCreatedWithinGrace != 1 ||
		snapshot.Incus.OldestMissingCreatedAgeSecs != 0 {
		t.Fatalf("new absence reused stale first-seen state: %#v", snapshot.Incus)
	}
}

func TestCollectorObservesMissingImageMaintenanceWithoutFailingFleetHealth(t *testing.T) {
	collector := healthyCollector(t)
	journal, err := collector.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	lease := journal.Leases["runner-one"]
	delete(journal.Leases, "runner-one")
	lease.InstanceName = "gha-image-builder-deadbeef"
	lease.PoolID = "image-maintenance/nddev-linux-standard"
	journal.Leases[lease.InstanceName] = lease
	collector.Journal = func(context.Context) (providerjournal.Journal, error) { return journal, nil }
	collector.Instances = func(context.Context) ([]string, error) { return nil, nil }

	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.MissingInstances != 0 ||
		snapshot.Incus.MissingMaintenanceInstances != 1 ||
		snapshot.Journal.CreatedWithoutRunningIdentity != 0 {
		t.Fatalf("maintenance teardown gap poisoned fleet health: %#v", snapshot)
	}
}

func TestCollectorObservesVisibleImageMaintenanceWithoutReportingAnOrphan(t *testing.T) {
	collector := healthyCollector(t)
	journal, err := collector.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	delete(journal.Leases, "runner-one")
	collector.Journal = func(context.Context) (providerjournal.Journal, error) { return journal, nil }
	collector.Instances = func(context.Context) ([]string, error) {
		return []string{"gha-image-builder-012345abcdef", "gha-image-smoke-fedcba543210"}, nil
	}

	snapshot := collector.Collect(context.Background())
	if !snapshot.Healthy || snapshot.Incus.OrphanInstances != 0 ||
		snapshot.Incus.VisibleMaintenanceInstances != 2 {
		t.Fatalf("visible maintenance poisoned fleet health: %#v", snapshot)
	}
}

func TestCollectorDoesNotHideMalformedMaintenanceName(t *testing.T) {
	collector := healthyCollector(t)
	journal, err := collector.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	delete(journal.Leases, "runner-one")
	collector.Journal = func(context.Context) (providerjournal.Journal, error) { return journal, nil }
	collector.Instances = func(context.Context) ([]string, error) {
		return []string{"gha-image-builder-not-a-digest"}, nil
	}

	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.Incus.OrphanInstances != 1 ||
		snapshot.Incus.VisibleMaintenanceInstances != 0 {
		t.Fatalf("malformed maintenance identity escaped orphan detection: %#v", snapshot)
	}
}

func TestCollectorTreatsAdmittedLeaseAsCreateTransition(t *testing.T) {
	t.Parallel()
	for _, visible := range []bool{false, true} {
		t.Run(fmt.Sprintf("visible=%t", visible), func(t *testing.T) {
			collector := healthyCollector(t)
			journal, err := collector.Journal(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			lease := journal.Leases["runner-one"]
			lease.State = providerjournal.StateAdmitted
			journal.Leases["runner-one"] = lease
			collector.Journal = func(context.Context) (providerjournal.Journal, error) { return journal, nil }
			if !visible {
				collector.Instances = func(context.Context) ([]string, error) { return nil, nil }
			}
			snapshot := collector.Collect(context.Background())
			if snapshot.Incus.OrphanInstances != 0 || snapshot.Incus.MissingInstances != 0 {
				t.Fatalf("admitted create transition was reported inconsistent: %#v", snapshot.Incus)
			}
		})
	}
}

func TestCollectorFailsClosedAndRedactsSourceErrors(t *testing.T) {
	collector := healthyCollector(t)
	collector.Host = func(context.Context) (hostprobe.Snapshot, error) {
		return hostprobe.Snapshot{}, errors.New("Author" + "ization: Bear" + "er secret-token-value failed")
	}
	collector.Service = func(_ context.Context, name string) (string, error) {
		if name == "gha-zot" {
			return "failed", nil
		}
		return "active", nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || len(snapshot.CollectionErrors) != 1 {
		t.Fatalf("expected one collection failure: %#v", snapshot)
	}
	if strings.Contains(snapshot.CollectionErrors[0], "secret-token-value") {
		t.Fatalf("source error was not redacted: %q", snapshot.CollectionErrors[0])
	}
	if snapshot.Services[4].Name != "gha-zot" || snapshot.Services[4].Active {
		t.Fatalf("failed service not reported: %#v", snapshot.Services)
	}
}

func TestCollectorRejectsDuplicateVisibleInstanceNames(t *testing.T) {
	collector := healthyCollector(t)
	collector.Instances = func(context.Context) ([]string, error) {
		return []string{"runner-one", "runner-one"}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || len(snapshot.CollectionErrors) != 1 ||
		!strings.Contains(snapshot.CollectionErrors[0], "duplicate visible instance") {
		t.Fatalf("duplicate inventory was accepted: %#v", snapshot)
	}
}

func TestCollectorValidateRequiresEverySource(t *testing.T) {
	collector := healthyCollector(t)
	collector.Journal = nil
	if err := collector.Validate(); err == nil || !strings.Contains(err.Error(), "every observer source") {
		t.Fatalf("validation error = %v", err)
	}
}

func TestCollectorFailsClosedForStaleOrIncompleteDiagnosticExport(t *testing.T) {
	collector := healthyCollector(t)
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-4 * time.Minute).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 1, PendingBundles: 1,
			SourceBytes: 4096, ExportedBytes: 2048,
		}, nil
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || len(snapshot.CollectionErrors) != 1 ||
		!strings.Contains(snapshot.CollectionErrors[0], "stale") {
		t.Fatalf("stale export status was accepted: %#v", snapshot)
	}
}

func TestCollectorDoesNotDuplicateDiagnosticFailureWhenSpoolIsUnavailable(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{}, errors.New("synthetic spool failure")
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || len(snapshot.CollectionErrors) != 1 ||
		!strings.HasPrefix(snapshot.CollectionErrors[0], "diagnostics:") {
		t.Fatalf("diagnostic failure was duplicated: %#v", snapshot.CollectionErrors)
	}
	if snapshot.DiagnosticExport.ExportedBundles != 2 {
		t.Fatalf("readable export status was discarded: %#v", snapshot.DiagnosticExport)
	}
	if snapshot.DiagnosticExportSync.State != diagnosticExportSyncUnavailable {
		t.Fatalf("unavailable spool produced a misleading sync state: %#v", snapshot.DiagnosticExportSync)
	}
}

func TestCollectorMarksUnreadableDiagnosticExportUnavailable(t *testing.T) {
	collector := healthyCollector(t)
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{}, errors.New("synthetic exporter state failure")
	}
	snapshot := collector.Collect(context.Background())
	if snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncUnavailable ||
		len(snapshot.CollectionErrors) != 1 || !strings.HasPrefix(snapshot.CollectionErrors[0], "diagnostic export:") {
		t.Fatalf("unreadable exporter state was not represented safely: %#v", snapshot)
	}
}

func TestSummarizeExecutionCorrelationIsSymmetric(t *testing.T) {
	now := time.Date(2026, 8, 20, 22, 0, 0, 0, time.UTC)
	journal := providerjournal.Journal{Leases: map[string]providerjournal.Lease{
		"runner-covered": {
			InstanceName: "runner-covered", State: providerjournal.StateCreated,
			AdmittedAt: now.Add(-2 * time.Minute),
		},
		"runner-unbound": {
			InstanceName: "runner-unbound", State: providerjournal.StateCreated,
			AdmittedAt: now.Add(-17 * time.Minute),
		},
		"runner-deleting": {
			InstanceName: "runner-deleting", State: providerjournal.StateDeleting,
			AdmittedAt: now.Add(-30 * time.Minute),
		},
	}}
	queue := queueintent.Snapshot{Active: []queueintent.Intent{
		{State: queueintent.StateRunning, RunnerName: "runner-covered"},
		{State: queueintent.StateRunning},
		{State: queueintent.StateAssigned},
	}}

	got := summarizeExecutionCorrelation(journal, queue, now)
	if got.RunningWithoutRunnerIdentity != 1 {
		t.Fatalf("running without runner identity = %d, want 1", got.RunningWithoutRunnerIdentity)
	}
	if got.CreatedWithoutRunningIdentity != 1 {
		t.Fatalf("created without running identity = %d, want 1", got.CreatedWithoutRunningIdentity)
	}
	if got.OldestCreatedWithoutRunningIdentityAgeSeconds != 17*60 {
		t.Fatalf("oldest unbound created age = %d, want %d", got.OldestCreatedWithoutRunningIdentityAgeSeconds, 17*60)
	}
}
