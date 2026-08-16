# Per-member placement for the NDDev GitHub Actions fleet.
#
# A cluster's project limits are one fleet-wide total, not a per-member share.
# Incus' built-in scheduler picks the member with the fewest instances and never
# looks at their size, so on its own it will put a second ten-gibibyte worker on
# a host that cannot hold it while a larger idle host sits empty. This scriptlet
# is what keeps each member inside what its host can actually run, and it is the
# reason the project quota can safely be stated as the fleet total.
#
# Incus 6.0 exposes get_cluster_member_state, get_cluster_member_resources,
# get_instance_resources, set_target and the log_* helpers. It does not expose
# instance listings, so the ceiling is expressed in memory rather than in a
# count -- which is the more honest constraint anyway: these workers are sized
# in memory, memory is not overcommitted (guardrails.allow_memory_ballooning is
# false), and a host that has room for the memory has room for the worker.

PROJECT = "gha-fleet"
POOL = "gha-lvm"

# Left for the host itself: the Incus daemon, the collector, the observer, the
# registry and cache services, and the page cache a running worker needs.
# Matches host_reserve.minimum_memory_mib in the platform configuration.
HOST_MEMORY_RESERVE_BYTES = 2048 * 1024 * 1024

def instance_placement(request, candidate_members):
    # Any project this fleet does not own keeps Incus' built-in behaviour.
    if request.project != PROJECT:
        return

    want = get_instance_resources()
    needed_memory = want.memory_size + HOST_MEMORY_RESERVE_BYTES

    chosen = ""
    chosen_free = -1

    for member in candidate_members:
        name = member.server_name
        state = get_cluster_member_state(name)

        # Reclaimable page cache counts as free for this purpose; counting only
        # free_ram would refuse a host that has merely been busy reading images.
        free_memory = state.sysinfo.free_ram + state.sysinfo.buffered_ram
        if free_memory < needed_memory:
            continue

        pool = state.storage_pools[POOL]
        free_disk = pool.space.total - pool.space.used
        if free_disk < want.root_disk_size:
            continue

        # Rank by free memory. It is the signal that actually moves with
        # load: a running worker holds its whole allocation, because memory is
        # not overcommitted here.
        #
        # Storage pool usage was tried first and is not usable for this. The
        # pool is LVM thin, so every member reports the whole thin pool as used
        # and the four differ by around 200 MB out of 50 GB -- noise, which
        # made placement effectively arbitrary and concentrated three
        # consecutive workers on one member while three hosts sat empty.
        #
        # The known weakness of ranking on memory is a burst: an instance that
        # has been created but not yet started holds nothing, so several
        # creates racing through here can all see the same free member. The
        # provider starts an instance immediately after creating it, which
        # closes that window to seconds, and the guard above is what keeps the
        # window safe rather than the ranking -- a member without room for the
        # allocation is skipped whatever its rank.
        if free_memory > chosen_free:
            chosen = name
            chosen_free = free_memory

    if chosen == "":
        # Refusing is the point. The provider's own admission answers first, so
        # reaching here means the fleet is genuinely full; placing anyway would
        # oversubscribe a host that cannot run the work.
        log_warn("fleet placement refused: no member has ", needed_memory, " bytes of memory free")
        fail("no fleet member has room for this worker")

    log_info("fleet placement: ", chosen, " free_memory=", chosen_free, " needed=", needed_memory)
    set_target(chosen)
