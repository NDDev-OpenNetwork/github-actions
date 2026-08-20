package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/hostprobe"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
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
	pressure pressuregate.Policy,
) (admission.HostSnapshot, error) {
	server, _, err := cli.GetServer()
	if err != nil {
		return admission.HostSnapshot{}, fmt.Errorf("read Incus server state: %w", err)
	}
	if server.Environment.ServerClustered {
		return clusterHostState(cli, platform, pressure, time.Now().UTC())
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
		PressureAvailable:  snapshot.Pressure.Available,
		CPUSomeAvg10:       snapshot.Pressure.CPU.Some.Avg10,
		MemoryFullAvg10:    snapshot.Pressure.Memory.Full.Avg10,
		IOFullAvg10:        snapshot.Pressure.IO.Full.Avg10,
	}, nil
}

func clusterHostState(cli InstanceServerInterface, platform platformconfig.Config, pressure pressuregate.Policy, now time.Time) (admission.HostSnapshot, error) {
	members, err := cli.GetClusterMembers()
	if err != nil {
		return admission.HostSnapshot{}, fmt.Errorf("list Incus cluster members: %w", err)
	}
	snapshot := admission.HostSnapshot{FreeDiskPercent: 100, PressureAvailable: pressure.Required}
	fallback := admission.HostSnapshot{FreeDiskPercent: 100}
	online := 0
	eligible := 0
	invalidPressure := false
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
		memberPressure := pressuregate.State{State: pressuregate.StateOpen}
		sample := pressuregate.Sample{}
		var pressureErr error
		if pressure.Required {
			memberPressure, sample, pressureErr = pressuregate.ParseMetadata(member.Config, pressure, now)
			if pressureErr != nil {
				invalidPressure = true
			}
		}
		// Admission commits hard worker memory. Emergency swap is recovery
		// headroom for a transient spike, never another pool of schedulable RAM.
		memberMemoryMiB := int(state.SysInfo.TotalRAM / (1024 * 1024))
		memberAvailableMiB := int((state.SysInfo.FreeRAM + state.SysInfo.BufferRAM) / (1024 * 1024))
		memberDisk := memberFreeDiskPercent(state, platform.Incus.StoragePool)
		fallback.TotalCPUUnits += platform.Incus.ProjectMaxCPUUnits
		fallback.TotalMemoryMiB += memberMemoryMiB
		fallback.AvailableMemoryMiB += memberAvailableMiB
		if memberDisk < fallback.FreeDiskPercent {
			fallback.FreeDiskPercent = memberDisk
		}
		if pressureErr != nil || memberPressure.State != pressuregate.StateOpen {
			continue
		}
		eligible++
		snapshot.TotalCPUUnits += platform.Incus.ProjectMaxCPUUnits
		snapshot.TotalMemoryMiB += memberMemoryMiB
		// Reclaimable page cache is available to a new worker; free_ram alone
		// reads a member that has merely been reading images as full.
		snapshot.AvailableMemoryMiB += memberAvailableMiB
		if memberDisk < snapshot.FreeDiskPercent {
			snapshot.FreeDiskPercent = memberDisk
		}
		snapshot.CPUSomeAvg10 = max(snapshot.CPUSomeAvg10, sample.CPUSomeAvg10)
		snapshot.MemoryFullAvg10 = max(snapshot.MemoryFullAvg10, sample.MemoryFullAvg10)
		snapshot.IOFullAvg10 = max(snapshot.IOFullAvg10, sample.IOFullAvg10)
	}
	if online == 0 {
		return admission.HostSnapshot{}, fmt.Errorf("no Incus cluster member is online")
	}
	if pressure.Required && (invalidPressure || eligible == 0) {
		fallback.Healthy = false
		fallback.PressureAvailable = !invalidPressure
		return fallback, nil
	}
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
