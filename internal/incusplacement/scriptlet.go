// Package incusplacement renders the cluster-wide Incus placement policy for
// disposable CI workers. Keeping the scriptlet here makes placement reviewed,
// reproducible desired state instead of an untracked live-server setting.
package incusplacement

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

const ServerConfigKey = "instances.placement.scriptlet"

// Render returns a portable Incus Starlark placement scriptlet derived only
// from typed public platform policy. Every candidate must first pass pressure,
// disk and hard-memory checks. Placement scores both work already promised to
// a member (including creates not visible in the instance list yet) and the
// measured load, so a burst cannot herd onto a member during load-average lag.
func Render(cfg config.Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	maxWorkerReservationMiB := 0
	maxWorkerCPUUnits := 0
	reservationByLimit := make(map[int]int)
	cpuByLimit := make(map[int]int)
	for _, pool := range cfg.Pools {
		reservation := pool.EffectiveReservation()
		if previous, exists := reservationByLimit[pool.Resources.MemoryMiB]; exists && previous != reservation.MemoryMiB {
			return "", fmt.Errorf("placement requires one measured reservation per hard memory limit")
		}
		reservationByLimit[pool.Resources.MemoryMiB] = reservation.MemoryMiB
		if previous, exists := cpuByLimit[pool.Resources.MemoryMiB]; exists && previous != pool.Resources.VCPU {
			return "", fmt.Errorf("placement requires one CPU allowance per hard memory limit")
		}
		cpuByLimit[pool.Resources.MemoryMiB] = pool.Resources.VCPU
		if reservation.MemoryMiB > maxWorkerReservationMiB {
			maxWorkerReservationMiB = reservation.MemoryMiB
		}
		if pool.Resources.VCPU > maxWorkerCPUUnits {
			maxWorkerCPUUnits = pool.Resources.VCPU
		}
	}
	if maxWorkerReservationMiB == 0 {
		return "", fmt.Errorf("placement requires at least one worker memory class")
	}
	limits := make([]int, 0, len(reservationByLimit))
	for limit := range reservationByLimit {
		limits = append(limits, limit)
	}
	sort.Ints(limits)
	entries := make([]string, 0, len(limits))
	cpuEntries := make([]string, 0, len(limits))
	for _, limit := range limits {
		entries = append(entries, fmt.Sprintf("%d: %d", limit, reservationByLimit[limit]))
		cpuEntries = append(cpuEntries, fmt.Sprintf("%d: %d", limit, cpuByLimit[limit]))
	}

	values := map[string]string{
		"PROJECT":                strconv.Quote(cfg.Incus.Project),
		"POOL":                   strconv.Quote(cfg.Incus.StoragePool),
		"MINIMUM_MEMORY_MIB":     strconv.Itoa(cfg.HostReserve.MinimumMemoryMiB),
		"MINIMUM_MEMORY_PERCENT": strconv.Itoa(cfg.HostReserve.MinimumPercent),
		"MAX_WORKER_MEMORY_MIB":  strconv.Itoa(maxWorkerReservationMiB),
		"MAX_WORKER_CPU_UNITS":   strconv.Itoa(maxWorkerCPUUnits),
		"RESERVATION_BY_LIMIT":   "{" + strings.Join(entries, ", ") + "}",
		"CPU_BY_LIMIT":           "{" + strings.Join(cpuEntries, ", ") + "}",
		"PRESSURE_SCHEMA":        strconv.Quote("1"),
		"PRESSURE_OPEN":          strconv.Quote("open"),
	}
	script := placementTemplate
	for key, value := range values {
		script = strings.ReplaceAll(script, "{{"+key+"}}", value)
	}
	if strings.Contains(script, "{{") {
		return "", fmt.Errorf("placement template contains an unresolved value")
	}
	return script, nil
}

