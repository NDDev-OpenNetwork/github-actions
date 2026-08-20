package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func testCacheDelivery() rustfscache.Delivery {
	return rustfscache.Delivery{
		Role:      "trusted-writer",
		Endpoint:  "https://198.51.100.1:9002",
		Region:    "us-east-1",
		Bucket:    "github-actions-cache",
		Prefix:    "example-org/example-actions/trust/trusted",
		Mode:      "read-write",
		AccessKey: "AK" + "IA0123456789ABCDEF",
		SecretKey: []byte(strings.Repeat("s", 64)),
		CAPEM:     []byte("-----BEGIN CERTIFICATE-----\nfixture\n-----END CERTIFICATE-----\n"),
	}
}

func TestCacheRoleMappingIsExactAndPromoterIsNeverDelivered(t *testing.T) {
	tests := []struct {
		pool    platformconfig.Pool
		role    string
		enabled bool
		wantErr bool
	}{
		{platformconfig.Pool{Name: "trusted", Trust: "trusted", Capabilities: platformconfig.Capabilities{CacheWriteScope: "trusted"}}, "trusted-writer", true, false},
		{platformconfig.Pool{Name: "untrusted", Trust: "untrusted", Capabilities: platformconfig.Capabilities{CacheWriteScope: "isolated"}}, "untrusted-writer", true, false},
		{platformconfig.Pool{Name: "release", Trust: "release", Capabilities: platformconfig.Capabilities{CacheWriteScope: "none"}}, "release-reader", true, false},
		{platformconfig.Pool{Name: "disabled", Trust: "trusted", Capabilities: platformconfig.Capabilities{CacheWriteScope: "none"}}, "", false, false},
		{platformconfig.Pool{Name: "drift", Trust: "trusted", Capabilities: platformconfig.Capabilities{CacheWriteScope: "isolated"}}, "", false, true},
	}
	for _, test := range tests {
		role, enabled, err := cacheRoleForPool(test.pool)
		require.Equal(t, test.wantErr, err != nil, test.pool.Name)
		require.Equal(t, test.role, role, test.pool.Name)
		require.Equal(t, test.enabled, enabled, test.pool.Name)
		require.NotEqual(t, "promoter", role)
	}
}

func TestCacheDeliveryFailsOpenWithoutCacheForAnotherRepository(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	loads := 0
	provider.cacheDelivery = func(string) (rustfscache.Delivery, error) {
		loads++
		return testCacheDelivery(), nil
	}
	bootstrap := validBootstrap()
	bootstrap.RepoURL = "https://github.com/example-org/github-device-sync"
	raw, enabled, err := provider.renderCacheAssignment(bootstrap)
	require.NoError(t, err)
	require.False(t, enabled)
	require.Nil(t, raw)
	require.Zero(t, loads, "a foreign repository must not load the github-actions credential")
}

