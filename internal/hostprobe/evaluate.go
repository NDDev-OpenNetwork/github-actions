package hostprobe

import (
	"fmt"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityBlocker Severity = "blocker"
)

type Finding struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
}

type Decision struct {
	PilotReady              bool      `json:"pilot_ready"`
	Pool                    string    `json:"pool"`
	HostReserveMode         string    `json:"host_reserve_mode,omitempty"`
	RequiredCPUUnits        int       `json:"required_cpu_units"`
	RequiredMemoryMiB       int       `json:"required_memory_mib"`
	RequiredFreeDiskPercent int       `json:"required_free_disk_percent"`
	Findings                []Finding `json:"findings"`
}

// EvaluateColdPilot fails closed against the same host reserve and pool
// resource policy used by runtime admission. Legacy listeners and workers are
// reported but are not treated as a blocker because coexistence is required.
func EvaluateColdPilot(snapshot Snapshot, reserve config.HostReserve, pool config.Pool) Decision {
	return evaluateBuildHost(snapshot, reserve, pool, true, true, "VM pilot")
}

// EvaluateContainerImageBuild verifies the host that will build and smoke an
// Incus container image. It deliberately excludes KVM and nested-KVM: neither
// participates in a container build. A pending host reboot remains visible but
// cannot invalidate image bytes produced entirely in the container userspace;
// runtime capacity and failed fleet dependencies still fail closed.
func EvaluateContainerImageBuild(snapshot Snapshot, reserve config.HostReserve, pool config.Pool) Decision {
	return evaluateBuildHost(snapshot, reserve, pool, false, false, "container image build")
}

func evaluateBuildHost(snapshot Snapshot, reserve config.HostReserve, pool config.Pool, requireKVM, rebootBlocks bool, operation string) Decision {
	cpuReserve := max(reserve.MinimumCPUUnits, ceilingPercent(snapshot.CPU.PhysicalCores, reserve.MinimumPercent))
	memoryReserve := max(reserve.MinimumMemoryMiB, ceilingPercent(snapshot.Memory.TotalMiB, reserve.MinimumPercent))
	decision := Decision{
		PilotReady:              true,
		Pool:                    pool.Name,
		HostReserveMode:         reserve.Mode,
		RequiredCPUUnits:        cpuReserve + pool.Resources.VCPU,
		RequiredMemoryMiB:       memoryReserve + pool.Resources.MemoryMiB,
		RequiredFreeDiskPercent: reserve.MinimumFreeDiskPercent,
		Findings:                make([]Finding, 0),
	}
	add := func(code string, severity Severity, message string) {
		decision.Findings = append(decision.Findings, Finding{Code: code, Severity: severity, Message: message})
		if severity == SeverityBlocker {
			decision.PilotReady = false
		}
	}

	if snapshot.SchemaVersion != 1 {
		add("snapshot-schema", SeverityBlocker, "host snapshot schema must be 1")
	}
	if snapshot.OperatingSystem.ID != "ubuntu" || snapshot.OperatingSystem.VersionID != "24.04" {
		add("unsupported-operating-system", SeverityBlocker, "pilot requires Ubuntu 24.04")
	}
	if !oneOf(snapshot.OperatingSystem.Architecture, "x86_64", "amd64") {
		add("unsupported-architecture", SeverityBlocker, "pilot requires x86_64")
	}
	if snapshot.CPU.PhysicalCores < decision.RequiredCPUUnits {
		add("insufficient-cpu", SeverityBlocker, fmt.Sprintf("need %d non-overcommitted CPU units, observed %d", decision.RequiredCPUUnits, snapshot.CPU.PhysicalCores))
	}
	if snapshot.CPU.Load1 > float64(cpuReserve) {
		add("host-cpu-pressure", SeverityBlocker, fmt.Sprintf("one-minute load %.2f exceeds the %d-unit coexistence reserve", snapshot.CPU.Load1, cpuReserve))
	}
	if snapshot.Memory.TotalMiB < decision.RequiredMemoryMiB {
		add("insufficient-memory", SeverityBlocker, fmt.Sprintf("need %d MiB total memory, observed %d MiB", decision.RequiredMemoryMiB, snapshot.Memory.TotalMiB))
	}
	if snapshot.Memory.AvailableMiB < decision.RequiredMemoryMiB {
		add("insufficient-available-memory", SeverityBlocker, fmt.Sprintf("need %d MiB currently available for the %s and reserve, observed %d MiB", decision.RequiredMemoryMiB, operation, snapshot.Memory.AvailableMiB))
	}
	if snapshot.RootFilesystem.FreePercent < reserve.MinimumFreeDiskPercent {
		add("disk-pressure", SeverityBlocker, fmt.Sprintf("need at least %d percent free disk, observed %d", reserve.MinimumFreeDiskPercent, snapshot.RootFilesystem.FreePercent))
	}
	if snapshot.RootFilesystem.FreeInodesPercent < reserve.MinimumFreeDiskPercent {
		add("inode-pressure", SeverityBlocker, fmt.Sprintf("need at least %d percent free inodes, observed %d", reserve.MinimumFreeDiskPercent, snapshot.RootFilesystem.FreeInodesPercent))
	}
	if snapshot.RootFilesystem.RotationalKnown && snapshot.RootFilesystem.Rotational {
		add("rotational-storage", SeverityWarning, "root storage is reported as rotational; image and cache latency must be benchmarked")
	}
	if requireKVM {
		if !snapshot.KVM.Present {
			add("kvm-missing", SeverityBlocker, "/dev/kvm is missing")
		} else if !snapshot.KVM.Accessible {
			add("kvm-inaccessible", SeverityBlocker, "/dev/kvm is not accessible to the preflight process")
		}
		if snapshot.Virtualization != "none" && !snapshot.KVM.Nested {
			add("nested-kvm-disabled", SeverityBlocker, "nested KVM is required on a virtual host")
		}
	}
	if snapshot.Maintenance.RebootRequired {
		severity := SeverityWarning
		if rebootBlocks {
			severity = SeverityBlocker
		}
		add("reboot-required", severity, "complete the pending host reboot before the next host lifecycle operation")
	}
	switch failed, required := snapshot.Maintenance.FailedUnits, requiredFailedUnits(snapshot.Maintenance.FailedUnits); {
	case snapshot.Maintenance.SystemState == "running":
	case snapshot.Maintenance.SystemState != "degraded":
		add("system-unhealthy", SeverityBlocker, fmt.Sprintf("systemd state is %q, want running", snapshot.Maintenance.SystemState))
	case len(failed) == 0:
		add("system-unhealthy", SeverityBlocker, "systemd is degraded and its failed units could not be enumerated")
	case len(required) > 0:
		add("required-service-failed", SeverityBlocker, fmt.Sprintf("fleet dependency failed: %s", strings.Join(required, ", ")))
	default:
		add("host-degraded-unrelated", SeverityWarning, fmt.Sprintf("systemd is degraded, but no fleet dependency failed: %s", strings.Join(failed, ", ")))
	}
	if !snapshot.Software.Incus.Present {
		add("incus-missing", SeverityBlocker, "Incus must be installed and initialized")
	}
	if snapshot.Virtualization != "none" {
		add("nested-virtualization", SeverityWarning, "the CI host is itself virtualized; provider guarantees and p95 variance must be measured")
	}
	if snapshot.LegacyRunners.Listeners > 0 {
		add("legacy-coexistence", SeverityInfo, fmt.Sprintf("preserving %d legacy listeners; %d workers were active at capture", snapshot.LegacyRunners.Listeners, snapshot.LegacyRunners.Workers))
	}
	return decision
}

