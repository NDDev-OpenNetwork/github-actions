package fleetobserve

import (
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/diagnosticexport"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

func TestRenderPrometheusIsDeterministicAndBounded(t *testing.T) {
	collector := healthyCollector(t)
	snapshot := collector.Collect(t.Context())
	snapshot.Host.Memory.OOMKillsTotal = 2
	snapshot.Host.Pressure = hostprobe.Pressure{
		Available: true,
		CPU:       hostprobe.PressureResource{Some: hostprobe.PressureWindow{Avg10: 1.25, TotalMicros: 2_000_000}},
	}
	metrics := RenderPrometheus(snapshot, observationTime.Add(5*time.Second), 45*time.Second)
	for _, wanted := range []string{
		"gha_fleet_observer_up 1\n",
		"gha_fleet_platform_healthy 1\n",
		"gha_fleet_legacy_runner_listeners 12\n",
		"gha_fleet_host_oom_kills_total 2\n",
		"gha_fleet_host_psi_available 1\n",
		`gha_fleet_host_psi_stall_percent{mode="some",resource="cpu",window_seconds="10"} 1.25`,
		`gha_fleet_host_psi_stall_seconds_total{mode="some",resource="cpu"} 2`,
		`gha_fleet_pool_container_admission_ready{pool="nddev-linux-standard"} 1`,
		`gha_fleet_provider_journal_leases_by_state{state="created"} 1`,
		"gha_fleet_provider_retry_journal_generation 1\n",
		"gha_fleet_provider_retry_deferred_records 0\n",
		"gha_fleet_provider_terminal_circuits 0\n",
		"gha_fleet_shared_capacity_saturated 0\n",
		"gha_fleet_shared_capacity_probe_owned 0\n",
		"gha_fleet_shared_capacity_probe_active 0\n",
		"gha_fleet_shared_capacity_waiters 0\n",
		`gha_fleet_shared_capacity_wake_reason{reason="none"} 1`,
		`gha_fleet_provider_retry_records_by_error_class{error_class="provider"} 0`,
		`gha_fleet_provider_retry_deferred_records_by_error_class{error_class="provider"} 0`,
		`gha_fleet_provider_lease_oldest_state_age_seconds{state="created"} 1`,
		"gha_fleet_provider_warm_preemptions 0\n",
		"# TYPE gha_fleet_provider_warm_preemptions_total counter\n",
		"gha_fleet_provider_warm_preemptions_total 0\n",
		"gha_fleet_queue_journal_generation 1\n",
		"gha_fleet_queue_intents_active 0\n",
		"gha_fleet_queue_intents_in_flight 0\n",
		"gha_fleet_queue_uncovered_running 0\n",
		"gha_fleet_queue_missing_runner_request_id 0\n",
		"gha_fleet_queue_direct_jit_without_runner_request_id 0\n",
		`gha_fleet_queue_intents_by_state{state="queued"} 0`,
		`gha_fleet_queue_intent_oldest_state_age_seconds{state="assigned"} 0`,
		`gha_fleet_queue_intents_by_priority{priority="0"} 0`,
		`gha_fleet_queue_intents_by_scale_set{scale_set="nddev-linux-integration"} 0`,
		"gha_fleet_incus_orphan_instances 0\n",
		"gha_fleet_diagnostic_bundles 2\n",
		"gha_fleet_diagnostic_export_exported_bundles 2\n",
		"gha_fleet_diagnostic_export_pending_bundles 0\n",
		`gha_fleet_diagnostic_export_sync_state{state="synchronized"} 1`,
		`gha_fleet_diagnostic_export_sync_state{state="convergence-grace"} 0`,
		"gha_fleet_diagnostic_export_sync_grace_remaining_seconds 0\n",
		"gha_fleet_diagnostic_export_local_bundle_delta 0\n",
		"gha_fleet_diagnostic_export_observed_age_seconds 5\n",
		"gha_fleet_diagnostic_export_last_success_age_seconds 5\n",
		"gha_fleet_diagnostic_export_last_progress_age_seconds -1\n",
		"gha_fleet_diagnostic_export_last_full_sync_age_seconds -1\n",
		`gha_fleet_service_up{service="gha-zot"} 1`,
	} {
		if !strings.Contains(metrics, wanted) {
			t.Errorf("metrics missing %q\n%s", wanted, metrics)
		}
	}
	for _, retired := range []string{"gha_fleet_pool_pilot_ready", "gha_fleet_pool_findings"} {
		if strings.Contains(metrics, retired) {
			t.Errorf("retired VM preflight metric %q remains\n%s", retired, metrics)
		}
	}
	if strings.Contains(metrics, "runner-one") {
		t.Fatal("dynamic instance identity leaked into metrics")
	}
	if metrics != RenderPrometheus(snapshot, observationTime.Add(5*time.Second), 45*time.Second) {
		t.Fatal("metrics rendering is not deterministic")
	}
}

func TestRenderPrometheusExposesBoundedDiagnosticConvergence(t *testing.T) {
	collector := healthyCollector(t)
	collector.Diagnostics = func(time.Time) (workerdiagnostics.SpoolStats, error) {
		return workerdiagnostics.SpoolStats{Bundles: 3, Bytes: 5120}, nil
	}
	collector.Export = func() (diagnosticexport.Status, error) {
		return diagnosticexport.Status{
			SchemaVersion: 1, DeploymentStage: "canary",
			ObservedAt:    observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			LastSuccessAt: observationTime.Add(-30 * time.Second).Format(time.RFC3339Nano),
			SourceBundles: 2, ExportedBundles: 2, SourceBytes: 4096, ExportedBytes: 4096,
		}, nil
	}
	metrics := RenderPrometheus(collector.Collect(t.Context()), observationTime, 45*time.Second)
	for _, wanted := range []string{
		`gha_fleet_diagnostic_export_sync_state{state="convergence-grace"} 1`,
		"gha_fleet_diagnostic_export_sync_grace_remaining_seconds 60\n",
		"gha_fleet_diagnostic_export_local_bundle_delta 1\n",
		"gha_fleet_diagnostic_export_local_byte_delta 1024\n",
	} {
		if !strings.Contains(metrics, wanted) {
			t.Errorf("convergence metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestRenderPrometheusMarksStaleSampleDown(t *testing.T) {
	snapshot := healthyCollector(t).Collect(t.Context())
	metrics := RenderPrometheus(snapshot, observationTime.Add(time.Minute), 45*time.Second)
	for _, wanted := range []string{"gha_fleet_observer_up 0\n", "gha_fleet_snapshot_stale 1\n"} {
		if !strings.Contains(metrics, wanted) {
			t.Fatalf("stale metrics missing %q\n%s", wanted, metrics)
		}
	}
}

func TestRenderPrometheusFailsClosedForFutureSample(t *testing.T) {
	snapshot := healthyCollector(t).Collect(t.Context())
	metrics := RenderPrometheus(snapshot, observationTime.Add(-time.Second), 45*time.Second)
	if !strings.Contains(metrics, "gha_fleet_observer_up 0\n") ||
		!strings.Contains(metrics, "gha_fleet_snapshot_stale 1\n") {
		t.Fatalf("future sample was accepted\n%s", metrics)
	}
}

func TestMetricLabelsAreEscaped(t *testing.T) {
	var output strings.Builder
	metric(&output, "test_metric", map[string]string{"label": "line\nslash\\quote\""}, 1)
	if output.String() != "test_metric{label=\"line\\nslash\\\\quote\\\"\"} 1\n" {
		t.Fatalf("escaped metric = %q", output.String())
	}
}
