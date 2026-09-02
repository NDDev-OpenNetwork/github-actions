package provider

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/cloudbase/garm-provider-common/errors"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// A warm instance stamped by a previous release is an outdated disposable:
// the reconciler recycles it and refills at the current identity, instead of
// failing closed until an operator deletes it by hand -- the ritual every
// provider bump (.103, .105, .106) and image wave used to require.
func TestReconcileWarmRecyclesAnOutdatedIdentityInsteadOfFailing(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	stale := warmInstance("warm-standard-stale")
	stale.Config[providerVersionKey] = "v0.1.5-nddev.90"
	stale.ExpandedConfig[providerVersionKey] = "v0.1.5-nddev.90"
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{*stale}, nil).Once()

	// Dry run: the plan names the recycle, deletes nothing.
	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.NoError(t, err)
	require.Equal(t, []string{"warm-standard-stale"}, result.RecycledStale)
	require.Equal(t, 0, result.ReadyBefore)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}

// A stale IMAGE fingerprint is the same outdated class -- this is the warm
// surface of an image wave recycling itself.
func TestReconcileWarmRecyclesAStaleImageFingerprint(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	stale := warmInstance("warm-standard-oldimage")
	stale.Config[imageFingerprintKey] = "0000000000000000000000000000000000000000000000000000000000000000"
	stale.ExpandedConfig[imageFingerprintKey] = "0000000000000000000000000000000000000000000000000000000000000000"
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{*stale}, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.NoError(t, err)
	require.Equal(t, []string{"warm-standard-oldimage"}, result.RecycledStale)
}

// A boundary violation -- wrong trust, foreign ownership -- is NOT currency
// and must keep failing loudly; recycling must never absorb it.
func TestReconcileWarmStillFailsOnABoundaryViolation(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	poisoned := warmInstance("warm-standard-poisoned")
	poisoned.Config[trustKey] = "untrusted"
	poisoned.ExpandedConfig[trustKey] = "untrusted"
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{*poisoned}, nil).Once()

	_, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "validating warm-ready inventory")
}