// HealthyForRuntimeAdmission separates immutable/runtime host failures from
// resource pressure that the admission journal evaluates against the complete
// Incus allocation set. Capacity, live available memory, disk and inode
// findings remain fail-closed in admission; only their accounting moves to the
// layer that can atomically reserve or preempt speculative warm capacity.
//
// One-minute load is delegated only on a dedicated host, and the distinction
// is not cosmetic. On a retained-workloads host, load is the single signal
// about consumption the journal cannot see, because the legacy listeners and
// the retained application stacks are not fleet VMs; it must stay a blocker
// there. On a dedicated host the fleet is the only consumer, the journal
// already holds exact per-VM CPU-unit accounting against
// project_max_cpu_units, and the proxy contributes a feedback loop instead of
// safety: one 4-vCPU worker takes an 8-core host past any reserve on its own,
// so the host then refuses to replenish the warm VM it just consumed, and each
// refusal keeps load high enough to refuse the next one.
//
// It remains a blocker in the cold-pilot decision for both modes, because that
// is an operator gate rather than an automatic retry.
func HealthyForRuntimeAdmission(decision Decision) bool {
	for _, finding := range decision.Findings {
		if finding.Severity != SeverityBlocker {
			continue
		}
		switch finding.Code {
		case "insufficient-cpu", "insufficient-memory", "insufficient-available-memory", "disk-pressure", "inode-pressure":
			continue
		case "host-cpu-pressure":
			if decision.HostReserveMode != "dedicated" {
				return false
			}
			continue
		default:
			return false
		}
	}
	return true
}

// fleetServicePrefixes names the units whose failure genuinely makes a host
// unsafe to admit work onto: without them a VM cannot be created, cannot be
// assigned a job, or cannot reach the control plane. Anything else may fail
// without closing admission. A degraded systemd aggregate is a diagnostic
// signal, not a capacity one, and treating it as a blocker stopped the entire
// fleet for unrelated units -- including the transient systemd-run scopes that
// diagnosis itself leaves behind.
//
// The list is deliberately short, and each exclusion is a decision:
//
//   - the cache services are absent. A cache outage is allowed to fall back to
//     an uncached build, which docs/cache-plane.md states as policy. Both
//     production hosts sat degraded on a failed gha-zot while every VM they
//     could have run was unaffected.
//   - the observer and the telemetry collector are absent. Losing sight of a
//     fleet is not the same as the fleet being unable to work.
//   - the diagnostic exporter is absent. Its failure must block teardown,
//     where the evidence contract applies, not provisioning, where it does not.
//   - the warm-pool reconciler is absent. It is a timer-driven oneshot, so it
//     is inactive between runs by design, and a failure stops replenishment
//     rather than making the host unsafe for the job already admitted.
var fleetServicePrefixes = []string{
	"garm",
	"gha-fleet-gateway",
	"incus",
	"lxcfs",
}

// requiredFailedUnits selects the failed units that are fleet dependencies.
func requiredFailedUnits(units []string) []string {
	required := make([]string, 0, len(units))
	for _, unit := range units {
		name := strings.TrimSuffix(unit, ".service")
		for _, prefix := range fleetServicePrefixes {
			if name == prefix || strings.HasPrefix(name, prefix+"-") || strings.HasPrefix(name, prefix+".") {
				required = append(required, unit)
				break
			}
		}
	}
	if len(required) == 0 {
		return nil
	}
	return required
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if strings.EqualFold(value, candidate) {
			return true
		}
	}
	return false
}

func ceilingPercent(total, value int) int {
	return (total*value + 99) / 100
}
