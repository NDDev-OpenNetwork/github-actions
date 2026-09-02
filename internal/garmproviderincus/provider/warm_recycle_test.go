package provider

import (
	"context"
	"testing"

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
