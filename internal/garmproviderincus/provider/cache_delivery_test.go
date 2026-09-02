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
	"sort"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachebroker"
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

func configureTestCacheClaim(t *testing.T, provider *Incus) cachebroker.Store {
	t.Helper()
	directory := t.TempDir()
	store := cachebroker.Store{Path: filepath.Join(directory, "claims.json"), LockPath: filepath.Join(directory, "claims.lock")}
	provider.cacheClaim = func() (cachebroker.Store, string, []byte, error) {
		return store, "https://198.51.100.1:9443/api/v1/cache/claim", []byte("fixture-ca"), nil
	}
	provider.cacheClaimRandom = strings.NewReader(strings.Repeat("r", cachebroker.ClaimTokenBytes))
	return store
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
		{platformconfig.Pool{Name: "correlation", Trust: "trusted", Capabilities: platformconfig.Capabilities{CacheWriteScope: "none"}}, "correlation-only", true, false},
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

func TestOrganizationBootstrapInjectsOneTimeClaimWithoutCacheSecret(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	store := configureTestCacheClaim(t, provider)
	bootstrap := validBootstrap()
	bootstrap.RepoURL = "https://github.com/example-org"
	var assignment []byte
	cli.On("CreateInstanceFile", bootstrap.Name, cacheAssignmentPath, mock.Anything).Run(func(arguments mock.Arguments) {
		args := arguments.Get(2).(incus.InstanceFileArgs)
		assignment, _ = io.ReadAll(args.Content)
	}).Return(nil).Once()
	cli.On("CreateInstanceFile", bootstrap.Name, cacheReadyPath, mock.Anything).Return(nil).Once()
	require.NoError(t, provider.injectColdCacheAssignment(context.Background(), bootstrap.Name, bootstrap))
	var claim workerCacheClaim
	require.NoError(t, json.Unmarshal(assignment, &claim))
	require.Equal(t, 2, claim.SchemaVersion)
	require.Equal(t, bootstrap.Name, claim.InstanceName)
	require.NotContains(t, string(assignment), "AKIA")
	require.NotContains(t, string(assignment), strings.Repeat("s", 64))
	journal, err := store.Read(context.Background())
	require.NoError(t, err)
	require.Equal(t, "trusted-writer", journal.Claims[bootstrap.Name].Role)
}

func TestCacheClaimCarriesRustFSAndWorkerGatewayTrust(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	configureTestCacheClaim(t, provider)
	bootstrap := validBootstrap()
	bootstrap.CACertBundle = []byte("worker-gateway-ca")
	var assignment []byte
	cli.On("CreateInstanceFile", bootstrap.Name, cacheAssignmentPath, mock.Anything).Run(func(arguments mock.Arguments) {
		args := arguments.Get(2).(incus.InstanceFileArgs)
		assignment, _ = io.ReadAll(args.Content)
	}).Return(nil).Once()
	cli.On("CreateInstanceFile", bootstrap.Name, cacheReadyPath, mock.Anything).Return(nil).Once()

	require.NoError(t, provider.injectColdCacheAssignment(context.Background(), bootstrap.Name, bootstrap))
	var claim workerCacheClaim
	require.NoError(t, json.Unmarshal(assignment, &claim))
	trust, err := base64.StdEncoding.DecodeString(claim.CAPEMB64)
	require.NoError(t, err)
	require.Equal(t, "fixture-ca\nworker-gateway-ca", string(trust))
}

func TestCloudConfigContainsNoCacheSecret(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	configureTestCacheClaim(t, provider)
	bootstrap := validBootstrap()
	delivery := testCacheDelivery()

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

func TestCacheSetupAcceptsDistinctWarmProviderAndRunnerIdentity(t *testing.T) {
	script := string(renderCacheSetupScript())
	require.Contains(t, script, `"instance_name","runner_name","schema_version"`)
	require.Contains(t, script, `(.runner_name | test("^[a-z][a-z0-9-]{5,63}$"))`)
}

func TestExactBootstrapUsesAuthenticatedClaimBeforeZeroByteReadinessMarker(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	store := configureTestCacheClaim(t, provider)
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
			var claim workerCacheClaim
			require.NoError(t, json.Unmarshal(raw, &claim))
			require.Equal(t, 2, claim.SchemaVersion)
			require.NotContains(t, string(raw), "AKIA")
			require.NotContains(t, string(raw), strings.Repeat("s", 64))
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
	journal, err := store.Read(context.Background())
	require.NoError(t, err)
	require.Empty(t, journal.Claims[bootstrap.Name].ClaimedRepository)
}

func TestColdDeliveryStopsRetryingWhenCanceledInstanceStops(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	configureTestCacheClaim(t, provider)
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
	if err != nil {
		t.Fatalf("job-start hook failed: %v\n%s", err, output)
	}
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

func TestJobStartedHookClaimsRepositoryScopedDelivery(t *testing.T) {
	directory := t.TempDir()
	assignmentPath := filepath.Join(directory, "assignment.json")
	readyPath := filepath.Join(directory, "assignment.ready")
	consumedPath := filepath.Join(directory, "assignment.consumed")
	caPath := filepath.Join(directory, "ca.pem")
	githubEnv := filepath.Join(directory, "github-env")
	delivery := testCacheDelivery()
	assignment := workerCacheClaim{SchemaVersion: 2, InstanceName: "warm-standard-example", RunnerName: "runner-example", ClaimEndpoint: "https://198.51.100.1:9443/api/v1/cache/claim", ClaimToken: strings.Repeat("a", 43), CAPEMB64: base64.StdEncoding.EncodeToString(delivery.CAPEM)}
	raw, _ := json.Marshal(assignment)
	require.NoError(t, os.WriteFile(assignmentPath, raw, 0o400))
	require.NoError(t, os.WriteFile(readyPath, nil, 0o400))
	require.NoError(t, os.WriteFile(caPath, delivery.CAPEM, 0o400))
	require.NoError(t, os.WriteFile(githubEnv, nil, 0o600))
	response := workerCacheAssignment{SchemaVersion: 1, DeliveryID: strings.Repeat("b", 64), Role: delivery.Role, Mode: delivery.Mode, Endpoint: delivery.Endpoint, Region: delivery.Region, Bucket: delivery.Bucket, PrefixRoot: delivery.Prefix, AccessKey: delivery.AccessKey, SecretKeyB64: base64.StdEncoding.EncodeToString(delivery.SecretKey), CAPEMB64: base64.StdEncoding.EncodeToString(delivery.CAPEM)}
	responseRaw, _ := json.Marshal(response)
	fakeCurl := filepath.Join(directory, "curl")
	requestPath := filepath.Join(directory, "claim-request.json")
	require.NoError(t, os.WriteFile(fakeCurl, []byte("#!/bin/sh\ncat >'"+requestPath+"'\nprintf '%s' '"+string(responseRaw)+"'\n"), 0o700))
	hook := strings.NewReplacer(cacheAssignmentPath, assignmentPath, cacheReadyPath, readyPath, cacheConsumedPath, consumedPath, cacheCAPath, caPath).Replace(cacheJobStartedHook())
	command := exec.Command("bash", "-c", hook)
	command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "GITHUB_ENV="+githubEnv,
		"GITHUB_REPOSITORY=example-org/example-actions", "GITHUB_REPOSITORY_ID=123",
		"GITHUB_RUN_ID=456", "GITHUB_RUN_ATTEMPT=2", "GITHUB_JOB=test",
		"GITHUB_WORKFLOW_REF=example-org/example-actions/.github/workflows/ci.yml@refs/heads/main",
		"GITHUB_SHA="+strings.Repeat("a", 40), "RUNNER_NAME=runner-example")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("claim hook failed: %v\n%s", err, output)
	}
	environment, _ := os.ReadFile(githubEnv)
	require.Contains(t, string(environment), "NDDEV_CACHE_ROLE=trusted-writer")
	require.Contains(t, string(environment), "AWS_ACCESS_KEY_ID="+delivery.AccessKey)
	// The trusted writer is handed runs-on/cache's bucket, endpoint and path
	// style so the drop-in for actions/cache lands on the fleet's store.
	require.Contains(t, string(environment), "RUNS_ON_S3_BUCKET_CACHE="+delivery.Bucket)
	require.Contains(t, string(environment), "RUNS_ON_S3_BUCKET_ENDPOINT="+delivery.Endpoint)
	require.Contains(t, string(environment), "RUNS_ON_S3_FORCE_PATH_STYLE=true")
	require.NotContains(t, string(environment), "NDDEV_BUILDCACHE_REF=")
	requestRaw, err := os.ReadFile(requestPath)
	require.NoError(t, err)
	var claimRequest map[string]any
	require.NoError(t, json.Unmarshal(requestRaw, &claimRequest))
	require.Equal(t, float64(123), claimRequest["repository_id"])
	require.Equal(t, float64(456), claimRequest["workflow_run_id"])
	require.Equal(t, float64(2), claimRequest["run_attempt"])
	require.Equal(t, "test", claimRequest["job_name"])
	require.Equal(t, strings.Repeat("a", 40), claimRequest["commit_sha"])
	require.Equal(t, "warm-standard-example", claimRequest["instance_name"])
	require.Equal(t, "runner-example", claimRequest["runner_name"])
	consumed, _ := os.ReadFile(consumedPath)
	require.Equal(t, response.DeliveryID+"\n", string(consumed))
}

func TestJobStartedHookDegradesToUncachedWhenRepositoryClaimFails(t *testing.T) {
	directory := t.TempDir()
	assignmentPath := filepath.Join(directory, "assignment.json")
	readyPath := filepath.Join(directory, "assignment.ready")
	consumedPath := filepath.Join(directory, "assignment.consumed")
	caPath := filepath.Join(directory, "ca.pem")
	githubEnv := filepath.Join(directory, "github-env")
	delivery := testCacheDelivery()
	assignment := workerCacheClaim{SchemaVersion: 2, InstanceName: "warm-standard-example", RunnerName: "runner-example", ClaimEndpoint: "https://198.51.100.1:9443/api/v1/cache/claim", ClaimToken: strings.Repeat("a", 43), CAPEMB64: base64.StdEncoding.EncodeToString(delivery.CAPEM)}
	raw, _ := json.Marshal(assignment)
	require.NoError(t, os.WriteFile(assignmentPath, raw, 0o400))
	require.NoError(t, os.WriteFile(readyPath, nil, 0o400))
	require.NoError(t, os.WriteFile(caPath, delivery.CAPEM, 0o400))
	require.NoError(t, os.WriteFile(githubEnv, nil, 0o600))
	fakeCurl := filepath.Join(directory, "curl")
	requestPath := filepath.Join(directory, "claim-request.json")
	require.NoError(t, os.WriteFile(fakeCurl, []byte("#!/bin/sh\ncat >'"+requestPath+"'\nprintf 'claim denied\\n' >&2\nexit 22\n"), 0o700))
	hook := strings.NewReplacer(cacheAssignmentPath, assignmentPath, cacheReadyPath, readyPath, cacheConsumedPath, consumedPath, cacheCAPath, caPath).Replace(cacheJobStartedHook())
	command := exec.Command("bash", "-c", hook)
	command.Env = append(environmentWithout(
		"GITHUB_REPOSITORY_ID", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_JOB", "GITHUB_WORKFLOW_REF", "GITHUB_SHA",
	), "PATH="+directory+":"+os.Getenv("PATH"), "GITHUB_ENV="+githubEnv,
		"GITHUB_REPOSITORY=example-org/example-actions", "RUNNER_NAME=runner-example")
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "job correlation is incomplete")
	require.Contains(t, string(output), "continuing without cache")
	requestRaw, err := os.ReadFile(requestPath)
	require.NoError(t, err)
	var claimRequest map[string]any
	require.NoError(t, json.Unmarshal(requestRaw, &claimRequest))
	require.Equal(t, []string{"claim_token", "instance_name", "repository", "runner_name"}, sortedMapKeys(claimRequest))
	environment, err := os.ReadFile(githubEnv)
	require.NoError(t, err)
	require.Empty(t, environment)
	_, err = os.Lstat(assignmentPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(readyPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = os.Lstat(consumedPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func sortedMapKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func environmentWithout(names ...string) []string {
	removed := make(map[string]struct{}, len(names))
	for _, name := range names {
		removed[name] = struct{}{}
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, exists := removed[name]; !exists {
			environment = append(environment, entry)
		}
	}
	return environment
}

func TestCacheShellProgramsParseAndNeverEnableXtrace(t *testing.T) {
	for name, script := range map[string]string{
		"setup":  string(renderCacheSetupScript()),
		"hook":   cacheJobStartedHook(),
		"warm":   string(mergeCacheIntoWarmAssignment(renderWarmAssignment(expectedMetadataURL, "opaque", nil, ""), []byte(`{"secret":"opaque"}`))),
		"direct": string(mergeCacheIntoWarmAssignment(renderWarmAssignment(expectedMetadataURL, "", nil, testEncodedDirectJIT(t)), []byte(`{"secret":"opaque"}`))),
	} {
		command := exec.Command("bash", "-n")
		command.Stdin = strings.NewReader(script)
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s script: %s", name, output)
		require.NotContains(t, script, "set -x", name)
	}
}

// An assignment the hook cannot use costs the job its cache, never the job.
// Twenty-two jobs failed at "Set up runner" on 2026-08-30/31 because the hook
// exited non-zero on an assignment a provider rollout had left unreadable.
func TestJobStartedHookFailsOpenWhenTheAssignmentIsUnusable(t *testing.T) {
	directory := t.TempDir()
	assignmentPath := filepath.Join(directory, "assignment.json")
	readyPath := filepath.Join(directory, "assignment.ready")
	consumedPath := filepath.Join(directory, "assignment.consumed")
	githubEnv := filepath.Join(directory, "github-env")
	// Wrong mode and wrong shape: two of the checks that used to fail the job.
	require.NoError(t, os.WriteFile(assignmentPath, []byte("{\"schema_version\":1}"), 0o644))
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
	require.Contains(t, string(output), "::warning::")
	require.Contains(t, string(output), "continuing without cache")
	environment, err := os.ReadFile(githubEnv)
	require.NoError(t, err)
	require.Empty(t, environment)
	_, err = os.Lstat(consumedPath)
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A delivery that carries a buildcache credential logs the runner's Docker
// client into the member's zot for exactly this repository's layer-cache
// namespace and names the reference the workflow passes to BuildKit; the
// untrusted writer is handed no runs-on/cache bucket at all.
func TestJobStartedHookLogsIntoTheBuildcacheAndKeepsGitHubCacheForUntrusted(t *testing.T) {
	directory := t.TempDir()
	home := filepath.Join(directory, "home")
	require.NoError(t, os.MkdirAll(home, 0o700))
	assignmentPath := filepath.Join(directory, "assignment.json")
	readyPath := filepath.Join(directory, "assignment.ready")
	consumedPath := filepath.Join(directory, "assignment.consumed")
	caPath := filepath.Join(directory, "ca.pem")
	githubEnv := filepath.Join(directory, "github-env")
	delivery := testCacheDelivery()
	assignment := workerCacheClaim{SchemaVersion: 2, InstanceName: "warm-integration-example", RunnerName: "runner-example", ClaimEndpoint: "https://198.51.100.1:9443/api/v1/cache/claim", ClaimToken: strings.Repeat("t", 43), CAPEMB64: base64.StdEncoding.EncodeToString(delivery.CAPEM)}
	raw, _ := json.Marshal(assignment)
	require.NoError(t, os.WriteFile(assignmentPath, raw, 0o400))
	require.NoError(t, os.WriteFile(readyPath, nil, 0o400))
	require.NoError(t, os.WriteFile(caPath, delivery.CAPEM, 0o400))
	require.NoError(t, os.WriteFile(githubEnv, nil, 0o600))
	response := map[string]any{
		"schema_version": 1, "delivery_id": strings.Repeat("b", 64), "role": "untrusted-writer", "mode": "read-write",
		"endpoint": delivery.Endpoint, "region": delivery.Region, "bucket": delivery.Bucket,
		"prefix_root": "example-org/example-actions/trust/untrusted", "access_key": delivery.AccessKey,
		"secret_key_b64": base64.StdEncoding.EncodeToString(delivery.SecretKey), "ca_pem_b64": base64.StdEncoding.EncodeToString(delivery.CAPEM),
		"buildcache": map[string]any{"registry": "https://192.0.2.1:5001", "repository": "buildcache/example-org/example-actions/untrusted",
			"username": "gha-zot-example-org-example-actions-buildcache-untrusted", "password_b64": base64.StdEncoding.EncodeToString([]byte("secret-pass"))},
	}
	responseRaw, _ := json.Marshal(response)
	fakeCurl := filepath.Join(directory, "curl")
	require.NoError(t, os.WriteFile(fakeCurl, []byte("#!/bin/sh\ncat >/dev/null\nprintf '%s' '"+string(responseRaw)+"'\n"), 0o700))
	hook := strings.NewReplacer(cacheAssignmentPath, assignmentPath, cacheReadyPath, readyPath, cacheConsumedPath, consumedPath, cacheCAPath, caPath).Replace(cacheJobStartedHook())
	command := exec.Command("bash", "-c", hook)
	command.Env = append(os.Environ(), "PATH="+directory+":"+os.Getenv("PATH"), "GITHUB_ENV="+githubEnv, "HOME="+home,
		"GITHUB_REPOSITORY=example-org/example-actions", "GITHUB_REPOSITORY_ID=123",
		"GITHUB_RUN_ID=456", "GITHUB_RUN_ATTEMPT=1", "GITHUB_JOB=test",
		"GITHUB_WORKFLOW_REF=example-org/example-actions/.github/workflows/ci.yml@refs/heads/main",
		"GITHUB_SHA="+strings.Repeat("a", 40), "RUNNER_NAME=runner-example")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("claim hook failed: %v\n%s", err, output)
	}
	require.Contains(t, string(output), "::add-mask::secret-pass")
	environment, _ := os.ReadFile(githubEnv)
	require.Contains(t, string(environment), "NDDEV_BUILDCACHE_REGISTRY=192.0.2.1:5001")
	require.Contains(t, string(environment), "NDDEV_BUILDCACHE_REF=192.0.2.1:5001/buildcache/example-org/example-actions/untrusted")
	require.NotContains(t, string(environment), "RUNS_ON_S3_BUCKET_CACHE=")
	dockerConfig, err := os.ReadFile(filepath.Join(home, ".docker", "config.json"))
	require.NoError(t, err)
	var parsed struct {
		Auths map[string]struct {
			Auth string `json:"auth"`
		} `json:"auths"`
	}
	require.NoError(t, json.Unmarshal(dockerConfig, &parsed))
	require.Equal(t, base64.StdEncoding.EncodeToString([]byte("gha-zot-example-org-example-actions-buildcache-untrusted:secret-pass")), parsed.Auths["192.0.2.1:5001"].Auth)
	info, err := os.Stat(filepath.Join(home, ".docker", "config.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

// Node keeps its own root store, so the runner's .env hands it the fleet CA.
func TestCacheSetupHandsNodeTheFleetCA(t *testing.T) {
	require.Contains(t, string(renderCacheSetupScript()), "NODE_EXTRA_CA_CERTS=%s")
}

// Anonymous git from the fleet's shared addresses is throttled under a
// burst; the setup installs a github.com credential helper that answers
// with the job token a step exports and stays silent otherwise, and it adds
// the helper to the runner's baked git config instead of replacing it.
func TestCacheSetupInstallsTheGitHubTokenCredentialHelper(t *testing.T) {
	script := string(renderCacheSetupScript())
	require.Contains(t, script, `credential_helper="/home/runner/.gha-cache/github-token-credential"`)
	require.Contains(t, script, `git config --file /home/runner/.gitconfig "credential.https://github.com.helper" "${credential_helper}"`)
	start := strings.Index(script, "<<'HELPER'\n")
	end := strings.Index(script, "\nHELPER\n")
	require.True(t, start > 0 && end > start, "helper body not rendered")
	helper := filepath.Join(t.TempDir(), "github-token-credential")
	require.NoError(t, os.WriteFile(helper, []byte(script[start+len("<<'HELPER'\n"):end+1]), 0o755))
	run := func(action, input string, env ...string) string {
		command := exec.Command("bash", helper, action)
		command.Stdin = strings.NewReader(input)
		command.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
		output, err := command.CombinedOutput()
		require.NoError(t, err, string(output))
		return string(output)
	}
	github := "protocol=https\nhost=github.com\n\n"
	require.Equal(t, "username=x-access-token\npassword=ghs_step\n", run("get", github, "GH_TOKEN=ghs_step"))
	require.Equal(t, "username=x-access-token\npassword=ghs_job\n", run("get", github, "GITHUB_TOKEN=ghs_job"))
	require.Equal(t, "", run("get", github), "a step that exports no token stays anonymous")
	require.Equal(t, "", run("get", "protocol=https\nhost=gitlab.com\n\n", "GH_TOKEN=ghs_step"), "the token is for github.com only")
	require.Equal(t, "", run("store", github, "GH_TOKEN=ghs_step"), "only get answers")
}
