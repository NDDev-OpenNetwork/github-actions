package fleetobserve

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

var observationTime = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func testPlatform(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load("../../config/server-gha-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func healthyHost() hostprobe.Snapshot {
	return hostprobe.Snapshot{
		SchemaVersion: 1,
		CapturedAt:    observationTime.Format(time.RFC3339),
		Hostname:      "server-gha-runner-1",
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
		Config: testPlatform(t),
		Host: func(context.Context) (hostprobe.Snapshot, error) {
			return healthyHost(), nil
		},
		Journal: func(context.Context) (providerjournal.Journal, error) {
			return testJournal(), nil
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
	if len(snapshot.Pools) != 4 || len(snapshot.Services) != len(ServiceNames()) || snapshot.Diagnostics.Bundles != 2 ||
		snapshot.DiagnosticExport.ExportedBundles != 2 ||
		snapshot.DiagnosticExportSync.State != diagnosticExportSyncSynchronized {
		t.Fatalf("incomplete snapshot: %#v", snapshot)
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

	summary := summarizeJournal(journal)
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

func TestCollectorRejectsPendingExportDuringConvergenceGrace(t *testing.T) {
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
	if snapshot.Healthy || snapshot.DiagnosticExportSync.State != diagnosticExportSyncInvalid ||
		len(snapshot.CollectionErrors) != 1 || !strings.Contains(snapshot.CollectionErrors[0], "pending or failed") {
		t.Fatalf("pending exporter state was hidden by convergence grace: %#v", snapshot)
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

func TestCollectorFailsClosedAndRedactsSourceErrors(t *testing.T) {
	collector := healthyCollector(t)
	collector.Host = func(context.Context) (hostprobe.Snapshot, error) {
		return hostprobe.Snapshot{}, errors.New("Authorization: Bearer secret-token-value failed")
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
