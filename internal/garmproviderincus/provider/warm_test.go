package provider

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func TestWarmCreateRecognizesConcurrentRetirement(t *testing.T) {
	require.True(t, warmInstanceRetiredDuringCreate(errors.Join(
		errors.New("waiting for warm instance network"),
		runnerErrors.ErrNotFound,
	)))
	require.False(t, warmInstanceRetiredDuringCreate(errors.New("Incus control plane unavailable")))
}

type denyWarmAdmission struct {
	allowAllAdmission
	decision admission.Decision
	err      error
}

func (d denyWarmAdmission) AdmitWarm(context.Context, InstanceServerInterface, string, string) (admission.Decision, error) {
	return d.decision, d.err
}

func setWarmTarget(provider *Incus, flavor string, target int) {
	for index := range provider.platform.Pools {
		if provider.platform.Pools[index].Name == flavor {
			provider.platform.Pools[index].Warm.TargetReady = target
			provider.platform.Pools[index].Warm.MaxReady = 1
		}
	}
}

func preparingWarmInstance(name string) *api.InstanceFull {
	instance := warmInstance(name)
	instance.Config[lifecycleKey] = lifecycleWarmPreparing
	instance.Config[warmReadyKey] = "false"
	instance.ExpandedConfig[lifecycleKey] = lifecycleWarmPreparing
	instance.ExpandedConfig[warmReadyKey] = "false"
	return instance
}

func TestReconcileWarmDryRunDoesNotMutateDeficit(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, 1, result.TargetReady)
	require.Zero(t, result.ReadyBefore)
	require.Zero(t, result.ReadyAfter)
	require.Empty(t, result.Created)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

// A dry run is the output an operator reads before authorising a destructive
// converge. DeletedExcess was appended only under apply, so the plan silently
// reduced the ready set to the target and reported deleting nothing -- the list
// was empty exactly when it was being consulted, and full only after the
// deletions had already happened.
func TestReconcileWarmDryRunNamesTheExcessItWouldDelete(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	first := warmInstance("warm-standard-0001")
	second := warmInstance("warm-standard-0002")
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{*first, *second}, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", false)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.Equal(t, 2, result.ReadyBefore)
	require.Equal(t, 1, result.ReadyAfter)
	require.Equal(t, []string{"warm-standard-0002"}, result.DeletedExcess)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestReconcileWarmReportsAdmissionBackpressureAsStructuredNoOp(t *testing.T) {
	reasons := []admission.Reason{
		admission.ReasonHostUnhealthy,
		admission.ReasonDiskPressure,
		admission.ReasonPoolSaturated,
		admission.ReasonInsufficientCPU,
		admission.ReasonInsufficientMemory,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			setWarmTarget(provider, "nddev-linux-standard", 1)
			decision := admission.Decision{
				Reason:                reason,
				Pool:                  "nddev-linux-standard",
				RequiredCPUReserve:    4,
				RequiredMemoryReserve: 16384,
				RemainingCPUUnits:     0,
				RemainingMemoryMiB:    32768,
			}
			provider.admission = denyWarmAdmission{decision: decision}
			cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil).Once()

			result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
			require.NoError(t, err)
			require.True(t, result.Applied)
			require.True(t, result.Deferred)
			require.Equal(t, reason, result.DeferralReason)
			require.Equal(t, &decision, result.AdmissionDecision)
			require.Zero(t, result.ReadyAfter)
			require.Empty(t, result.Created)
			cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
		})
	}
}

// A pending job must not stop the fleet from building the warm capacity that
// would serve the next one. The gate this replaces refused warm creation while
// any intent existed in any scale set, and the fleet is rarely idle: over six
// hours it deferred 510 of 634 reconciles and warm depth was zero in 631 of
// them, so every job cold-started, which lengthened the queue, which kept the
// gate shut. Capacity is still protected -- AdmitWarm runs the pool, CPU,
// memory and pressure checks, warm creation is bounded by max_ready, and a real
// job claims a warm instance rather than waiting behind it.
func TestReconcileWarmBuildsCapacityWhileJobsAreQueued(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	provider.admission = &warmAdmission{}
	instance := preparingWarmInstance("warm-standard-preparing")
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Once()
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*instance}, nil).Once()
	cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil).Once()
	cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
		io.NopCloser(strings.NewReader(warmReadyEvidence)),
		&incus.InstanceFileResponse{UID: 0, GID: 0, Mode: 0o644, Type: "file"},
		nil,
	).Once()
	cli.On("UpdateInstance", instance.Name, mock.Anything, "etag-warm").Return(op, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
	require.NoError(t, err)
	require.False(t, result.Deferred)
	require.Empty(t, result.DeferralReason)
	require.Equal(t, []string{instance.Name}, result.Promoted)
	require.Equal(t, 1, result.ReadyAfter)
	cli.AssertExpectations(t)
}