const placementTemplate = `# Managed by NDDev github-actions. Do not edit live.
#
# Hard memory, pressure and disk checks are safety authorities. Eligible
# workers minimize the worse of promised CPU and measured CPU pressure. This
# keeps rapid parallel creates balanced before one-minute load catches up.

PROJECT = {{PROJECT}}
POOL = {{POOL}}
MINIMUM_MEMORY_BYTES = {{MINIMUM_MEMORY_MIB}} * 1024 * 1024
MINIMUM_MEMORY_PERCENT = {{MINIMUM_MEMORY_PERCENT}}
MAX_WORKER_MEMORY_BYTES = {{MAX_WORKER_MEMORY_MIB}} * 1024 * 1024
MAX_WORKER_CPU_UNITS = {{MAX_WORKER_CPU_UNITS}}
RESERVATION_BY_LIMIT_MIB = {{RESERVATION_BY_LIMIT}}
CPU_ALLOWANCE_BY_LIMIT_MIB = {{CPU_BY_LIMIT}}
PRESSURE_SCHEMA = {{PRESSURE_SCHEMA}}
PRESSURE_OPEN = {{PRESSURE_OPEN}}
# Exactly the two names internal/imageplan gives a build's instances.
MAINTENANCE_PREFIXES = ("gha-image-builder-", "gha-image-smoke-")
LOAD_TIE_EPSILON = 0.05

def memory_reserve_bytes(total):
    percent = (total * MINIMUM_MEMORY_PERCENT + 99) // 100
    if percent > MINIMUM_MEMORY_BYTES:
        return percent
    return MINIMUM_MEMORY_BYTES

def committed_memory_bytes(instances, pending_count):
    committed = 0
    for instance in instances:
        memory = instance.expanded_config.get("limits.memory", "")
        if not memory.endswith("MiB"):
            fail("fleet instance has non-MiB memory limit: " + instance.name)
        limit_mib = int(memory[:-3])
        if limit_mib not in RESERVATION_BY_LIMIT_MIB:
            fail("fleet instance has no memory reservation: " + instance.name)
        committed += RESERVATION_BY_LIMIT_MIB[limit_mib] * 1024 * 1024
    pending_without_record = pending_count - len(instances)
    if pending_without_record < 0:
        fail("fleet pending instance count is below committed records")
    return committed + pending_without_record * MAX_WORKER_MEMORY_BYTES

def committed_cpu_units(instances, pending_count):
    committed = 0
    for instance in instances:
        memory = instance.expanded_config.get("limits.memory", "")
        if not memory.endswith("MiB"):
            fail("fleet instance has non-MiB memory limit: " + instance.name)
        limit_mib = int(memory[:-3])
        if limit_mib not in CPU_ALLOWANCE_BY_LIMIT_MIB:
            fail("fleet instance has no CPU allowance: " + instance.name)
        committed += CPU_ALLOWANCE_BY_LIMIT_MIB[limit_mib]
    pending_without_record = pending_count - len(instances)
    if pending_without_record < 0:
        fail("fleet pending instance count is below committed records")
    return committed + pending_without_record * MAX_WORKER_CPU_UNITS

def load_per_core(state, resources):
    sysinfo = getattr(state, "sysinfo", None)
    if sysinfo == None:
        return 0.0
    averages = getattr(sysinfo, "load_averages", None)
    if averages == None or len(averages) == 0:
        return 0.0
    cpu = getattr(resources, "cpu", None)
    if cpu == None:
        return 0.0
    cores = getattr(cpu, "total", 0)
    if cores == None or cores <= 0:
        return 0.0
    return float(averages[0]) / float(cores)

def instance_placement(request, candidate_members):
    if request.project != PROJECT:
        return

    want = get_instance_resources()
    want_limit_mib = want.memory_size // (1024 * 1024)
    if want_limit_mib not in RESERVATION_BY_LIMIT_MIB:
        fail("requested worker has no measured memory reservation")
    want_reservation_bytes = RESERVATION_BY_LIMIT_MIB[want_limit_mib] * 1024 * 1024
    want_cpu_units = CPU_ALLOWANCE_BY_LIMIT_MIB[want_limit_mib]
    chosen = ""
    chosen_remaining = -1
    chosen_count = -1
    chosen_load = -1.0
    chosen_projected_cpu = -1.0
    chosen_score = -1.0

    # An image builder is maintenance, not a job, and it is placed deliberately
    # on a member that was drained for it. The pressure gate exists to keep new
    # work off a busy member; applying it here made drain and build jointly
    # unsatisfiable -- drain empties the member by closing its gate, the closed
    # gate removes it from every candidate list, and the build then reports
    # "no fleet member has room" about a member that is completely empty.
    #
    # Observed: gha-runner-2 drained to zero occupants, then a staged image
    # build refused with insufficient-memory naming an empty member.
    maintenance = request.name.startswith(MAINTENANCE_PREFIXES)

    for member in candidate_members:
        name = member.server_name
        if member.config.get("user.gha_pressure.schema", "") != PRESSURE_SCHEMA:
            continue
        if not maintenance and member.config.get("user.gha_pressure.state", "") != PRESSURE_OPEN:
            continue

        state = get_cluster_member_state(name)
        resources = get_cluster_member_resources(name)
        pool = state.storage_pools[POOL]
        if pool.space.total - pool.space.used < want.root_disk_size:
            continue

        pending_count = get_instances_count(PROJECT, name, True)
        instances = get_instances(PROJECT, name)
        committed = committed_memory_bytes(instances, pending_count)
        reserve = memory_reserve_bytes(resources.memory.total)
        remaining = resources.memory.total - committed - want_reservation_bytes - reserve
        if remaining < 0:
            continue

        member_load = load_per_core(state, resources)
        cores = resources.cpu.total
        if cores == None or cores <= 0:
            continue
        committed_cpu = committed_cpu_units(instances, pending_count)
        projected_cpu = float(committed_cpu + want_cpu_units) / float(cores)
        member_score = member_load
        if projected_cpu > member_score:
            member_score = projected_cpu
        better = False
        if chosen == "":
            better = True
        elif member_score < chosen_score - LOAD_TIE_EPSILON:
            better = True
        elif member_score <= chosen_score + LOAD_TIE_EPSILON:
            if projected_cpu < chosen_projected_cpu:
                better = True
            elif projected_cpu == chosen_projected_cpu and member_load < chosen_load:
                better = True
            elif projected_cpu == chosen_projected_cpu and member_load == chosen_load and remaining > chosen_remaining:
                better = True
            elif projected_cpu == chosen_projected_cpu and member_load == chosen_load and remaining == chosen_remaining and pending_count < chosen_count:
                better = True

        if better:
            chosen = name
            chosen_remaining = remaining
            chosen_count = pending_count
            chosen_load = member_load
            chosen_projected_cpu = projected_cpu
            chosen_score = member_score

    if chosen == "":
        log_warn("fleet placement refused: no member has safe capacity")
        fail("insufficient-memory: no fleet member has room for this worker")

    log_info("fleet placement: ", chosen, " score=", chosen_score, " projected_cpu=", chosen_projected_cpu, " load_per_core=", chosen_load, " remaining_memory=", chosen_remaining, " pending_count=", chosen_count)
    set_target(chosen)
`
