package hostprobe

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func TestSoftwareVersionRequiresSuccessfulCommand(t *testing.T) {
	t.Parallel()
	if softwareVersion("permission denied", errors.New("exit status 1")).Present {
		t.Fatal("failed command output must not identify installed software")
	}
	version := softwareVersion("6.0.0", nil)
	if !version.Present || version.Version != "6.0.0" {
		t.Fatalf("unexpected version: %#v", version)
	}
}

func TestPercentRoundsUpWithoutOverflow(t *testing.T) {
	t.Parallel()
	if value := percent(^uint64(0)-1, ^uint64(0)); value != 100 {
		t.Fatalf("expected 100, got %d", value)
	}
	if value := percent(1, 3); value != 34 {
		t.Fatalf("expected 34, got %d", value)
	}
}

func TestParseMeminfo(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "meminfo")
	data := []byte("MemTotal:       32768000 kB\nMemAvailable:   24576000 kB\nSwapTotal:       8388608 kB\nSwapFree:        7340032 kB\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := parseMeminfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if memory.TotalMiB != 32000 || memory.AvailableMiB != 24000 || memory.SwapTotalMiB != 8192 || memory.SwapFreeMiB != 7168 {
		t.Fatalf("unexpected memory: %#v", memory)
	}
}

func TestParsePressureAndOOMCounters(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	pressureDirectory := filepath.Join(directory, "pressure")
	if err := os.Mkdir(pressureDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"cpu", "memory", "io"} {
		content := "some avg10=1.25 avg60=2.50 avg300=3.75 total=4000000\n"
		if resource != "cpu" {
			content += "full avg10=0.25 avg60=0.50 avg300=0.75 total=1000000\n"
		}
		if err := os.WriteFile(filepath.Join(pressureDirectory, resource), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pressure, err := parsePressure(pressureDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if !pressure.Available || pressure.CPU.Some.Avg10 != 1.25 || pressure.Memory.Full.Avg300 != 0.75 ||
		pressure.IO.Full.TotalMicros != 1000000 {
		t.Fatalf("unexpected pressure: %#v", pressure)
	}
	vmstat := filepath.Join(directory, "vmstat")
	if err := os.WriteFile(vmstat, []byte("pgfault 10\noom_kill 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oomKills, err := parseOOMKills(vmstat)
	if err != nil || oomKills != 7 {
		t.Fatalf("oom kills=%d err=%v", oomKills, err)
	}
}

func TestCountHostGlobalOOMLinesIgnoresMemoryCgroupKills(t *testing.T) {
	t.Parallel()
	body := []byte("" +
		"6,1,0,-;Out of memory: Killed process 100 (java)\n" +
		"6,2,0,-;oom-kill:constraint=CONSTRAINT_MEMCG,nodemask=(null),cpuset=/,mems_allowed=0,task_memcg=/incus.gha-fleet/1\n" +
		"6,3,0,-;Memory cgroup out of memory: Killed process 100 (java)\n" +
		"6,4,0,-;oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),cpuset=/,mems_allowed=0,global_oom,task_memcg=/\n" +
		"6,5,0,-;Out of memory: Killed process 200 (java)\n" +
		"Sep  4 01:16:00 gha-runner-1 kernel: oom-kill:constraint=CONSTRAINT_NONE,nodemask=(null),global_oom\n")
	if got := countHostGlobalOOMLines(body); got != 2 {
		t.Fatalf("host-global oom lines=%d, want 2", got)
	}
}

func TestObserveOOMKillsPrefersKmsgHostGlobalOverVmstat(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "dev"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc", "vmstat"), []byte("oom_kill 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kmsg := []byte("oom-kill:constraint=CONSTRAINT_MEMCG,task_memcg=/incus\n" +
		"oom-kill:constraint=CONSTRAINT_NONE,global_oom\n")
	if err := os.WriteFile(filepath.Join(root, "dev", "kmsg"), kmsg, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := observeOOMKills(t.Context(), root, stubCommandRunner{})
	if err != nil || got != 1 {
		t.Fatalf("observeOOMKills=%d err=%v, want 1 host-global kill", got, err)
	}
}

func TestObserveOOMKillsFallsBackToVmstatWithoutKmsg(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "proc", "vmstat"), []byte("oom_kill 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := observeOOMKills(t.Context(), root, stubCommandRunner{})
	if err != nil || got != 7 {
		t.Fatalf("observeOOMKills=%d err=%v, want vmstat 7", got, err)
	}
}

func TestCountJournalHostGlobalOOMTreatsEmptyExitOneAsZero(t *testing.T) {
	t.Parallel()
	got, err := countJournalHostGlobalOOM(t.Context(), stubCommandRunner{err: exec.Command("sh", "-c", "exit 1").Run()})
	if err != nil || got != 0 {
		t.Fatalf("empty journal grep got=%d err=%v, want 0 nil", got, err)
	}
}

func TestCountJournalHostGlobalOOMDoesNotTreatPermissionNoiseAsZero(t *testing.T) {
	t.Parallel()
	_, err := countJournalHostGlobalOOM(t.Context(), stubCommandRunner{
		out: []byte("No journal files were opened due to insufficient permissions.\n"),
		err: exec.Command("sh", "-c", "exit 1").Run(),
	})
	if err == nil {
		t.Fatal("permission failure must fall back, not report zero host-global kills")
	}
}

type stubCommandRunner struct {
	out []byte
	err error
}

func (s stubCommandRunner) Run(context.Context, string, ...string) ([]byte, error) {
	return s.out, s.err
}

func TestParsePressureRejectsMalformedAvailableData(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "cpu"), []byte("some avg10=not-a-number avg60=0 avg300=0 total=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := parsePressure(directory); err == nil {
		t.Fatal("malformed pressure data was accepted")
	}
}

func TestEvaluateColdPilotReady(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	decision := EvaluateColdPilot(snapshot, reserve(), pool())
	if !decision.PilotReady {
		t.Fatalf("expected ready decision, findings: %#v", decision.Findings)
	}
	if decision.RequiredCPUUnits != 6 || decision.RequiredMemoryMiB != 22528 {
		t.Fatalf("unexpected requirements: %#v", decision)
	}
}

func TestEvaluateContainerImageBuildIgnoresVMOnlyPrerequisites(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.KVM.Present = false
	snapshot.KVM.Accessible = false
	snapshot.KVM.Nested = false
	snapshot.Maintenance.RebootRequired = true

	decision := EvaluateContainerImageBuild(snapshot, reserve(), pool())
	if !decision.PilotReady {
		t.Fatalf("container build rejected VM-only prerequisites: %#v", decision.Findings)
	}
	for _, finding := range decision.Findings {
		if finding.Code == "kvm-missing" || finding.Code == "kvm-inaccessible" || finding.Code == "nested-kvm-disabled" {
			t.Fatalf("container build retained VM-only finding: %#v", finding)
		}
		if finding.Code == "reboot-required" && finding.Severity != SeverityWarning {
			t.Fatalf("container reboot finding = %#v, want warning", finding)
		}
	}
}

func TestEvaluateContainerImageBuildStillFailsClosedOnHostDependency(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.Software.Incus.Present = false
	decision := EvaluateContainerImageBuild(snapshot, reserve(), pool())
	if decision.PilotReady {
		t.Fatal("container build ignored missing Incus")
	}
}

func TestEvaluateColdPilotReportsAllBlockersAndCoexistence(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.CPU.PhysicalCores = 4
	snapshot.CPU.Load1 = 8
	snapshot.Memory.TotalMiB = 20000
	snapshot.Memory.AvailableMiB = 12000
	snapshot.RootFilesystem.FreePercent = 10
	snapshot.RootFilesystem.FreeInodesPercent = 5
	snapshot.KVM.Accessible = false
	snapshot.KVM.Nested = false
	snapshot.Maintenance.RebootRequired = true
	snapshot.Software.Incus = SoftwareVersion{}
	snapshot.LegacyRunners = LegacyRunners{Listeners: 12, Workers: 7}

	decision := EvaluateColdPilot(snapshot, reserve(), pool())
	if decision.PilotReady {
		t.Fatal("expected fail-closed decision")
	}
	codes := make(map[string]bool)
	for _, finding := range decision.Findings {
		codes[finding.Code] = true
	}
	for _, code := range []string{
		"insufficient-cpu", "host-cpu-pressure", "insufficient-memory", "insufficient-available-memory", "disk-pressure", "inode-pressure",
		"kvm-inaccessible", "nested-kvm-disabled", "reboot-required", "incus-missing",
		"legacy-coexistence",
	} {
		if !codes[code] {
			t.Errorf("missing finding %q in %#v", code, decision.Findings)
		}
	}
}

func readySnapshot() Snapshot {
	return Snapshot{
		SchemaVersion:   1,
		OperatingSystem: OperatingSystem{ID: "ubuntu", VersionID: "24.04", Architecture: "x86_64"},
		Virtualization:  "kvm",
		CPU:             CPU{PhysicalCores: 8},
		Memory:          Memory{TotalMiB: 32768, AvailableMiB: 32768},
		RootFilesystem:  Filesystem{FreePercent: 72, FreeInodesPercent: 60},
		KVM:             KVM{Present: true, Accessible: true, Nested: true},
		Maintenance:     Maintenance{SystemState: "running"},
		Software:        Software{Incus: SoftwareVersion{Present: true, Version: "6.0.0"}},
	}
}

func TestEvaluateColdPilotBlocksLiveMemoryPressure(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.Memory.AvailableMiB = 20 * 1024
	decision := EvaluateColdPilot(snapshot, reserve(), pool())
	if decision.PilotReady {
		t.Fatal("expected live memory pressure to block the pilot")
	}
	for _, finding := range decision.Findings {
		if finding.Code == "insufficient-available-memory" && finding.Severity == SeverityBlocker {
			return
		}
	}
	t.Fatalf("missing live-memory blocker: %#v", decision.Findings)
}

func TestEvaluateColdPilotBlocksCPUQueuePressure(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.CPU.Load1 = 4.01
	decision := EvaluateColdPilot(snapshot, reserve(), pool())
	if decision.PilotReady {
		t.Fatal("expected CPU queue pressure to block the pilot")
	}
	for _, finding := range decision.Findings {
		if finding.Code == "host-cpu-pressure" && finding.Severity == SeverityBlocker {
			return
		}
	}
	t.Fatalf("missing CPU-pressure blocker: %#v", decision.Findings)
}

func TestHealthyForRuntimeAdmissionDelegatesOnlyCapacityFindings(t *testing.T) {
	t.Parallel()

	capacity := EvaluateColdPilot(readySnapshot(), reserve(), pool())
	capacity.Findings = []Finding{
		{Code: "insufficient-cpu", Severity: SeverityBlocker},
		{Code: "insufficient-available-memory", Severity: SeverityBlocker},
		{Code: "disk-pressure", Severity: SeverityBlocker},
		{Code: "legacy-coexistence", Severity: SeverityInfo},
	}
	if !HealthyForRuntimeAdmission(capacity) {
		t.Fatal("capacity-only findings were treated as an opaque host failure")
	}

	capacity.Findings = append(capacity.Findings, Finding{Code: "host-cpu-pressure", Severity: SeverityBlocker})
	if HealthyForRuntimeAdmission(capacity) {
		t.Fatal("runtime host pressure was delegated to capacity accounting")
	}
}

func reserve() config.HostReserve {
	return config.HostReserve{
		MinimumCPUUnits:        4,
		MinimumMemoryMiB:       16 * 1024,
		MinimumPercent:         10,
		MinimumFreeDiskPercent: 20,
	}
}

func pool() config.Pool {
	return config.Pool{
		Name:      "nddev-linux-fast",
		Resources: config.Resources{VCPU: 2, MemoryMiB: 6 * 1024},
	}
}

// A degraded systemd aggregate is not by itself proof that this host cannot
// run a worker. The unit that failed decides that, so these cases pin each
// classification rather than the aggregate.
func TestEvaluateColdPilotClassifiesDegradedSystemdByFailedUnit(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		state    string
		failed   []string
		code     string
		severity Severity
	}{
		{
			name:     "unrelated failed unit keeps the host admissible",
			state:    "degraded",
			failed:   []string{"run-r7c2a1.service", "logrotate.service"},
			code:     "host-degraded-unrelated",
			severity: SeverityWarning,
		},
		{
			name:     "a failed fleet dependency blocks",
			state:    "degraded",
			failed:   []string{"logrotate.service", "gha-fleet-gateway.service"},
			code:     "required-service-failed",
			severity: SeverityBlocker,
		},
		{
			name:     "a failed manager blocks",
			state:    "degraded",
			failed:   []string{"garm.service"},
			code:     "required-service-failed",
			severity: SeverityBlocker,
		},
		{
			name:     "a failed observer does not block",
			state:    "degraded",
			failed:   []string{"gha-fleet-observer.service"},
			code:     "host-degraded-unrelated",
			severity: SeverityWarning,
		},
		{
			name:     "a failed hypervisor unit blocks",
			state:    "degraded",
			failed:   []string{"incus-startup.service"},
			code:     "required-service-failed",
			severity: SeverityBlocker,
		},
		{
			name:     "degradation without an enumerable cause fails closed",
			state:    "degraded",
			failed:   nil,
			code:     "system-unhealthy",
			severity: SeverityBlocker,
		},
		{
			name:     "a non-degraded abnormal state still fails closed",
			state:    "maintenance",
			failed:   nil,
			code:     "system-unhealthy",
			severity: SeverityBlocker,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			snapshot := readySnapshot()
			snapshot.Maintenance.SystemState = testCase.state
			snapshot.Maintenance.FailedUnits = testCase.failed

			decision := EvaluateColdPilot(snapshot, reserve(), pool())
			var found *Finding
			for index, finding := range decision.Findings {
				if finding.Code == testCase.code {
					found = &decision.Findings[index]
				}
			}
			if found == nil {
				t.Fatalf("missing %s finding: %#v", testCase.code, decision.Findings)
			}
			if found.Severity != testCase.severity {
				t.Fatalf("%s severity is %q, want %q", testCase.code, found.Severity, testCase.severity)
			}
			if wantReady := testCase.severity != SeverityBlocker; decision.PilotReady != wantReady {
				t.Fatalf("pilot ready is %v, want %v", decision.PilotReady, wantReady)
			}
			if healthy := HealthyForRuntimeAdmission(decision); healthy == (testCase.severity == SeverityBlocker) {
				t.Fatalf("runtime admission health is %v for a %q finding", healthy, testCase.severity)
			}
		})
	}
}

// Both production hosts were observed degraded on a failed gha-zot while every
// VM they could have run was unaffected. A cache outage is allowed to fall back
// to an uncached build, so it must not close admission; the same holds for the
// other cache service and for telemetry.
func TestEvaluateColdPilotKeepsCacheAndTelemetryFailuresOutOfAdmission(t *testing.T) {
	t.Parallel()
	for _, unit := range []string{
		"gha-zot.service",
		"gha-rustfs.service",
		"otelcol-fleet.service",
		"gha-warm-pool.service",
	} {
		t.Run(unit, func(t *testing.T) {
			t.Parallel()
			snapshot := readySnapshot()
			snapshot.Maintenance.SystemState = "degraded"
			snapshot.Maintenance.FailedUnits = []string{unit}

			decision := EvaluateColdPilot(snapshot, reserve(), pool())
			if !decision.PilotReady || !HealthyForRuntimeAdmission(decision) {
				t.Fatalf("%s closed admission: %#v", unit, decision.Findings)
			}
		})
	}
}

// The diagnostic exporter is deliberately not a fleet dependency: its failure
// must block teardown, where the evidence contract applies, and not
// provisioning, where it does not. A live transfer closed the whole fleet this
// way.
func TestEvaluateColdPilotKeepsDiagnosticExporterFailureOutOfAdmission(t *testing.T) {
	t.Parallel()
	snapshot := readySnapshot()
	snapshot.Maintenance.SystemState = "degraded"
	snapshot.Maintenance.FailedUnits = []string{"gha-diagnostic-exporter.service"}

	decision := EvaluateColdPilot(snapshot, reserve(), pool())
	if !decision.PilotReady || !HealthyForRuntimeAdmission(decision) {
		t.Fatalf("exporter failure closed admission: %#v", decision.Findings)
	}
}

// On a dedicated host the fleet is the only consumer, so one-minute load is a
// lagging proxy for accounting the journal already holds exactly, and leaving
// it as a blocker makes a refused create refuse the next one. On a shared host
// it is the only visible signal about the retained workloads.
func TestHealthyForRuntimeAdmissionDelegatesLoadOnlyOnADedicatedHost(t *testing.T) {
	t.Parallel()
	for mode, wantHealthy := range map[string]bool{"dedicated": true, "retained-workloads": false} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			hostReserve := reserve()
			hostReserve.Mode = mode
			snapshot := readySnapshot()
			snapshot.CPU.Load1 = 4.01

			decision := EvaluateColdPilot(snapshot, hostReserve, pool())
			if decision.PilotReady {
				t.Fatal("load above the reserve must still block the operator gate in both modes")
			}
			if healthy := HealthyForRuntimeAdmission(decision); healthy != wantHealthy {
				t.Fatalf("runtime admission health is %v on a %s host, want %v", healthy, mode, wantHealthy)
			}
		})
	}
}