func TestReconcileWarmStillFailsOnAdmissionEvaluationError(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setWarmTarget(provider, "nddev-linux-standard", 1)
	provider.admission = denyWarmAdmission{err: errors.New("host probe failed")}
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil).Once()

	result, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
	require.ErrorContains(t, err, "evaluating warm capacity")
	require.False(t, result.Deferred)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestDrainWarmAllowsPreviousReleaseOnlyThroughNormalTeardown(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	diagnostics := &recordingDiagnostics{}
	provider.diagnostics = diagnostics
	instance := warmInstance("warm-standard-0001")
	instance.Config[providerVersionKey] = "v0.1.5-nddev.6"
	instance.ExpandedConfig[providerVersionKey] = "v0.1.5-nddev.6"
	instance.Config[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
	instance.ExpandedConfig[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
	instance.Config["boot.autostart"] = "false"
	instance.ExpandedConfig["boot.autostart"] = "false"
	stopOperation := new(MockOperation)
	stopOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	deleteOperation := new(MockOperation)
	deleteOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Times(3)
	cli.On("UpdateInstanceState", instance.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").
		Return(stopOperation, nil).Once()
	cli.On("DeleteInstance", instance.Name).Return(deleteOperation, nil).Once()

	result, err := provider.DrainWarm(context.Background(), "nddev-linux-standard", instance.Name, true)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.True(t, result.Deleted)
	require.True(t, diagnostics.called)
	cli.AssertExpectations(t)
}

func TestDrainWarmDryRunDoesNotMutate(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := warmInstance("warm-standard-0001")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Once()

	result, err := provider.DrainWarm(context.Background(), "nddev-linux-standard", instance.Name, false)
	require.NoError(t, err)
	require.False(t, result.Applied)
	require.False(t, result.Deleted)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}

func TestDrainWarmRejectsAssignedOrForeignInstance(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*api.InstanceFull)
	}{
		{
			name: "assigned",
			mutate: func(instance *api.InstanceFull) {
				instance.Config[lifecycleKey] = lifecycleEphemeralOneJob
				instance.ExpandedConfig[lifecycleKey] = lifecycleEphemeralOneJob
				instance.Config[repositoryKey] = "example-user/github-actions"
				instance.ExpandedConfig[repositoryKey] = "example-user/github-actions"
			},
		},
		{
			name: "foreign",
			mutate: func(instance *api.InstanceFull) {
				instance.Config[controllerIDKeyName] = "other-controller"
				instance.ExpandedConfig[controllerIDKeyName] = "other-controller"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			instance := warmInstance("warm-standard-0001")
			test.mutate(instance)
			cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Once()

			_, err := provider.DrainWarm(context.Background(), "nddev-linux-standard", instance.Name, true)
			require.Error(t, err)
			cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
			cli.AssertNotCalled(t, "UpdateInstanceState", mock.Anything, mock.Anything, mock.Anything)
		})
	}
}

func TestReconcileWarmRejectsStoppedOrNonAutostartReadyInventory(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*api.InstanceFull)
	}{
		{
			name: "stopped",
			mutate: func(instance *api.InstanceFull) {
				instance.State.Status = "Stopped"
			},
		},
		{
			name: "autostart-disabled",
			mutate: func(instance *api.InstanceFull) {
				instance.Config["boot.autostart"] = "false"
				instance.ExpandedConfig["boot.autostart"] = "false"
			},
		},
		{
			name: "previous-provider-version",
			mutate: func(instance *api.InstanceFull) {
				instance.Config[providerVersionKey] = "v0.1.5-nddev.6"
				instance.ExpandedConfig[providerVersionKey] = "v0.1.5-nddev.6"
			},
		},
		{
			name: "previous-provider-commit",
			mutate: func(instance *api.InstanceFull) {
				instance.Config[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
				instance.ExpandedConfig[providerCommitKey] = "4b95d7fa39d3c68e496aa5c14958af175f166773"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			setWarmTarget(provider, "nddev-linux-standard", 1)
			instance := warmInstance("warm-standard-0001")
			test.mutate(instance)
			cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*instance}, nil).Once()

			_, err := provider.ReconcileWarm(context.Background(), "nddev-linux-standard", true)
			require.ErrorContains(t, err, "validating warm-ready inventory")
			cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
			cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
		})
	}
}

