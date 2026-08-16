package provider

import (
	"context"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/lxc/incus/v7/shared/api"
)

// fleetHostState answers "does this fleet have room", which is a different
// question from "does this machine have room" the moment more than one machine
// is behind the queue.
//
// On a standalone host the two coincide and the local probe is the better
// source: it reads one-minute load and the free-inode percentage, neither of
// which Incus reports, and the machine it reads is the machine that will run
// the worker.
//
// Clustered, the local probe is not merely imprecise -- it is about the wrong
// machine. The queue may run on a host that is deliberately not a hypervisor
// at all, and every hypervisor blocker the cold probe raises there (no KVM, no
// Incus service, a fleet unit that host does not run) would close admission for
// a fleet that is perfectly healthy. So the clustered path never probes
// locally. Capacity is summed across online members, and whether any given
// member can take this particular worker is the placement scriptlet's
// decision, made at placement time with every member's live state in view.
func fleetHostState(
	ctx context.Context,
	cli InstanceServerInterface,
	platform platformconfig.Config,
	pool platformconfig.Pool,
) (admission.HostSnapshot, error) {
	server, _, err := cli.GetServer()
	if err != nil {
		return admission.HostSnapshot{}, fmt.Errorf("read Incus server state: %w", err)
	}
	if server.Environment.ServerClustered {
		return clusterHostState(cli, platform)
	}

	snapshot, err := hostprobe.Collect(ctx)
	if err != nil {
		return admission.HostSnapshot{}, fmt.Errorf("collect host state: %w", err)
	}
	cold := hostprobe.EvaluateColdPilot(snapshot, platform.HostReserve, pool)
	return admission.HostSnapshot{
		Healthy:            hostprobe.HealthyForRuntimeAdmission(cold),
		TotalCPUUnits:      snapshot.CPU.PhysicalCores,
		TotalMemoryMiB:     snapshot.Memory.TotalMiB,
		AvailableMemoryMiB: snapshot.Memory.AvailableMiB,
		FreeDiskPercent:    min(snapshot.RootFilesystem.FreePercent, snapshot.RootFilesystem.FreeInodesPercent),
	}, nil
}

func clusterHostState(cli InstanceServerInterface, platform platformconfig.Config) (admission.HostSnapshot, error) {
	members, err := cli.GetClusterMembers()
	if err != nil {
		return admission.HostSnapshot{}, fmt.Errorf("list Incus cluster members: %w", err)
	}
	snapshot := admission.HostSnapshot{FreeDiskPercent: 100}
	online := 0
	for _, member := range members {
		// An offline member's capacity is not the fleet's capacity. Counting it
		// would admit work to a host that cannot take it, and the placement
		// scriptlet would then refuse every candidate.
		if member.Status != "Online" {
			continue
		}
		state, _, err := cli.GetClusterMemberState(member.ServerName)
		if err != nil {
			return admission.HostSnapshot{}, fmt.Errorf("read cluster member %q state: %w", member.ServerName, err)
		}
		online++
		snapshot.TotalCPUUnits += platform.Incus.ProjectMaxCPUUnits
		snapshot.TotalMemoryMiB += int(state.SysInfo.TotalRAM / (1024 * 1024))
		// Reclaimable page cache is available to a new worker; free_ram alone
		// reads a member that has merely been reading images as full.
		snapshot.AvailableMemoryMiB += int((state.SysInfo.FreeRAM + state.SysInfo.BufferRAM) / (1024 * 1024))
		if percent := memberFreeDiskPercent(state, platform.Incus.StoragePool); percent < snapshot.FreeDiskPercent {
			snapshot.FreeDiskPercent = percent
		}
	}
	if online == 0 {
		return admission.HostSnapshot{}, fmt.Errorf("no Incus cluster member is online")
	}
	// One online member is a fleet that can run work. Which member, and whether
	// it is the right one, is decided at placement.
	snapshot.Healthy = true
	return snapshot, nil
}

// memberFreeDiskPercent reports the worst member rather than the average. Disk
// pressure is a property of the machine that would run the worker, and an
// average would let three empty members hide one that is full.
func memberFreeDiskPercent(state *api.ClusterMemberState, pool string) int {
	space, exists := state.StoragePools[pool]
	if !exists || space.Space.Total == 0 {
		// A member that does not report the fleet pool cannot be assessed, and
		// this rule fails closed everywhere else it is used.
		return 0
	}
	free := space.Space.Total - space.Space.Used
	return int(free * 100 / space.Space.Total)
}
