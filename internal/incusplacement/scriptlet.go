// Package incusplacement renders the cluster-wide Incus placement policy for
// disposable CI workers. Keeping the scriptlet here makes placement reviewed,
// reproducible desired state instead of an untracked live-server setting.
package incusplacement

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

const ServerConfigKey = "instances.placement.scriptlet"

// Render returns a portable Incus Starlark placement scriptlet derived only
// from typed public platform policy. It uses best-fit decreasing placement:
// every candidate must first pass pressure, disk and hard-memory checks, then
// the worker is packed into the candidate with the least safe memory left.
func Render(cfg config.Config) (string, error) {
	if err := cfg.Validate(); err != nil {
		return "", err
	}
	maxWorkerMemoryMiB := 0
	for _, pool := range cfg.Pools {
		if pool.Resources.MemoryMiB > maxWorkerMemoryMiB {
			maxWorkerMemoryMiB = pool.Resources.MemoryMiB
		}
	}
	if maxWorkerMemoryMiB == 0 {
		return "", fmt.Errorf("placement requires at least one worker memory class")
	}

	values := map[string]string{
		"PROJECT":                strconv.Quote(cfg.Incus.Project),
		"POOL":                   strconv.Quote(cfg.Incus.StoragePool),
		"MINIMUM_MEMORY_MIB":     strconv.Itoa(cfg.HostReserve.MinimumMemoryMiB),
		"MINIMUM_MEMORY_PERCENT": strconv.Itoa(cfg.HostReserve.MinimumPercent),
		"MAX_WORKER_MEMORY_MIB":  strconv.Itoa(maxWorkerMemoryMiB),
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
# workers use best-fit packing so small jobs do not fragment every member and
# block a later large job while physical capacity remains idle.

PROJECT = {{PROJECT}}
POOL = {{POOL}}
MINIMUM_MEMORY_BYTES = {{MINIMUM_MEMORY_MIB}} * 1024 * 1024
MINIMUM_MEMORY_PERCENT = {{MINIMUM_MEMORY_PERCENT}}
MAX_WORKER_MEMORY_BYTES = {{MAX_WORKER_MEMORY_MIB}} * 1024 * 1024
PRESSURE_SCHEMA = {{PRESSURE_SCHEMA}}
PRESSURE_OPEN = {{PRESSURE_OPEN}}

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
        committed += int(memory[:-3]) * 1024 * 1024
    pending_without_record = pending_count - len(instances)
    if pending_without_record < 0:
        fail("fleet pending instance count is below committed records")
    return committed + pending_without_record * MAX_WORKER_MEMORY_BYTES

def instance_placement(request, candidate_members):
    if request.project != PROJECT:
        return

    want = get_instance_resources()
    chosen = ""
    chosen_remaining = -1
    chosen_count = -1

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
        remaining = resources.memory.total - committed - want.memory_size - reserve
        if remaining < 0:
            continue

        # Best fit preserves the largest untouched member for a later large
        # request. Pending count breaks equal-memory ties toward packing too.
        if chosen == "" or remaining < chosen_remaining or (remaining == chosen_remaining and pending_count > chosen_count):
            chosen = name
            chosen_remaining = remaining
            chosen_count = pending_count

    if chosen == "":
        log_warn("fleet placement refused: no member has safe capacity")
        fail("insufficient-memory: no fleet member has room for this worker")

    log_info("fleet best-fit placement: ", chosen, " remaining_memory=", chosen_remaining, " pending_count=", chosen_count)
    set_target(chosen)
`
