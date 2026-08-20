package provider

import (
	"context"
	"testing"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

func clusterPlatform() platformconfig.Config {
	var cfg platformconfig.Config
	cfg.Incus.StoragePool = "gha-lvm"
	cfg.Incus.ProjectMaxCPUUnits = 6
	return cfg
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
		{ServerName: "gha-runner-3", Status: "Online"},
		{ServerName: "gha-runner-4", Status: "Online"},
		{ServerName: "gha-runner-2", Status: "Offline"},
	}, nil)
	cli.On("GetClusterMemberState", "gha-runner-3").Return(memberState(16, 10, 1, 200, 100), "", nil)
	cli.On("GetClusterMemberState", "gha-runner-4").Return(memberState(16, 12, 0, 200, 20), "", nil)

	state, err := fleetHostState(context.Background(), cli, clusterPlatform(), testPool())
	require.NoError(t, err)

	// Two online members at six schedulable CPU units each.
	require.Equal(t, 12, state.TotalCPUUnits)
	// Emergency swap is recovery headroom, not schedulable hard memory.
	require.Equal(t, 32*1024, state.TotalMemoryMiB)
	// Free plus reclaimable cache, excluding swap: (10+1) + (12+0) GiB.
	require.Equal(t, 23*1024, state.AvailableMemoryMiB)
	// The worst member, not the average: 50% free beats 90% free hiding it.
	require.Equal(t, 50, state.FreeDiskPercent)

	// The queue host's own hypervisor readiness is not consulted. It is a
	// different machine from the one that will run the worker, and on a
	// dedicated queue host every KVM check would fail and close admission for
	// a fleet that is healthy.
	require.True(t, state.Healthy)
	cli.AssertNotCalled(t, "GetClusterMemberState", "gha-runner-2")
}

// A cluster with nothing online must refuse rather than report a fleet of zero
// capacity, which admission would reject as invalid input with a reason that
// says nothing about why.
func TestFleetHostStateRefusesWhenEveryMemberIsOffline(t *testing.T) {
	cli := &MockIncusServer{}
	cli.On("GetServer").Return(clusteredServer(), "", nil)
	cli.On("GetClusterMembers").Return([]api.ClusterMember{
		{ServerName: "gha-runner-3", Status: "Offline"},
	}, nil)

	_, err := fleetHostState(context.Background(), cli, clusterPlatform(), testPool())
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
	cli.On("GetClusterMembers").Return([]api.ClusterMember{{ServerName: "gha-runner-3", Status: "Online"}}, nil)
	cli.On("GetClusterMemberState", "gha-runner-3").Return(blank, "", nil)

	state, err := fleetHostState(context.Background(), cli, clusterPlatform(), testPool())
	require.NoError(t, err)
	require.Equal(t, 0, state.FreeDiskPercent)
}