func TestPromoteWarmReadyRequiresExactRootOwnedGuestEvidence(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := preparingWarmInstance("warm-standard-0001")
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Once()
	cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil).Once()
	cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
		io.NopCloser(strings.NewReader(warmReadyEvidence)),
		&incus.InstanceFileResponse{UID: 0, GID: 0, Mode: 0o644, Type: "file"},
		nil,
	).Once()
	cli.On("UpdateInstance", instance.Name, mock.MatchedBy(func(update api.InstancePut) bool {
		return update.Config[lifecycleKey] == lifecycleWarmUnregistered && update.Config[warmReadyKey] == "true" &&
			update.Config[repositoryKey] == "" && update.Config[garmJobNameKey] == ""
	}), "etag-warm").Return(op, nil).Once()

	promoted, err := provider.promoteWarmReady(context.Background(), instance.Name, "nddev-linux-standard")
	require.NoError(t, err)
	require.True(t, promoted)
	cli.AssertExpectations(t)
}

func TestPromoteWarmReadyRejectsMutableOrNonRootEvidence(t *testing.T) {
	for _, response := range []*incus.InstanceFileResponse{
		{UID: 1000, GID: 0, Mode: 0o644, Type: "file"},
		{UID: 0, GID: 0, Mode: 0o666, Type: "file"},
	} {
		cli := new(MockIncusServer)
		provider := newTestProvider(cli)
		instance := preparingWarmInstance("warm-standard-0001")
		cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil).Once()
		cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
			io.NopCloser(strings.NewReader(warmReadyEvidence)), response, nil,
		).Once()
		promoted, err := provider.promoteWarmReady(context.Background(), instance.Name, "nddev-linux-standard")
		require.False(t, promoted)
		require.ErrorContains(t, err, "invalid warm readiness evidence")
		cli.AssertNotCalled(t, "UpdateInstance", mock.Anything, mock.Anything, mock.Anything)
	}
}

func TestPromoteWarmReadyDistinguishesAbsentEvidenceFromAgentFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		fileError error
		wantError bool
	}{
		{name: "not-yet-ready", fileError: os.ErrNotExist},
		{name: "agent-failure", fileError: errors.New("incus agent unavailable"), wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			instance := preparingWarmInstance("warm-standard-0001")
			cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil).Once()
			cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
				(io.ReadCloser)(nil), (*incus.InstanceFileResponse)(nil), test.fileError,
			).Once()
			promoted, err := provider.promoteWarmReady(context.Background(), instance.Name, "nddev-linux-standard")
			require.False(t, promoted)
			if test.wantError {
				require.ErrorContains(t, err, "reading warm readiness evidence")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestWaitWarmReadyPollsUntilExactEvidenceAppears(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := preparingWarmInstance("warm-standard-0001")
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Once()
	cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil).Twice()
	cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
		(io.ReadCloser)(nil), (*incus.InstanceFileResponse)(nil), os.ErrNotExist,
	).Once()
	cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
		io.NopCloser(strings.NewReader(warmReadyEvidence)),
		&incus.InstanceFileResponse{UID: 0, GID: 0, Mode: 0o644, Type: "file"},
		nil,
	).Once()
	cli.On("UpdateInstance", instance.Name, mock.Anything, "etag-warm").Return(op, nil).Once()

	require.NoError(t, provider.waitWarmReady(context.Background(), instance.Name, "nddev-linux-standard", time.Millisecond))
	cli.AssertExpectations(t)
}

func TestWaitWarmReadyHonorsContextDeadline(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := preparingWarmInstance("warm-standard-0001")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "etag-warm", nil)
	cli.On("GetInstanceFile", instance.Name, warmReadyGuestPath).Return(
		(io.ReadCloser)(nil), (*incus.InstanceFileResponse)(nil), os.ErrNotExist,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	require.ErrorContains(t, provider.waitWarmReady(ctx, instance.Name, "nddev-linux-standard", time.Millisecond), "did not publish readiness evidence")
}

func TestWarmCreateArgsContainNoJobOrRegistrationIdentity(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, testImageDigest)
	args, err := provider.getWarmCreateArgs(context.Background(), "nddev-linux-standard", "warm-standard-0001")
	require.NoError(t, err)
	require.Equal(t, lifecycleWarmPreparing, args.Config[lifecycleKey])
	require.Equal(t, "true", args.Config["boot.autostart"])
	require.Equal(t, warmPoolIDPrefix+"nddev-linux-standard", args.Config[poolIDKey])
	require.Empty(t, args.Config[repositoryKey])
	require.Empty(t, args.Config[garmJobNameKey])
	require.NotContains(t, args.Config["user.user-data"], "token")
	require.NotContains(t, args.Config["user.user-data"], "runner")
}
