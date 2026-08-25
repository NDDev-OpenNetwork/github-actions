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
// disk and hard-memory checks; the least loaded candidate wins, with best-fit
// memory packing only as a tie-break between comparable CPU loads.
func Render(cfg config.Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	maxWorkerReservationMiB := 0
	reservationByLimit := make(map[int]int)
	for _, pool := range cfg.Pools {
		reservation := pool.EffectiveReservation()
		if previous, exists := reservationByLimit[pool.Resources.MemoryMiB]; exists && previous != reservation.MemoryMiB {
			return "", fmt.Errorf("placement requires one measured reservation per hard memory limit")
		}
		reservationByLimit[pool.Resources.MemoryMiB] = reservation.MemoryMiB
		if reservation.MemoryMiB > maxWorkerReservationMiB {
			maxWorkerReservationMiB = reservation.MemoryMiB
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
	for _, limit := range limits {
		entries = append(entries, fmt.Sprintf("%d: %d", limit, reservationByLimit[limit]))
	}

	values := map[string]string{
		"PROJECT":                strconv.Quote(cfg.Incus.Project),
		"POOL":                   strconv.Quote(cfg.Incus.StoragePool),
		"MINIMUM_MEMORY_MIB":     strconv.Itoa(cfg.HostReserve.MinimumMemoryMiB),
		"MINIMUM_MEMORY_PERCENT": strconv.Itoa(cfg.HostReserve.MinimumPercent),
		"MAX_WORKER_MEMORY_MIB":  strconv.Itoa(maxWorkerReservationMiB),
		"RESERVATION_BY_LIMIT":   "{" + strings.Join(entries, ", ") + "}",
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
# workers prefer the least loaded member because CPU is the scarce resource;
# best-fit memory packing breaks ties without overriding measured load.

PROJECT = {{PROJECT}}
POOL = {{POOL}}
MINIMUM_MEMORY_BYTES = {{MINIMUM_MEMORY_MIB}} * 1024 * 1024
MINIMUM_MEMORY_PERCENT = {{MINIMUM_MEMORY_PERCENT}}
MAX_WORKER_MEMORY_BYTES = {{MAX_WORKER_MEMORY_MIB}} * 1024 * 1024
RESERVATION_BY_LIMIT_MIB = {{RESERVATION_BY_LIMIT}}
PRESSURE_SCHEMA = {{PRESSURE_SCHEMA}}
PRESSURE_OPEN = {{PRESSURE_OPEN}}
LOAD_TIE_EPSILON = 0.05

def memory_reserve_bytes(total):
    percent = (total * MINIMUM_MEMORY_PERCENT + 99) // 100
    if percent > MINIMUM_MEMORY_BYTES:
        return percent
    return MINIMUM_MEMORY_BYTES

def committed_memory_bytes(member_name, pending_count):
    committed = 0
    instances = get_instances(PROJECT, member_name)
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
    chosen = ""
    chosen_remaining = -1
    chosen_count = -1
    chosen_load = -1.0

    for member in candidate_members:
        name = member.server_name
        if member.config.get("user.gha_pressure.schema", "") != PRESSURE_SCHEMA:
            continue
        if member.config.get("user.gha_pressure.state", "") != PRESSURE_OPEN:
            continue

        state = get_cluster_member_state(name)
        resources = get_cluster_member_resources(name)
        pool = state.storage_pools[POOL]
        if pool.space.total - pool.space.used < want.root_disk_size:
            continue

        pending_count = get_instances_count(PROJECT, name, True)
        committed = committed_memory_bytes(name, pending_count)
        reserve = memory_reserve_bytes(resources.memory.total)
        remaining = resources.memory.total - committed - want_reservation_bytes - reserve
        if remaining < 0:
            continue

        member_load = load_per_core(state, resources)
        better = False
        if chosen == "":
            better = True
        elif member_load < chosen_load - LOAD_TIE_EPSILON:
            better = True
        elif member_load <= chosen_load + LOAD_TIE_EPSILON:
            if remaining < chosen_remaining:
                better = True
            elif remaining == chosen_remaining and pending_count > chosen_count:
                better = True

        if better:
            chosen = name
            chosen_remaining = remaining
            chosen_count = pending_count
            chosen_load = member_load

    if chosen == "":
        log_warn("fleet placement refused: no member has safe capacity")
        fail("insufficient-memory: no fleet member has room for this worker")

    log_info("fleet placement: ", chosen, " load_per_core=", chosen_load, " remaining_memory=", chosen_remaining, " pending_count=", chosen_count)
    set_target(chosen)
`
