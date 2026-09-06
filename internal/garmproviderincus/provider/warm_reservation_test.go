package provider

import (
	"context"
	"errors"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type trackedWarmAdmission struct {
	allowAllAdmission
	reserved   map[string]bool
	released   []string
	releaseErr error
}

func (a *trackedWarmAdmission) AdmitWarm(_ context.Context, _ InstanceServerInterface, _, name string) (admission.Decision, error) {
	if a.reserved == nil {
		a.reserved = make(map[string]bool)
	}
	a.reserved[name] = true
	return admission.Decision{Admitted: true}, nil
}

func (a *trackedWarmAdmission) Release(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.released = append(a.released, name)
	if a.releaseErr != nil {
		return a.releaseErr
	}
	delete(a.reserved, name)
	return nil
}

func TestWarmPlacementDeferralReleasesReservationBeforeNextAttempt(t *testing.T) {
	cli := new(MockIncusServer)
	p := newTestProvider(cli)
	a := &trackedWarmAdmission{}
	p.admission = a
	prepareCreateMocks(cli, testImageDigest)
	cli.On("CreateInstance", mock.Anything).Return(new(MockOperation), errors.New(
		"Failed instance placement scriptlet: insufficient-memory: no fleet member has room for this worker",
	))
	for range 3 {
		name, consumed, deferred, err := p.createWarm(t.Context(), "nddev-linux-standard")
		require.NoError(t, err)
		require.Empty(t, name)
		require.False(t, consumed)
		require.NotNil(t, deferred)
		require.Equal(t, admission.ReasonPlacementRefused, deferred.Reason)
		require.Empty(t, a.reserved, "a refused warm create must not consume the next job's memory")
	}
	require.Len(t, a.released, 3)
	for _, name := range a.released {
		require.Contains(t, name, "warm-standard-")
	}
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}

func TestWarmPlacementDeferralReportsReleaseFailure(t *testing.T) {
	cli := new(MockIncusServer)
	p := newTestProvider(cli)
	a := &trackedWarmAdmission{releaseErr: errors.New("journal unavailable")}
	p.admission = a
	prepareCreateMocks(cli, testImageDigest)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cli.On("CreateInstance", mock.Anything).Run(func(mock.Arguments) { cancel() }).Return(new(MockOperation), errors.New(
		"Failed instance placement scriptlet: insufficient-memory: no fleet member has room for this worker",
	))
	_, _, _, err := p.createWarm(ctx, "nddev-linux-standard")
	require.ErrorContains(t, err, "journal unavailable")
	require.Len(t, a.released, 1, "cleanup must run even when the create context expired")
	require.Len(t, a.reserved, 1)
}

func TestWarmArgumentsFailureReleasesExactReservedIdentity(t *testing.T) {
	cli := new(MockIncusServer)
	p := newTestProvider(cli)
	a := &trackedWarmAdmission{}
	p.admission = a
	cli.On("GetProfileNames").Return([]string{}, errors.New("profile lookup unavailable"))
	_, _, _, err := p.createWarm(t.Context(), "nddev-linux-standard")
	require.Error(t, err)
	require.Empty(t, a.reserved)
	require.Len(t, a.released, 1)
	require.Contains(t, a.released[0], "warm-standard-")
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestWarmAmbiguousCreatePreservesReservationForReconciliation(t *testing.T) {
	cli := new(MockIncusServer)
	p := newTestProvider(cli)
	a := &trackedWarmAdmission{}
	p.admission = a
	prepareCreateMocks(cli, testImageDigest)
	cli.On("CreateInstance", mock.Anything).Return(new(MockOperation), errors.New("connection reset after request"))
	_, _, _, err := p.createWarm(t.Context(), "nddev-linux-standard")
	require.Error(t, err)
	require.Len(t, a.reserved, 1, "an ambiguous create may still allocate a worker")
	require.Empty(t, a.released)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}