func TestRenderedAssignmentBindsOneJobAndContainsNoSecretInCloudConfig(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	delivery := testCacheDelivery()
	provider.cacheDelivery = func(role string) (rustfscache.Delivery, error) {
		require.Equal(t, "trusted-writer", role)
		copy := delivery
		copy.SecretKey = append([]byte(nil), delivery.SecretKey...)
		copy.CAPEM = append([]byte(nil), delivery.CAPEM...)
		return copy, nil
	}
	bootstrap := validBootstrap()
	raw, enabled, err := provider.renderCacheAssignment(bootstrap)
	require.NoError(t, err)
	require.True(t, enabled)
	defer clear(raw)
	var assignment workerCacheAssignment
	require.NoError(t, json.Unmarshal(raw, &assignment))
	require.Equal(t, cacheDeliveryID(bootstrap.Name), assignment.DeliveryID)
	require.Equal(t, delivery.AccessKey, assignment.AccessKey)
	decoded, err := base64.StdEncoding.DecodeString(assignment.SecretKeyB64)
	require.NoError(t, err)
	require.Equal(t, delivery.SecretKey, decoded)
	clear(decoded)

	cli := new(MockIncusServer)
	provider.cli = cli
	prepareCreateMocks(cli, testImageDigest)
	originalToolFetch := DefaultToolFetch
	originalCloudConfig := DefaultGetCloudconfig
	t.Cleanup(func() {
		DefaultToolFetch = originalToolFetch
		DefaultGetCloudconfig = originalCloudConfig
	})
	DefaultToolFetch = func(_ commonParams.OSType, _ commonParams.OSArch, tools []commonParams.RunnerApplicationDownload) (commonParams.RunnerApplicationDownload, error) {
		return tools[0], nil
	}
	var captured commonParams.BootstrapInstance
	DefaultGetCloudconfig = func(params commonParams.BootstrapInstance, _ commonParams.RunnerApplicationDownload, _ string) (string, error) {
		captured = params
		return "#cloud-config", nil
	}
	args, err := provider.getCreateInstanceArgs(context.Background(), bootstrap, extraSpecs{DisableUpdates: true})
	require.NoError(t, err)
	userData := args.Config["user.user-data"]
	require.NotContains(t, userData, delivery.AccessKey)
	require.NotContains(t, userData, string(delivery.SecretKey))
	specs, err := parseExtraSpecsFromBootstrapParams(captured)
	require.NoError(t, err)
	cacheSetup := string(specs.PreInstallScripts["00-nddev-cache-delivery.sh"])
	require.Contains(t, cacheSetup, "timed out waiting for one-job cache delivery")
	require.Contains(t, cacheSetup, `cat /etc/ssl/certs/ca-certificates.crt "${ca_temp}"`)
	require.Contains(t, cacheSetup, "SSL_CERT_FILE=")
	require.Contains(t, cacheSetup, "CURL_CA_BUNDLE=")
	require.Contains(t, cacheSetup, "AWS_CA_BUNDLE=")
	require.NotContains(t, cacheSetup, "update-ca-certificates")
	require.NotContains(t, cacheSetup, "/usr/local/share/ca-certificates")
	require.NotContains(t, cacheSetup, delivery.AccessKey)
	require.NotContains(t, cacheSetup, string(delivery.SecretKey))
}

func TestColdDeliveryWritesSecretBeforeZeroByteReadinessMarker(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	delivery := testCacheDelivery()
	provider.cacheDelivery = func(string) (rustfscache.Delivery, error) {
		copy := delivery
		copy.SecretKey = append([]byte(nil), delivery.SecretKey...)
		copy.CAPEM = append([]byte(nil), delivery.CAPEM...)
		return copy, nil
	}
	bootstrap := validBootstrap()
	var order []string
	provider.cli.(*MockIncusServer).On("CreateInstanceFile", bootstrap.Name, cacheAssignmentPath, mock.Anything).
		Run(func(arguments mock.Arguments) {
			args := arguments.Get(2).(incus.InstanceFileArgs)
			raw, err := io.ReadAll(args.Content)
			require.NoError(t, err)
			require.Equal(t, int64(0), args.UID)
			require.Equal(t, int64(0), args.GID)
			require.Equal(t, 0o400, args.Mode)
			require.Contains(t, string(raw), delivery.AccessKey)
			require.NotContains(t, raw, delivery.SecretKey)
			order = append(order, "assignment")
		}).Return(nil).Once()
	provider.cli.(*MockIncusServer).On("CreateInstanceFile", bootstrap.Name, cacheReadyPath, mock.Anything).
		Run(func(arguments mock.Arguments) {
			args := arguments.Get(2).(incus.InstanceFileArgs)
			raw, err := io.ReadAll(args.Content)
			require.NoError(t, err)
			require.Empty(t, raw)
			require.Equal(t, int64(0), args.UID)
			require.Equal(t, int64(0), args.GID)
			require.Equal(t, 0o400, args.Mode)
			order = append(order, "ready")
		}).Return(nil).Once()

	require.NoError(t, provider.injectColdCacheAssignment(context.Background(), bootstrap.Name, bootstrap))
	require.Equal(t, []string{"assignment", "ready"}, order)
}

func TestColdDeliveryStopsRetryingWhenCanceledInstanceStops(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	delivery := testCacheDelivery()
	provider.cacheDelivery = func(string) (rustfscache.Delivery, error) {
		copy := delivery
		copy.SecretKey = append([]byte(nil), delivery.SecretKey...)
		copy.CAPEM = append([]byte(nil), delivery.CAPEM...)
		return copy, nil
	}
	bootstrap := validBootstrap()
	provider.cli.(*MockIncusServer).On("CreateInstanceFile", bootstrap.Name, cacheAssignmentPath, mock.Anything).
		Return(errors.New("Instance is not running")).Once()
	provider.cli.(*MockIncusServer).On("GetInstanceFull", bootstrap.Name).
		Return(&api.InstanceFull{State: &api.InstanceState{Status: "Stopped"}}, "", nil).Once()

	err := provider.injectColdCacheAssignment(context.Background(), bootstrap.Name, bootstrap)
	require.ErrorContains(t, err, "instance stopped during canceled create")
	provider.cli.(*MockIncusServer).AssertNumberOfCalls(t, "CreateInstanceFile", 1)
}