// Under apply, an outdated warm instance is deleted and, with the target at
// zero, nothing refills -- the recycle is its own complete story. The old
// contract made these two rows hard errors; they are currency now.
func TestReconcileWarmAppliesTheRecycleForStaleIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*api.InstanceFull){
		"previous-provider-version": func(instance *api.InstanceFull) {
			instance.Config[providerVersionKey] = "v0.1.5-nddev.6"
			instance.ExpandedConfig[providerVersionKey] = "v0.1.5-nddev.6"
		},
		"previous-provider-commit": func(instance *api.InstanceFull) {
			instance.Config[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
			instance.ExpandedConfig[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
		},
	} {
		t.Run(name, func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			setWarmTarget(provider, "nddev-linux-standard", 0)
			stale := warmInstance("warm-standard-stale")
			mutate(stale)
			cli.On("GetInstancesFull", api.InstanceTypeAny).
				Return([]api.InstanceFull{*stale}, nil).Once()
			op := new(MockOperation)
			op.On("WaitContext", mock.Anything).Return(nil)
			cli.On("GetInstanceFull", "warm-standard-stale").Return(stale, "", nil)
			cli.On("UpdateInstanceState", "warm-standard-stale",
				api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").Return(op, nil).Once()
			cli.On("DeleteInstance", "warm-standard-stale").Return(op, nil).Once()

			result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
			require.NoError(t, err)
			require.Equal(t, []string{"warm-standard-stale"}, result.RecycledStale)
			cli.AssertCalled(t, "DeleteInstance", "warm-standard-stale")
		})
	}
}

// A pool capability moved under a warm instance -- the priority pools took
// cache_write_scope trusted on 2026-09-02 -- and the instance prepared under
// the old value is outdated, not a boundary violation: recycled, not a
// reconcile that stalls on the one instance only a reconcile could remove.
func TestReconcileWarmRecyclesAnInstanceWhosePoolCapabilityMoved(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	moved := warmInstance("warm-standard-moved")
	moved.Config[cacheWriteScopeKey] = "none"
	moved.ExpandedConfig[cacheWriteScopeKey] = "none"
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{*moved}, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.NoError(t, err)
	require.Equal(t, []string{"warm-standard-moved"}, result.RecycledStale)
	require.Equal(t, 0, result.ReadyBefore)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}

// One warm instance that never publishes readiness is deleted and recorded,
// and the pool's reconcile carries on: failing it instead left the depth
// unrestored during exactly the bursts that consume it (2026-09-02, eleven
// aborted reconciles).
func TestWarmInstanceThatNeverBecomesReadyIsAbandonedNotFatal(t *testing.T) {
	previous := stateOperationTimeout
	stateOperationTimeout = 20 * time.Millisecond
	t.Cleanup(func() { stateOperationTimeout = previous })

	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil).Once()
	prepareCreateMocks(cli, testImageDigest)
	operation := new(MockOperation)
	operation.On("WaitContext", mock.Anything).Return(nil)
	cli.On("CreateInstance", mock.Anything).Return(operation, nil).Once()
	cli.On("UpdateInstanceState", mock.Anything, api.InstanceStatePut{Action: "start", Timeout: -1}, "").Return(operation, nil).Maybe()
	// The instance is created and reachable, but its readiness evidence never
	// appears, so every poll finds nothing and the wait times out.
	created := warmInstance("warm-standard-unready")
	created.ExpandedConfig[lifecycleKey] = lifecycleWarmPreparing
	created.ExpandedConfig[warmReadyKey] = ""
	created.State = &api.InstanceState{Status: "Running", Network: map[string]api.InstanceStateNetwork{
		"eth0": {Addresses: []api.InstanceStateNetworkAddress{{Family: "inet", Scope: "global", Address: "10.0.0.5"}}},
	}}
	cli.On("GetInstanceFull", mock.Anything).Return(created, "etag", nil).Maybe()
	cli.On("GetInstanceFile", mock.Anything, warmReadyGuestPath).
		Return(io.NopCloser(strings.NewReader("")), (*incus.InstanceFileResponse)(nil), fmt.Errorf("evidence absent: %w", errors.ErrNotFound)).Maybe()
	cli.On("DeleteInstance", mock.Anything).Return(operation, nil).Maybe()
	cli.On("UpdateInstanceState", mock.Anything, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").Return(operation, nil).Maybe()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
	require.NoError(t, err, "one unready instance must not fail the pool")
	require.Len(t, result.Abandoned, 1)
	require.Empty(t, result.Created)
}

// Incus answers a file read on an instance whose delete has begun with
// "Instance storage pool not found", not with a 404; that is the instance
// going away, not a broken pool.
func TestAVanishingInstanceIsRecognisedByItsStoragePoolError(t *testing.T) {
	require.True(t, warmInstanceRetiredDuringCreate(fmt.Errorf("reading warm readiness evidence: Failed getting instance pool: Instance storage pool not found")))
	require.True(t, warmInstanceRetiredDuringCreate(fmt.Errorf("attempt count exceeded: fetching instance: Instance not found")))
	require.False(t, warmInstanceRetiredDuringCreate(fmt.Errorf("storage pool is full")))
	require.False(t, warmInstanceRetiredDuringCreate(nil))
}

// The preparing loop reads each instance's readiness evidence. One that is
// being deleted answers with the storage-pool error; the pass must skip it
// and keep reconciling, not abort the pool (2026-09-02, 10:59Z).
func TestPreparingInstanceThatVanishesMidReadIsSkipped(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 0)
	preparing := warmInstance("warm-standard-vanishing")
	preparing.ExpandedConfig[lifecycleKey] = lifecycleWarmPreparing
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*preparing}, nil).Once()
	cli.On("GetInstanceFull", preparing.Name).Return(preparing, "etag", nil).Maybe()
	cli.On("GetInstanceFile", preparing.Name, warmReadyGuestPath).
		Return(io.NopCloser(strings.NewReader("")), (*incus.InstanceFileResponse)(nil),
			fmt.Errorf("Failed getting instance pool: Instance storage pool not found")).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
	require.NoError(t, err, "an instance that vanished mid-read must not fail the pool")
	require.Empty(t, result.Promoted)
}
