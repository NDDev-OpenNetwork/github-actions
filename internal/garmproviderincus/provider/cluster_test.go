package provider

import (
	"context"
	"testing"
	"time"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

func clusterPlatform() platformconfig.Config {
	var cfg platformconfig.Config
	cfg.Incus.StoragePool = "gha-lvm"
	cfg.Incus.ProjectMaxCPUUnits = 6
	cfg.Pressure = pressuregate.Policy{Required: true, StaleAfterSeconds: 90, HeartbeatSeconds: 30, MinimumClosedSeconds: 30,
		CPUSomeClose: 20, CPUSomeReopen: 10, MemoryFullClose: 1, MemoryFullReopen: .1,
		IOFullClose: 10, IOFullReopen: 5, MaximumRecentOOMKills: 0}
	return cfg
}

var clusterNow = time.Now().UTC()

func pressureMember(name, status, state string) api.ClusterMember {
	sample := pressuregate.Sample{ObservedAt: clusterNow, CPUSomeAvg10: 1}
	gate := pressuregate.State{SchemaVersion: 1, State: state, Reason: "healthy", StateSince: clusterNow, ObservedAt: clusterNow}
	return api.ClusterMember{ClusterMemberPut: api.ClusterMemberPut{Config: pressuregate.Metadata(gate, sample)}, ServerName: name, Status: status}
}

func testPool() platformconfig.Pool {
	pool := platformconfig.Pool{Name: "nddev-linux-standard"}
	pool.Resources.VCPU = 2
	pool.Resources.MemoryMiB = 4096
	return pool
}

func clusteredServer() *api.Server {
	server := &api.Server{}
	server.Environment.ServerClustered = true
	return server
}

func memberState(totalGiB, freeGiB, bufferGiB uint64, poolTotal, poolUsed uint64) *api.ClusterMemberState {
	state := &api.ClusterMemberState{}
	state.SysInfo.TotalRAM = totalGiB * 1024 * 1024 * 1024
	state.SysInfo.FreeRAM = freeGiB * 1024 * 1024 * 1024
	state.SysInfo.BufferRAM = bufferGiB * 1024 * 1024 * 1024
	state.SysInfo.TotalSwap = 2 * 1024 * 1024 * 1024
	state.SysInfo.FreeSwap = 2 * 1024 * 1024 * 1024
	state.StoragePools = map[string]api.StoragePoolState{
		"gha-lvm": {ResourcesStoragePool: api.ResourcesStoragePool{
			Space: api.ResourcesStoragePoolSpace{Total: poolTotal, Used: poolUsed},
		}},
	}
	return state
}

// Clustered, the answer is the fleet's capacity, not the capacity of whichever
// member happens to run the provider -- which may be a host that runs no
// workers at all.
func TestFleetHostStateSumsOnlineClusterMembers(t *testing.T) {
	cli := &MockIncusServer{}
	cli.On("GetServer").Return(clusteredServer(), "", nil)
	cli.On("GetClusterMembers").Return([]api.ClusterMember{
		pressureMember("example-runner-3", "Online", pressuregate.StateOpen),
		pressureMember("example-runner-4", "Online", pressuregate.StateOpen),
		pressureMember("example-runner-2", "Offline", pressuregate.StateOpen),
	}, nil)
	cli.On("GetClusterMemberState", "example-runner-3").Return(memberState(16, 10, 1, 200, 100), "", nil)
	cli.On("GetClusterMemberState", "example-runner-4").Return(memberState(16, 12, 0, 200, 20), "", nil)

	platform := clusterPlatform()
	state, err := fleetHostState(context.Background(), cli, platform, testPool(), platform.Pressure)
	require.NoError(t, err)

	// Two online members at six schedulable CPU units each.
	require.Equal(t, 12, state.TotalCPUUnits)
	// Emergency swap is recovery headroom, not schedulable hard memory.
	require.Equal(t, 32*1024, state.TotalMemoryMiB)
	// Measured reservations consume the two members' physical envelope. Live
	// pressure and placement, not free+buffers without page cache, stop work.
	require.Equal(t, 32*1024, state.AvailableMemoryMiB)
	// The worst member, not the average: 50% free beats 90% free hiding it.
	require.Equal(t, 50, state.FreeDiskPercent)

	// The queue host's own hypervisor readiness is not consulted. It is a
	// different machine from the one that will run the worker, and on a
	// dedicated queue host every KVM check would fail and close admission for
	// a fleet that is healthy.
	require.True(t, state.Healthy)
	cli.AssertNotCalled(t, "GetClusterMemberState", "example-runner-2")
}

// A cluster with nothing online must refuse rather than report a fleet of zero
// capacity, which admission would reject as invalid input with a reason that
// says nothing about why.
func TestFleetHostStateRefusesWhenEveryMemberIsOffline(t *testing.T) {
	cli := &MockIncusServer{}
	cli.On("GetServer").Return(clusteredServer(), "", nil)
	cli.On("GetClusterMembers").Return([]api.ClusterMember{
		pressureMember("example-runner-3", "Offline", pressuregate.StateOpen),
	}, nil)

	platform := clusterPlatform()
	_, err := fleetHostState(context.Background(), cli, platform, testPool(), platform.Pressure)
	require.ErrorContains(t, err, "no Incus cluster member is online")
}

// A member that does not report the fleet's pool cannot be assessed, and the
// disk rule fails closed everywhere else it is used.
func TestFleetHostStateTreatsAnUnreportedPoolAsFull(t *testing.T) {
	blank := &api.ClusterMemberState{}
	blank.SysInfo.TotalRAM = 16 * 1024 * 1024 * 1024
	blank.StoragePools = map[string]api.StoragePoolState{}

	cli := &MockIncusServer{}
	cli.On("GetServer").Return(clusteredServer(), "", nil)
	cli.On("GetClusterMembers").Return([]api.ClusterMember{pressureMember("example-runner-3", "Online", pressuregate.StateOpen)}, nil)
	cli.On("GetClusterMemberState", "example-runner-3").Return(blank, "", nil)

	platform := clusterPlatform()
	state, err := fleetHostState(context.Background(), cli, platform, testPool(), platform.Pressure)
	require.NoError(t, err)
	require.Equal(t, 0, state.FreeDiskPercent)
}

func TestFleetHostStateExcludesClosedMemberAndFailsClosedOnStaleMetadata(t *testing.T) {
	platform := clusterPlatform()
	for name, members := range map[string][]api.ClusterMember{
		"closed member": {
			pressureMember("example-runner-3", "Online", pressuregate.StateOpen),
			pressureMember("example-runner-4", "Online", pressuregate.StateClosed),
		},
		"stale member": {
			pressureMember("example-runner-3", "Online", pressuregate.StateOpen),
			{ClusterMemberPut: api.ClusterMemberPut{Config: pressuregate.Metadata(
				pressuregate.State{SchemaVersion: 1, State: pressuregate.StateOpen, StateSince: clusterNow.Add(-time.Minute)},
				pressuregate.Sample{ObservedAt: clusterNow.Add(-time.Minute)},
			)}, ServerName: "example-runner-4", Status: "Online"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			cli := &MockIncusServer{}
			cli.On("GetServer").Return(clusteredServer(), "", nil)
			cli.On("GetClusterMembers").Return(members, nil)
			cli.On("GetClusterMemberState", "example-runner-3").Return(memberState(16, 10, 0, 200, 20), "", nil)
			cli.On("GetClusterMemberState", "example-runner-4").Return(memberState(16, 10, 0, 200, 20), "", nil)
			state, err := fleetHostState(context.Background(), cli, platform, testPool(), platform.Pressure)
			require.NoError(t, err)
			if name == "closed member" {
				require.True(t, state.Healthy)
				require.Equal(t, 6, state.TotalCPUUnits)
			} else {
				require.False(t, state.Healthy)
				require.False(t, state.PressureAvailable)
			}
		})
	}
}

func TestFleetHostStateAllowsTwoPhaseUpgradeBeforePressurePolicyIsEnabled(t *testing.T) {
	platform := clusterPlatform()
	platform.Pressure = pressuregate.Policy{}
	cli := &MockIncusServer{}
	cli.On("GetServer").Return(clusteredServer(), "", nil)
	cli.On("GetClusterMembers").Return([]api.ClusterMember{{ServerName: "example-runner-3", Status: "Online"}}, nil)
	cli.On("GetClusterMemberState", "example-runner-3").Return(memberState(16, 10, 0, 200, 20), "", nil)
	state, err := fleetHostState(context.Background(), cli, platform, testPool(), platform.Pressure)
	require.NoError(t, err)
	require.True(t, state.Healthy)
	require.Equal(t, 6, state.TotalCPUUnits)
}