func TestCacheDeliveryOwnershipIsPinnedToTheExactImageIdentity(t *testing.T) {
	image := newTestProvider(new(MockIncusServer)).cfg.WorkerImages["nddev-linux-standard"]
	require.True(t, cacheDeliveryOwnerMatches(&incus.InstanceFileResponse{UID: 1001, GID: 1002}, image.RunnerUID, image.RunnerGID, false))
	require.False(t, cacheDeliveryOwnerMatches(&incus.InstanceFileResponse{UID: 1000, GID: 1000}, image.RunnerUID, image.RunnerGID, false))
	require.False(t, cacheDeliveryOwnerMatches(&incus.InstanceFileResponse{UID: 0, GID: 0}, image.RunnerUID, image.RunnerGID, false))
	require.True(t, cacheDeliveryOwnerMatches(&incus.InstanceFileResponse{UID: 0, GID: 0}, image.RunnerUID, image.RunnerGID, true))
	require.False(t, cacheDeliveryOwnerMatches(nil, image.RunnerUID, image.RunnerGID, true))
}

func TestJobStartedHookMasksExportsAndDeletesOneJobSecret(t *testing.T) {
	directory := t.TempDir()
	assignmentPath := filepath.Join(directory, "assignment.json")
	readyPath := filepath.Join(directory, "assignment.ready")
	consumedPath := filepath.Join(directory, "assignment.consumed")
	githubEnv := filepath.Join(directory, "github-env")
	delivery := testCacheDelivery()
	assignment := workerCacheAssignment{
		SchemaVersion: 1,
		DeliveryID:    strings.Repeat("a", 64),
		Role:          delivery.Role,
		Mode:          delivery.Mode,
		Endpoint:      delivery.Endpoint,
		Region:        delivery.Region,
		Bucket:        delivery.Bucket,
		PrefixRoot:    delivery.Prefix,
		AccessKey:     delivery.AccessKey,
		SecretKeyB64:  base64.StdEncoding.EncodeToString(delivery.SecretKey),
		CAPEMB64:      base64.StdEncoding.EncodeToString(delivery.CAPEM),
	}
	raw, err := json.Marshal(assignment)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(assignmentPath, raw, 0o400))
	require.NoError(t, os.WriteFile(readyPath, nil, 0o400))
	require.NoError(t, os.WriteFile(githubEnv, nil, 0o600))
	hook := strings.NewReplacer(
		cacheAssignmentPath, assignmentPath,
		cacheReadyPath, readyPath,
		cacheConsumedPath, consumedPath,
	).Replace(cacheJobStartedHook())
	command := exec.Command("bash", "-c", hook)
	command.Env = append(os.Environ(), "GITHUB_ENV="+githubEnv)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "::add-mask::"+delivery.AccessKey)
	require.Contains(t, string(output), "::add-mask::"+string(delivery.SecretKey))
	environment, err := os.ReadFile(githubEnv)
	require.NoError(t, err)
	require.Contains(t, string(environment), "AWS_ACCESS_KEY_ID="+delivery.AccessKey)
	require.Contains(t, string(environment), "AWS_SECRET_ACCESS_KEY="+string(delivery.SecretKey))
	require.Contains(t, string(environment), "NDDEV_CACHE_PREFIX_ROOT="+delivery.Prefix)
	_, err = os.Lstat(assignmentPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(readyPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	consumed, err := os.ReadFile(consumedPath)
	require.NoError(t, err)
	require.Equal(t, assignment.DeliveryID+"\n", string(consumed))
}

func TestCacheShellProgramsParseAndNeverEnableXtrace(t *testing.T) {
	for name, script := range map[string]string{
		"setup":  string(renderCacheSetupScript()),
		"hook":   cacheJobStartedHook(),
		"warm":   string(mergeCacheIntoWarmAssignment(renderWarmAssignment("opaque", nil, ""), []byte(`{"secret":"opaque"}`))),
		"direct": string(mergeCacheIntoWarmAssignment(renderWarmAssignment("", nil, testEncodedDirectJIT(t)), []byte(`{"secret":"opaque"}`))),
	} {
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(script)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s script: %s", name, output)
		require.NotContains(t, script, "set -x", name)
	}
}
