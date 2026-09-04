package provider

import (
	"bytes"
	"context"
	"io"
	"testing"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func expectImageIdentity(cli *MockIncusServer, instanceName, _ string) {
	cli.On("CreateInstanceFile", instanceName, imageIdentityDirectory, mock.MatchedBy(func(args incus.InstanceFileArgs) bool {
		return args.Type == "directory" && args.UID == 0 && args.GID == 0 && args.Mode == 0o755 && args.WriteMode == "overwrite"
	})).Return(nil).Once()
	// Content is not inspected here: sibling CreateInstanceFile matchers
	// ReadAll the same bytes.Reader while testify diffs every expectation,
	// which empties the reader before this matcher runs. The dedicated
	// injectImageIdentity tests below prove the fingerprint bytes.
	cli.On("CreateInstanceFile", instanceName, imageIdentityPath, mock.MatchedBy(func(args incus.InstanceFileArgs) bool {
		return args.UID == 0 && args.GID == 0 && args.Mode == 0o644 && args.Type == "file" && args.WriteMode == "overwrite" && args.Content != nil
	})).Return(nil).Once()
}

func TestInjectImageIdentityWritesWorldReadableFingerprint(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	var got []byte
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityDirectory, mock.Anything).Return(nil).Once()
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityPath, mock.Anything).Run(func(args mock.Arguments) {
		file := args.Get(2).(incus.InstanceFileArgs)
		require.Equal(t, int64(0), file.UID)
		require.Equal(t, int64(0), file.GID)
		require.Equal(t, 0o644, int(file.Mode))
		require.Equal(t, "file", file.Type)
		body, err := io.ReadAll(file.Content)
		require.NoError(t, err)
		got = body
	}).Return(nil).Once()

	require.NoError(t, provider.injectImageIdentity(context.Background(), cli, "runner-test-instance", testImageDigest))
	require.Equal(t, testImageDigest+"\n", string(got))
	cli.AssertExpectations(t)
}

func TestInjectImageIdentityRefusesANonHexFingerprintWithoutTouchingTheGuest(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)

	err := provider.injectImageIdentity(context.Background(), cli, "runner-test-instance", "not-a-fingerprint")
	require.ErrorContains(t, err, "64-hex digest")
	cli.AssertNotCalled(t, "CreateInstanceFile", mock.Anything, mock.Anything, mock.Anything)
}

func TestInjectImageIdentityRetriesUntilTheGuestAcceptsTheFile(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	running := ownedInstance("runner-test-instance")
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityDirectory, mock.Anything).Return(nil).Twice()
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityPath, mock.Anything).Return(io.ErrUnexpectedEOF).Once()
	cli.On("GetInstanceFull", "runner-test-instance").Return(running, "", nil).Once()
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityPath, mock.MatchedBy(func(args incus.InstanceFileArgs) bool {
		content, err := io.ReadAll(args.Content)
		return err == nil && args.Mode == 0o644 && bytes.Equal(bytes.TrimSpace(content), []byte(testImageDigest))
	})).Return(nil).Once()

	require.NoError(t, provider.injectImageIdentity(context.Background(), cli, "runner-test-instance", testImageDigest))
	cli.AssertExpectations(t)
}

func TestInjectImageIdentityStopsWhenTheInstanceStops(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	stopped := ownedInstance("runner-test-instance")
	stopped.State = &api.InstanceState{Status: "Stopped"}
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityDirectory, mock.Anything).Return(nil).Once()
	cli.On("CreateInstanceFile", "runner-test-instance", imageIdentityPath, mock.Anything).Return(io.ErrUnexpectedEOF).Once()
	cli.On("GetInstanceFull", "runner-test-instance").Return(stopped, "", nil).Once()

	err := provider.injectImageIdentity(context.Background(), cli, "runner-test-instance", testImageDigest)
	require.ErrorContains(t, err, "instance stopped")
	cli.AssertExpectations(t)
}
