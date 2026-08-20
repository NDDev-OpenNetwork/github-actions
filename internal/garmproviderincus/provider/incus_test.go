// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.

package provider

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incuspolicy"
	"github.com/NDDev-OpenNetwork/github-actions/internal/provideradmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
	"github.com/cloudbase/garm-provider-common/cloudconfig"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

const (
	testControllerID           = "controller-test"
	testImageAlias             = "nddev-ubuntu-24.04-amd64-current"
	testImageDigest            = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testIntegrationImageAlias  = "nddev-ubuntu-24.04-amd64-docker-current"
	testIntegrationImageDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	testContainerImageAlias    = "nddev-ubuntu-24.04-amd64-container-current"
	testContainerImageDigest   = "1111111111111111111111111111111111111111111111111111111111111111"
)

func newTestProvider(cli *MockIncusServer) *Incus {
	provider := &Incus{
		cfg: &config.Incus{
			ProjectName: config.ExpectedProjectName,
			SecureBoot:  true,
			WorkerImages: map[string]config.WorkerImage{
				"nddev-linux-standard": {
					Alias:        testImageAlias,
					Fingerprint:  testImageDigest,
					Variant:      "standard",
					InstanceType: config.IncusImageVirtualMachine,
					RunnerUID:    1001,
					RunnerGID:    1002,
				},
				"nddev-linux-integration": {
					Alias:        testIntegrationImageAlias,
					Fingerprint:  testIntegrationImageDigest,
					Variant:      "integration",
					InstanceType: config.IncusImageVirtualMachine,
					RunnerUID:    1001,
					RunnerGID:    1002,
				},
			},
			AllowedGitHubAccounts: []string{"example-org", "example-guild", "example-media"},
			WorkerGatewayURL:      "https://198.51.100.1:9443",
		},
		cli:          cli,
		imageManager: &image{},
		controllerID: testControllerID,
		admission:    allowAllAdmission{},
		diagnostics:  noopDiagnostics{},
		platform: platformconfig.Config{Backends: []platformconfig.Backend{{
			Name: "linux-amd64-incus", Platform: "linux", Architecture: "amd64", Implementation: "incus-vm",
		}}, Pools: []platformconfig.Pool{
			{
				Name:         "nddev-linux-standard",
				Backend:      "linux-amd64-incus",
				ScaleSetName: "nddev-linux-standard",
				Trust:        "trusted",
				Capabilities: platformconfig.Capabilities{
					NetworkPolicy:   "public-internet",
					CacheWriteScope: "trusted",
				},
			},
			{
				Name:         "nddev-linux-integration",
				Backend:      "linux-amd64-incus",
				ScaleSetName: "nddev-linux-integration",
				Trust:        "trusted",
				Capabilities: platformconfig.Capabilities{
					Docker:          true,
					NetworkPolicy:   "public-internet",
					CacheWriteScope: "trusted",
				},
			},
		}, ControlPlane: platformconfig.ControlPlane{RunnerVersion: "v2.336.0"}},
	}
	provider.cacheRepository = func() (string, error) { return "example-org/example-actions", nil }
	return provider
}

type allowAllAdmission struct{}

type repositoryResolvingAdmission struct {
	allowAllAdmission
	repository string
	err        error
}

func (r repositoryResolvingAdmission) ResolveRepository(context.Context, commonParams.BootstrapInstance) (string, error) {
	return r.repository, r.err
}

type recordingAdmission struct {
	allowAllAdmission
	released   []string
	reconciled int
}

type warmAdmission struct {
	claim    provideradmission.WarmClaimResult
	injected bool
}

type preemptingAdmission struct {
	allowAllAdmission
	calls              int
	rejectConfirmation bool
	markedDeleting     []string
	released           []string
	reconciled         int
}

type noopDiagnostics struct{}

func (noopDiagnostics) Capture(context.Context, InstanceServerInterface, *api.InstanceFull) (workerdiagnostics.Result, error) {
	return workerdiagnostics.Result{}, nil
}

type recordingDiagnostics struct {
	called bool
	err    error
}

type blockingDiagnostics struct{}

func (blockingDiagnostics) Capture(ctx context.Context, _ InstanceServerInterface, _ *api.InstanceFull) (workerdiagnostics.Result, error) {
	<-ctx.Done()
	return workerdiagnostics.Result{}, ctx.Err()
}

func (r *recordingDiagnostics) Capture(context.Context, InstanceServerInterface, *api.InstanceFull) (workerdiagnostics.Result, error) {
	r.called = true
	return workerdiagnostics.Result{Path: "/private/diagnostics.tar.gz"}, r.err
}

func (allowAllAdmission) Admit(context.Context, InstanceServerInterface, commonParams.BootstrapInstance) (provideradmission.AdmissionResult, error) {
	return provideradmission.AdmissionResult{Decision: admission.Decision{Admitted: true, Reason: admission.ReasonAdmitted}}, nil
}

func (allowAllAdmission) Reconcile(context.Context, InstanceServerInterface) error { return nil }
func (allowAllAdmission) MarkCreated(context.Context, string) error                { return nil }
func (allowAllAdmission) MarkDeleting(context.Context, string) error               { return nil }
func (allowAllAdmission) Release(context.Context, string) error                    { return nil }
func (allowAllAdmission) ClaimWarm(context.Context, InstanceServerInterface, commonParams.BootstrapInstance) (provideradmission.WarmClaimResult, error) {
	return provideradmission.WarmClaimResult{}, nil
}
func (allowAllAdmission) MarkWarmInjected(context.Context, string, string) error { return nil }
func (allowAllAdmission) Resolve(_ context.Context, identifier string) (string, error) {
	return identifier, nil
}
func (allowAllAdmission) AdmitWarm(context.Context, InstanceServerInterface, string, string) (admission.Decision, error) {
	return admission.Decision{Admitted: true, Reason: admission.ReasonAdmitted}, nil
}
func (allowAllAdmission) AuthorizeWarmDrain(context.Context, string) error { return nil }
func (allowAllAdmission) WarmBlockedByQueue(context.Context) (bool, error) { return false, nil }

func (r *recordingAdmission) Release(_ context.Context, instance string) error {
	r.released = append(r.released, instance)
	return nil
}

func (r *recordingAdmission) Reconcile(context.Context, InstanceServerInterface) error {
	r.reconciled++
	return nil
}

func (p *preemptingAdmission) Admit(context.Context, InstanceServerInterface, commonParams.BootstrapInstance) (provideradmission.AdmissionResult, error) {
	p.calls++
	result := provideradmission.AdmissionResult{Decision: admission.Decision{Admitted: true, Reason: admission.ReasonAdmitted}}
	if p.calls == 1 {
		// A durable preemption reservation must remain actionable even when a
		// retry observes a transiently unhealthy host before the victim is gone.
		result.Decision = admission.Decision{Admitted: false, Reason: admission.ReasonHostUnhealthy}
		result.PreemptedWarmWorkers = []string{"warm-standard-preempt"}
	} else if p.rejectConfirmation {
		result.Decision = admission.Decision{Admitted: false, Reason: admission.ReasonInsufficientMemory}
	}
	return result, nil
}

func (p *preemptingAdmission) MarkDeleting(_ context.Context, instance string) error {
	p.markedDeleting = append(p.markedDeleting, instance)
	return nil
}

func (p *preemptingAdmission) Release(_ context.Context, instance string) error {
	p.released = append(p.released, instance)
	return nil
}

func (p *preemptingAdmission) Reconcile(context.Context, InstanceServerInterface) error {
	p.reconciled++
	return nil
}

func (w *warmAdmission) Admit(context.Context, InstanceServerInterface, commonParams.BootstrapInstance) (provideradmission.AdmissionResult, error) {
	return provideradmission.AdmissionResult{Decision: admission.Decision{Admitted: true, Reason: admission.ReasonAdmitted}}, nil
}
func (w *warmAdmission) Reconcile(context.Context, InstanceServerInterface) error { return nil }
func (w *warmAdmission) MarkCreated(context.Context, string) error                { return nil }
func (w *warmAdmission) MarkDeleting(context.Context, string) error               { return nil }
func (w *warmAdmission) Release(context.Context, string) error                    { return nil }
func (w *warmAdmission) ClaimWarm(context.Context, InstanceServerInterface, commonParams.BootstrapInstance) (provideradmission.WarmClaimResult, error) {
	return w.claim, nil
}
func (w *warmAdmission) MarkWarmInjected(_ context.Context, jobName, instanceName string) error {
	if jobName != "runner-test-instance" || instanceName != w.claim.InstanceName {
		return errors.New("unexpected warm injection identity")
	}
	w.injected = true
	w.claim.State = providerjournal.ClaimInjected
	return nil
}
func (w *warmAdmission) Resolve(_ context.Context, identifier string) (string, error) {
	if identifier == "runner-test-instance" {
		return w.claim.InstanceName, nil
	}
	return identifier, nil
}
func (w *warmAdmission) AdmitWarm(context.Context, InstanceServerInterface, string, string) (admission.Decision, error) {
	return admission.Decision{Admitted: true, Reason: admission.ReasonAdmitted}, nil
}
func (w *warmAdmission) AuthorizeWarmDrain(context.Context, string) error { return nil }
func (w *warmAdmission) WarmBlockedByQueue(context.Context) (bool, error) { return false, nil }

func defaultTenantRepositoryURL() string {
	selected, err := tenant.ByID(tenant.DefaultID)
	if err != nil {
		panic(err)
	}
	return "https://github.com/" + selected.Repository
}

// The boundary is a set, not a constant. Every registered tenant must pass and
// anything outside the registry must not: before this, a second tenant failed
// here after the whole control path had already succeeded for it.
//
// A tenant that serves its whole account is admitted for every repository that
// account owns, because its scale sets hang from an organization entity and
// jobs therefore arrive from repositories the registry never names one by one.
// A tenant that does not is admitted for exactly the repository it declares.
func TestBootstrapBoundaryAdmitsEveryRegisteredTenantAndNothingElse(t *testing.T) {
	t.Parallel()

	for _, id := range tenant.IDs() {
		selected, err := tenant.ByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if !isRegisteredRepositoryURL("https://github.com/" + selected.Repository) {
			t.Errorf("tenant %q repository is outside the provider boundary", id)
		}
		sibling := "https://github.com/" + selected.Owner + "/some-other-repository"
		if got := isRegisteredRepositoryURL(sibling); got != selected.ServesWholeAccount {
			t.Errorf("tenant %q admits sibling repository = %v, want %v (serves whole account = %v)",
				id, got, selected.ServesWholeAccount, selected.ServesWholeAccount)
		}
	}
	// No account prefix may be widened into a neighbour by string prefix: an
	// owner whose name merely starts with a registered one is a different
	// account and must be refused.
	for _, rejected := range []string{
		"",
		"https://github.com/someone-else/ai_stp",
		"https://github.com/example-guild-evil/anything",
		"https://github.com/example-org-evil/github-actions",
		"https://example.invalid/example-org/example-actions",
	} {
		if isRegisteredRepositoryURL(rejected) {
			t.Errorf("unregistered repository %q passed the provider boundary", rejected)
		}
	}
}

func validBootstrap() commonParams.BootstrapInstance {
	bootstrap := commonParams.BootstrapInstance{
		Name:              "runner-test-instance",
		Tools:             testTools(),
		RepoURL:           defaultTenantRepositoryURL(),
		CallbackURL:       expectedCallbackURL,
		MetadataURL:       expectedMetadataURL,
		InstanceToken:     "opaque-test-token",
		GitHubRunnerGroup: expectedRunnerGroup,
		OSArch:            commonParams.Amd64,
		OSType:            commonParams.Linux,
		Flavor:            "nddev-linux-standard",
		Image:             testImageAlias,
		PoolID:            "pool-test",
		JitConfigEnabled:  true,
	}
	raw, err := json.Marshal(extraSpecs{
		DisableUpdates: true,
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: renderPinnedGARMV021LinuxWrapper(bootstrap.MetadataURL, bootstrap.InstanceToken),
		},
	})
	if err != nil {
		panic(err)
	}
	bootstrap.ExtraSpecs = raw
	return bootstrap
}

func validIntegrationBootstrap() commonParams.BootstrapInstance {
	bootstrap := validBootstrap()
	bootstrap.Flavor = "nddev-linux-integration"
	bootstrap.Image = testIntegrationImageAlias
	bootstrap.PoolID = "pool-integration-test"
	return bootstrap
}

func directJITBootstrap(t *testing.T) commonParams.BootstrapInstance {
	t.Helper()
	bootstrap := validBootstrap()
	specs, err := parseExtraSpecsFromBootstrapParams(bootstrap)
	require.NoError(t, err)
	specs.DirectJIT = true
	specs.EncodedJITConfig = testEncodedDirectJIT(t)
	bootstrap.ExtraSpecs, err = json.Marshal(specs)
	require.NoError(t, err)
	return bootstrap
}

func testCABundle(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Unix(1, 0),
		NotAfter:              time.Unix(4102444800, 0),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func testTools() []commonParams.RunnerApplicationDownload {
	return []commonParams.RunnerApplicationDownload{{
		OS:           ptr("linux"),
		Architecture: ptr("x86_64"),
		DownloadURL:  ptr("https://github.com/actions/runner/releases/download/v2.336.0/actions-runner-linux-x64-2.336.0.tar.gz"),
		Filename:     ptr("actions-runner-linux-x64-2.336.0.tar.gz"),
	}}
}

func ownedInstance(name string) *api.InstanceFull {
	return &api.InstanceFull{
		Instance: api.Instance{
			InstancePut: api.InstancePut{
				Architecture: "x86_64",
				Profiles:     []string{"nddev-linux-standard"},
			},
			Name: name,
			ExpandedConfig: map[string]string{
				"image.os":            "ubuntu",
				"image.release":       "24.04",
				controllerIDKeyName:   testControllerID,
				poolIDKey:             "pool-test",
				imageAliasKey:         testImageAlias,
				imageFingerprintKey:   testImageDigest,
				providerVersionKey:    Version,
				providerCommitKey:     Commit,
				flavorKey:             "nddev-linux-standard",
				lifecycleKey:          "ephemeral-one-job",
				trustKey:              "trusted",
				scaleSetKey:           "nddev-linux-standard",
				repositoryKey:         "example-org/example-actions",
				networkPolicyKey:      "public-internet",
				cacheWriteScopeKey:    "trusted",
				garmJobNameKey:        "runner-test-instance",
				osTypeKeyName:         string(commonParams.Linux),
				osArchKeyNAme:         string(commonParams.Amd64),
				"security.secureboot": "true",
				"security.nesting":    "false",
				"raw.qemu":            incuspolicy.DisableNestedVirtualizationRawQEMU,
			},
			Type: string(api.InstanceTypeVM),
		},
		State: &api.InstanceState{
			Status: "Running",
			Network: map[string]api.InstanceStateNetwork{
				"eth0": {Addresses: []api.InstanceStateNetworkAddress{{Address: "198.51.100.22", Scope: "global"}}},
			},
		},
	}
}

func TestExistingNMinusOneWorkerMayFinishButCannotBecomeWarm(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	previous := config.ProviderIdentity{Version: "v0.1.5-nddev.32", Commit: "945fad5e276c0b21ba763425a5d7a692df4e35e7"}
	provider.cfg.PreviousProviderIdentities = []config.ProviderIdentity{previous}
	instance := ownedInstance("runner-test-instance")
	instance.ExpandedConfig[providerVersionKey] = previous.Version
	instance.ExpandedConfig[providerCommitKey] = previous.Commit

	require.NoError(t, provider.validateExistingInstance(instance, validBootstrap()))
	warm := warmInstance("warm-standard-previous")
	warm.Config[providerVersionKey], warm.ExpandedConfig[providerVersionKey] = previous.Version, previous.Version
	warm.Config[providerCommitKey], warm.ExpandedConfig[providerCommitKey] = previous.Commit, previous.Commit
	require.ErrorContains(t, provider.validateWarmInstance(warm, validBootstrap()), providerVersionKey)
}

func warmInstance(name string) *api.InstanceFull {
	instance := ownedInstance(name)
	instance.Config = map[string]string{}
	for key, value := range instance.ExpandedConfig {
		if strings.HasPrefix(key, "image.") || key == "security.nesting" {
			continue
		}
		instance.Config[key] = value
	}
	instance.Config[poolIDKey] = warmPoolIDPrefix + "nddev-linux-standard"
	instance.Config[lifecycleKey] = lifecycleWarmUnregistered
	instance.Config[warmReadyKey] = "true"
	instance.Config["boot.autostart"] = "true"
	instance.Config[repositoryKey] = ""
	instance.Config[garmJobNameKey] = ""
	instance.ExpandedConfig[poolIDKey] = instance.Config[poolIDKey]
	instance.ExpandedConfig[lifecycleKey] = lifecycleWarmUnregistered
	instance.ExpandedConfig[warmReadyKey] = "true"
	instance.ExpandedConfig["boot.autostart"] = "true"
	instance.ExpandedConfig[repositoryKey] = ""
	instance.ExpandedConfig[garmJobNameKey] = ""
	return instance
}

func stubCloudConfig(t *testing.T) {
	t.Helper()
	originalToolFetch := DefaultToolFetch
	originalCloudConfig := DefaultGetCloudconfig
	t.Cleanup(func() {
		DefaultToolFetch = originalToolFetch
		DefaultGetCloudconfig = originalCloudConfig
	})
	DefaultToolFetch = func(_ commonParams.OSType, _ commonParams.OSArch, tools []commonParams.RunnerApplicationDownload) (commonParams.RunnerApplicationDownload, error) {
		return tools[0], nil
	}
	DefaultGetCloudconfig = func(_ commonParams.BootstrapInstance, _ commonParams.RunnerApplicationDownload, _ string) (string, error) {
		return "#cloud-config", nil
	}
}

func prepareCreateMocks(cli *MockIncusServer, fingerprint string) {
	prepareCreateMocksFor(cli, "nddev-linux-standard", testImageAlias, fingerprint, "standard")
}

func prepareCreateMocksFor(cli *MockIncusServer, profile, alias, fingerprint, variant string) {
	prepareCreateMocksForType(cli, profile, alias, fingerprint, variant, config.IncusImageVirtualMachine)
}

func prepareCreateMocksForType(cli *MockIncusServer, profile, alias, fingerprint, variant string, instanceType config.IncusImageType) {
	aliases := map[string]*api.ImageAliasesEntry{
		"x86_64": {
			ImageAliasesEntryPut: api.ImageAliasesEntryPut{Target: "image-target"},
			Name:                 alias,
			Type:                 string(instanceType),
		},
	}
	cli.On("GetProfileNames").Return([]string{profile}, nil)
	cli.On("GetImageAliasArchitectures", string(instanceType), alias).Return(aliases, nil)
	cli.On("GetImage", "image-target").Return(&api.Image{
		Fingerprint: fingerprint,
		ImagePut: api.ImagePut{
			Properties: map[string]string{"user.nddev.image.variant": variant},
		},
	}, "", nil)
}

func TestGetCLIReturnsInjectedClient(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	got, err := provider.getCLI(context.Background())
	require.NoError(t, err)
	require.Same(t, cli, got)
}

func setTestCacheDelivery(t *testing.T, provider *Incus) {
	t.Helper()
	provider.cacheDelivery = func(role string) (rustfscache.Delivery, error) {
		require.Equal(t, "trusted-writer", role)
		return testCacheDelivery(), nil
	}
}

func TestCompatibilityProbeValidatesProfileImageAndReadOnlyInventory(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setTestCacheDelivery(t, provider)
	prepareCreateMocks(cli, testImageDigest)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil)

	result, err := provider.Probe(context.Background(), "nddev-linux-standard")
	require.NoError(t, err)
	require.True(t, result.Compatible)
	require.Equal(t, config.ExpectedProjectName, result.Project)
	require.Equal(t, "nddev-linux-standard", result.Profile)
	require.Equal(t, testImageAlias, result.ImageAlias)
	require.Equal(t, testImageDigest, result.ImageFingerprint)
	require.Equal(t, IncusSDKVersion, result.IncusSDKVersion)
	require.Zero(t, result.VisibleInstanceCount)
	require.Empty(t, result.VisibleInstances)
	require.True(t, result.CacheDeliveryReady)
	require.Equal(t, "trusted-writer", result.CacheRole)
	cli.AssertExpectations(t)
}

func TestCompatibilityProbeSelectsIntegrationImageByProfile(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setTestCacheDelivery(t, provider)
	prepareCreateMocksFor(
		cli,
		"nddev-linux-integration",
		testIntegrationImageAlias,
		testIntegrationImageDigest,
		"integration",
	)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil)

	result, err := provider.Probe(context.Background(), "nddev-linux-integration")
	require.NoError(t, err)
	require.Equal(t, testIntegrationImageAlias, result.ImageAlias)
	require.Equal(t, testIntegrationImageDigest, result.ImageFingerprint)
	cli.AssertExpectations(t)
}

func TestCompatibilityProbeRejectsImageVariantDrift(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setTestCacheDelivery(t, provider)
	prepareCreateMocksFor(cli, "nddev-linux-standard", testImageAlias, testImageDigest, "integration")

	_, err := provider.Probe(context.Background(), "nddev-linux-standard")
	require.ErrorContains(t, err, "has variant \"integration\", expected \"standard\"")
}

func TestCompatibilityProbeReturnsStableSortedInventory(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setTestCacheDelivery(t, provider)
	prepareCreateMocks(cli, testImageDigest)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{
		{Instance: api.Instance{Name: "runner-z"}},
		{Instance: api.Instance{Name: "runner-a"}},
	}, nil)

	result, err := provider.Probe(context.Background(), "nddev-linux-standard")
	require.NoError(t, err)
	require.Equal(t, 2, result.VisibleInstanceCount)
	require.Equal(t, []string{"runner-a", "runner-z"}, result.VisibleInstances)
}

func TestCompatibilityProbeFailsOnImageAliasDrift(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	setTestCacheDelivery(t, provider)
	prepareCreateMocks(cli, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	_, err := provider.Probe(context.Background(), "nddev-linux-standard")
	require.ErrorContains(t, err, "expected \""+testImageDigest+"\"")
	cli.AssertExpectations(t)
}

func TestGetProfilesRequiresExactFlavorProfile(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	cli.On("GetProfileNames").Return([]string{"nddev-linux-standard"}, nil)

	profiles, err := provider.getProfiles(context.Background(), "nddev-linux-standard")
	require.NoError(t, err)
	require.Equal(t, []string{"nddev-linux-standard"}, profiles)

	_, err = provider.getProfiles(context.Background(), "missing")
	require.ErrorContains(t, err, "looking for profile missing")
}

func TestValidateBootstrapParamsFailsClosed(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	tests := []struct {
		name    string
		mutate  func(*commonParams.BootstrapInstance)
		message string
	}{
		{"name", func(p *commonParams.BootstrapInstance) { p.Name = "" }, "missing name"},
		{"pool", func(p *commonParams.BootstrapInstance) { p.PoolID = "" }, "missing pool ID"},
		{"flavor", func(p *commonParams.BootstrapInstance) { p.Flavor = "" }, "missing flavor"},
		{"repository", func(p *commonParams.BootstrapInstance) { p.RepoURL = "https://github.com/other/repository" }, "repository is outside"},
		{"callback", func(p *commonParams.BootstrapInstance) { p.CallbackURL = "https://example.invalid/callback" }, "callback URL is outside"},
		{"metadata", func(p *commonParams.BootstrapInstance) { p.MetadataURL = "https://example.invalid/metadata" }, "metadata URL is outside"},
		{"token absent", func(p *commonParams.BootstrapInstance) { p.InstanceToken = "" }, "missing or malformed instance token"},
		{"token whitespace", func(p *commonParams.BootstrapInstance) { p.InstanceToken = " token" }, "missing or malformed instance token"},
		{"token embedded newline", func(p *commonParams.BootstrapInstance) { p.InstanceToken = "token\nheader" }, "missing or malformed instance token"},
		{"runner group", func(p *commonParams.BootstrapInstance) { p.GitHubRunnerGroup = "Other" }, "runner group is outside"},
		{"image", func(p *commonParams.BootstrapInstance) { p.Image = "images:ubuntu/24.04" }, "is not the configured alias"},
		{"OS", func(p *commonParams.BootstrapInstance) { p.OSType = commonParams.Windows }, "only Linux"},
		{"architecture", func(p *commonParams.BootstrapInstance) { p.OSArch = commonParams.Arm64 }, "only amd64"},
		{"JIT", func(p *commonParams.BootstrapInstance) { p.JitConfigEnabled = false }, "JIT configuration is mandatory"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := validBootstrap()
			test.mutate(&params)
			require.ErrorContains(t, provider.validateBootstrapParams(params), test.message)
		})
	}
}

func TestWarmAssignmentDoesNotEnableTracingOrEmbedRawToken(t *testing.T) {
	caBundle := testCABundle(t)
	rendered := string(renderWarmAssignment(expectedMetadataURL, "opaque-test-token", caBundle, ""))
	require.NotContains(t, rendered, "opaque-test-token")
	require.NotContains(t, rendered, string(caBundle))
	require.NotContains(t, rendered, "set -x")
	require.Contains(t, rendered, "b3BhcXVlLXRlc3QtdG9rZW4=")
	require.Contains(t, rendered, base64.StdEncoding.EncodeToString(caBundle))
	require.Contains(t, rendered, expectedMetadataURL)
	require.Contains(t, rendered, `export CURL_CA_BUNDLE="${ca_file}"`)
	require.Contains(t, rendered, "export BASH_XTRACEFD=19")
	require.Contains(t, rendered, `/bin/bash "${install_script}"`)
}

func TestDirectJITWarmAssignmentUsesOnlyTheOfficialRunnerEntrypoint(t *testing.T) {
	encoded := testEncodedDirectJIT(t)
	rendered := string(renderWarmAssignment(expectedMetadataURL, "unused-metadata-token", testCABundle(t), encoded))
	require.Contains(t, rendered, `exec "${runner_root}/run.sh" --jitconfig "${JIT_CONFIG}"`)
	require.Contains(t, rendered, encoded)
	require.Contains(t, rendered, `"phase":"assignment-script-started"`)
	require.Contains(t, rendered, `"phase":"runner-exec"`)
	require.Contains(t, rendered, directJITPhasePath)
	require.Less(t, strings.Index(rendered, `"phase":"assignment-script-started"`), strings.Index(rendered, "# NDDEV_CACHE_SETUP_INSERTION_POINT"))
	require.Less(t, strings.Index(rendered, "# NDDEV_CACHE_SETUP_INSERTION_POINT"), strings.Index(rendered, `"phase":"runner-exec"`))
	require.NotContains(t, rendered, "unused-metadata-token")
	require.NotContains(t, rendered, expectedMetadataURL)
	require.NotContains(t, rendered, "curl ")
	require.NotContains(t, rendered, "systemctl")
	require.NotContains(t, rendered, "set -x")
	command := exec.Command("bash", "-n")
	command.Stdin = strings.NewReader(rendered)
	output, err := command.CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func TestWarmAssignmentSuppressesChildXTraceContainingToken(t *testing.T) {
	tempDir := t.TempDir()
	caBundle := testCABundle(t)
	caPathRecord := filepath.Join(tempDir, "ca-path")
	installPathRecord := filepath.Join(tempDir, "install-path")
	fakeCurl := filepath.Join(tempDir, "curl")
	require.NoError(t, os.WriteFile(fakeCurl, []byte(`#!/bin/bash
set -Eeuo pipefail
test -n "${CURL_CA_BUNDLE:-}"
test -s "${CURL_CA_BUNDLE}"
[[ "$(stat --format='%a' "${CURL_CA_BUNDLE}")" == 400 ]]
grep -q -- 'BEGIN CERTIFICATE' "${CURL_CA_BUNDLE}"
output=
while (($#)); do
  if [[ "$1" == -o ]]; then
    output="$2"
    shift 2
    continue
  fi
  shift
done
printf '%s' "${CURL_CA_BUNDLE}" >"${TEST_CA_PATH_RECORD}"
printf '%s' "${output}" >"${TEST_INSTALL_PATH_RECORD}"
cat >"${output}" <<'SCRIPT'
#!/bin/bash
set -x
secret=opaque-test-token
: "${secret}"
printf 'child-ok\n'
SCRIPT
`), 0o700))

	command := exec.Command("/bin/bash")
	command.Stdin = bytes.NewReader(renderWarmAssignment(expectedMetadataURL, "opaque-test-token", caBundle, ""))
	command.Env = append(os.Environ(),
		"PATH="+tempDir+":"+os.Getenv("PATH"),
		"TEST_CA_PATH_RECORD="+caPathRecord,
		"TEST_INSTALL_PATH_RECORD="+installPathRecord,
	)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	require.Contains(t, string(output), "child-ok")
	require.NotContains(t, string(output), "opaque-test-token")
	caPath, err := os.ReadFile(caPathRecord)
	require.NoError(t, err)
	installPath, err := os.ReadFile(installPathRecord)
	require.NoError(t, err)
	require.NoFileExists(t, string(caPath))
	require.NoFileExists(t, string(installPath))
}

func TestWarmAssignmentCleansTemporaryFilesWhenCurlFails(t *testing.T) {
	tempDir := t.TempDir()
	caPathRecord := filepath.Join(tempDir, "ca-path")
	installPathRecord := filepath.Join(tempDir, "install-path")
	fakeCurl := filepath.Join(tempDir, "curl")
	require.NoError(t, os.WriteFile(fakeCurl, []byte(`#!/bin/bash
set -Eeuo pipefail
output=
while (($#)); do
  if [[ "$1" == -o ]]; then
    output="$2"
    shift 2
    continue
  fi
  shift
done
test -n "${CURL_CA_BUNDLE:-}"
test -n "${output}"
printf '%s' "${CURL_CA_BUNDLE}" >"${TEST_CA_PATH_RECORD}"
printf '%s' "${output}" >"${TEST_INSTALL_PATH_RECORD}"
exit 22
`), 0o700))

	command := exec.Command("/bin/bash")
	command.Stdin = bytes.NewReader(renderWarmAssignment(expectedMetadataURL, "opaque-test-token", testCABundle(t), ""))
	command.Env = append(os.Environ(),
		"PATH="+tempDir+":"+os.Getenv("PATH"),
		"TEST_CA_PATH_RECORD="+caPathRecord,
		"TEST_INSTALL_PATH_RECORD="+installPathRecord,
	)
	output, err := command.CombinedOutput()
	require.Error(t, err, string(output))
	caPath, err := os.ReadFile(caPathRecord)
	require.NoError(t, err)
	installPath, err := os.ReadFile(installPathRecord)
	require.NoError(t, err)
	require.NoFileExists(t, string(caPath))
	require.NoFileExists(t, string(installPath))
}

func TestValidateBootstrapRequiresAnImageMappingForEveryAdmittedPool(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	provider.platform.Pools = append(provider.platform.Pools, platformconfig.Pool{Name: "nddev-linux-fast"})
	bootstrap := validBootstrap()
	bootstrap.Flavor = "nddev-linux-fast"

	require.ErrorContains(t, provider.validateBootstrapParams(bootstrap), "has no pinned worker image")
}

func TestGetCreateInstanceArgsPinsImageAndSecurityPolicy(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, testImageDigest)

	got, err := provider.getCreateInstanceArgs(context.Background(), validBootstrap(), extraSpecs{DisableUpdates: true})
	require.NoError(t, err)
	require.Equal(t, api.InstanceTypeVM, got.Type)
	require.Equal(t, testImageDigest, got.Source.Fingerprint)
	require.Empty(t, got.Source.Server)
	require.Empty(t, got.Source.Alias)
	require.Equal(t, []string{"nddev-linux-standard"}, got.Profiles)
	require.Equal(t, "GitHub one-job runner provisioned by GARM", got.Description)
	require.Equal(t, "#cloud-config", got.Config["user.user-data"])
	require.Equal(t, testControllerID, got.Config[controllerIDKeyName])
	require.Equal(t, "pool-test", got.Config[poolIDKey])
	require.Equal(t, testImageAlias, got.Config[imageAliasKey])
	require.Equal(t, testImageDigest, got.Config[imageFingerprintKey])
	require.Equal(t, Version, got.Config[providerVersionKey])
	require.Equal(t, Commit, got.Config[providerCommitKey])
	require.Equal(t, "nddev-linux-standard", got.Config[flavorKey])
	require.Equal(t, "ephemeral-one-job", got.Config[lifecycleKey])
	require.Equal(t, "trusted", got.Config[trustKey])
	require.Equal(t, "nddev-linux-standard", got.Config[scaleSetKey])
	require.Equal(t, "example-org/example-actions", got.Config[repositoryKey])
	require.Equal(t, "public-internet", got.Config[networkPolicyKey])
	require.Equal(t, "trusted", got.Config[cacheWriteScopeKey])
	require.Equal(t, "true", got.Config["security.secureboot"])
	require.NotContains(t, got.Config, "security.nesting")
	require.Equal(t, incuspolicy.DisableNestedVirtualizationRawQEMU, got.Config["raw.qemu"])
}

func TestGetCreateInstanceArgsBuildsUnprivilegedContainerWithoutVMDevices(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	provider.platform.Backends = append(provider.platform.Backends, platformconfig.Backend{
		Name: "linux-amd64-container", Platform: "linux", Architecture: "amd64", Implementation: "incus-container",
	})
	provider.platform.Pools = append(provider.platform.Pools, platformconfig.Pool{
		Name: "nddev-linux-fast", Backend: "linux-amd64-container", ScaleSetName: "nddev-linux-fast", Trust: "trusted",
		Capabilities: platformconfig.Capabilities{NetworkPolicy: "public-internet", CacheWriteScope: "none"},
	})
	provider.cfg.WorkerImages["nddev-linux-fast"] = config.WorkerImage{
		Alias: testContainerImageAlias, Fingerprint: testContainerImageDigest, Variant: "standard",
		InstanceType: config.IncusImageContainer, RunnerUID: 1001, RunnerGID: 1002,
	}
	prepareCreateMocksForType(cli, "nddev-linux-fast", testContainerImageAlias, testContainerImageDigest, "standard", config.IncusImageContainer)
	bootstrap := validBootstrap()
	bootstrap.Flavor = "nddev-linux-fast"
	bootstrap.Image = testContainerImageAlias
	got, err := provider.getCreateInstanceArgs(context.Background(), bootstrap, extraSpecs{DisableUpdates: true})
	require.NoError(t, err)
	require.Equal(t, api.InstanceTypeContainer, got.Type)
	require.Equal(t, "false", got.Config["security.privileged"])
	require.Equal(t, "false", got.Config["security.nesting"])
	require.Equal(t, "false", got.Config["security.syscalls.intercept.mknod"])
	require.Equal(t, "false", got.Config["security.syscalls.intercept.setxattr"])
	_, hasRawLXC := got.Config["raw.lxc"]
	require.False(t, hasRawLXC)
	require.NotContains(t, got.Config, "security.secureboot")
	require.NotContains(t, got.Config, "raw.qemu")
}

func TestGetCreateInstanceArgsEnablesNestingOnlyForDockerContainer(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	provider.platform.Backends = append(provider.platform.Backends, platformconfig.Backend{
		Name: "linux-amd64-container", Platform: "linux", Architecture: "amd64", Implementation: "incus-container",
		Capabilities: platformconfig.BackendCapabilities{Docker: true},
	})
	provider.platform.Pools = append(provider.platform.Pools, platformconfig.Pool{
		Name: "nddev-linux-docker-container-canary", Backend: "linux-amd64-container",
		ScaleSetName: "nddev-linux-docker-container-canary", Trust: "trusted",
		Capabilities: platformconfig.Capabilities{Docker: true, NetworkPolicy: "public-internet", CacheWriteScope: "none"},
	})
	provider.cfg.WorkerImages["nddev-linux-docker-container-canary"] = config.WorkerImage{
		Alias: testContainerImageAlias, Fingerprint: testContainerImageDigest, Variant: "integration",
		InstanceType: config.IncusImageContainer, RunnerUID: 1001, RunnerGID: 1002,
	}
	prepareCreateMocksForType(cli, "nddev-linux-docker-container-canary", testContainerImageAlias, testContainerImageDigest, "integration", config.IncusImageContainer)
	bootstrap := validBootstrap()
	bootstrap.Flavor = "nddev-linux-docker-container-canary"
	bootstrap.Image = testContainerImageAlias
	got, err := provider.getCreateInstanceArgs(context.Background(), bootstrap, extraSpecs{DisableUpdates: true})
	require.NoError(t, err)
	require.Equal(t, api.InstanceTypeContainer, got.Type)
	require.Equal(t, "false", got.Config["security.privileged"])
	require.Equal(t, "true", got.Config["security.nesting"])
	require.Equal(t, "false", got.Config["security.syscalls.intercept.mknod"])
	require.Equal(t, "false", got.Config["security.syscalls.intercept.setxattr"])
}

func TestGetCreateInstanceArgsSelectsIntegrationImageAndDockerGroup(t *testing.T) {
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

	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocksFor(
		cli,
		"nddev-linux-integration",
		testIntegrationImageAlias,
		testIntegrationImageDigest,
		"integration",
	)
	got, err := provider.getCreateInstanceArgs(
		context.Background(),
		validIntegrationBootstrap(),
		extraSpecs{DisableUpdates: true},
	)
	require.NoError(t, err)
	require.Equal(t, testIntegrationImageDigest, got.Source.Fingerprint)
	require.Equal(t, testIntegrationImageAlias, got.Config[imageAliasKey])
	require.Equal(t, "nddev-linux-integration", got.Config[flavorKey])
	require.Equal(t, []string{"nddev-linux-integration"}, got.Profiles)

	specs, err := parseExtraSpecsFromBootstrapParams(captured)
	require.NoError(t, err)
	require.Contains(t, string(specs.PreInstallScripts["01-nddev-runner-groups.sh"]), "usermod --groups sudo,docker runner")
}

func TestGetCreateInstanceArgsInjectsOnlyTrustedRunnerCacheBootstrap(t *testing.T) {
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

	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, testImageDigest)
	bootstrap := validBootstrap()
	_, err := provider.getCreateInstanceArgs(context.Background(), bootstrap, extraSpecs{
		DisableUpdates: true,
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: renderPinnedGARMV021LinuxWrapper(bootstrap.MetadataURL, bootstrap.InstanceToken),
		},
	})
	require.NoError(t, err)
	require.True(t, captured.UserDataOptions.DisableUpdatesOnBoot)
	require.Empty(t, captured.UserDataOptions.ExtraPackages)

	specs, err := parseExtraSpecsFromBootstrapParams(captured)
	require.NoError(t, err)
	require.True(t, specs.DisableUpdates)
	require.Len(t, specs.PreInstallScripts, 2)
	require.Empty(t, specs.RunnerInstallTemplate)
	require.Contains(t, string(specs.PreInstallScripts["00-nddev-runner-cache.sh"]), "/opt/cache/actions-runner/latest")
	require.Contains(t, string(specs.PreInstallScripts["01-nddev-runner-groups.sh"]), "usermod --groups sudo runner")
}

func TestRunnerToolMetadataMustMatchPinnedImageVersion(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	valid := testTools()[0]
	require.NoError(t, provider.validateRunnerTool(valid))

	wrongFilename := valid
	wrongFilename.Filename = ptr("actions-runner-linux-x64-2.337.0.tar.gz")
	require.ErrorContains(t, provider.validateRunnerTool(wrongFilename), "does not match pinned")

	wrongURL := valid
	wrongURL.DownloadURL = ptr("https://example.invalid/actions-runner-linux-x64-2.336.0.tar.gz")
	require.ErrorContains(t, provider.validateRunnerTool(wrongURL), "does not match pinned official URL")
}

func TestCanonicalRepositoryIdentity(t *testing.T) {
	for input, expected := range map[string]string{
		"https://github.com/example-org/example-actions":     "example-org/example-actions",
		"https://github.com/example-org/example-actions.git": "example-org/example-actions",
	} {
		got, err := canonicalRepositoryIdentity(input)
		require.NoError(t, err)
		require.Equal(t, expected, got)
	}
	for _, input := range []string{
		"http://github.com/owner/repo",
		"https://token@github.com/owner/repo",
		"https://github.com/owner/repo?token=x",
		"https://example.com/owner/repo",
		"https://github.com/owner",
	} {
		_, err := canonicalRepositoryIdentity(input)
		require.Error(t, err, input)
	}
}

func TestOrganizationBootstrapIsNarrowedThroughQueueIntent(t *testing.T) {
	bootstrap := validBootstrap()
	bootstrap.RepoURL = "https://github.com/example-org/"
	provider := &Incus{admission: repositoryResolvingAdmission{repository: "example-org/example-actions"}}

	resolved, err := provider.narrowBootstrapRepository(context.Background(), bootstrap)
	require.NoError(t, err)
	require.Equal(t, "https://github.com/example-org/example-actions", resolved.RepoURL)

	provider.admission = repositoryResolvingAdmission{err: errors.New("no concrete repository")}
	_, err = provider.narrowBootstrapRepository(context.Background(), bootstrap)
	require.ErrorContains(t, err, "cannot be narrowed through queue intent")
}

func TestWholeAccountBootstrapRetainsAccountIdentity(t *testing.T) {
	bootstrap := validBootstrap()
	bootstrap.RepoURL = "https://github.com/example-org"
	provider := &Incus{}
	resolved, err := provider.narrowBootstrapRepository(context.Background(), bootstrap)
	require.NoError(t, err)
	require.Equal(t, bootstrap.RepoURL, resolved.RepoURL)
	identity, err := canonicalRepositoryIdentity(resolved.RepoURL)
	require.NoError(t, err)
	require.Equal(t, "example-org", identity)
}

func TestGetCreateInstanceArgsRejectsImageAliasDrift(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	_, err := provider.getCreateInstanceArgs(context.Background(), validBootstrap(), extraSpecs{DisableUpdates: true})
	require.ErrorContains(t, err, "resolves to")
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestLaunchInstanceCreatesThenStarts(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	args := api.InstancesPost{Name: "runner-test-instance", Type: api.InstanceTypeVM}
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Twice()
	cli.On("CreateInstance", args).Return(op, nil).Once()
	cli.On("UpdateInstanceState", args.Name, api.InstanceStatePut{Action: "start", Timeout: -1}, "").Return(op, nil).Once()

	require.NoError(t, provider.launchInstance(context.Background(), args))
	cli.AssertExpectations(t)
}

func TestCreateInstanceReturnsOnlineOwnedVM(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, testImageDigest)
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Twice()
	cli.On("CreateInstance", mock.Anything).Return(op, nil).Once()
	cli.On("UpdateInstanceState", "runner-test-instance", api.InstanceStatePut{Action: "start", Timeout: -1}, "").Return(op, nil).Once()
	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", "runner-test-instance").Return(ownedInstance("runner-test-instance"), "", nil)

	got, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.NoError(t, err)
	require.Equal(t, "runner-test-instance", got.ProviderID)
	require.Equal(t, commonParams.Linux, got.OSType)
	require.Equal(t, commonParams.InstanceRunning, got.Status)
	require.Equal(t, []commonParams.Address{{Address: "198.51.100.22", Type: commonParams.PublicAddress}}, got.Addresses)
}

func TestCreateInstanceDeletesReservedWarmCapacityBeforeColdLaunch(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	control := &preemptingAdmission{}
	provider.admission = control
	prepareCreateMocks(cli, testImageDigest)

	warm := warmInstance("warm-standard-preempt")
	stopOperation := new(MockOperation)
	stopOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	deleteOperation := new(MockOperation)
	deleteOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	createOperation := new(MockOperation)
	createOperation.On("WaitContext", mock.Anything).Return(nil).Twice()

	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", warm.Name).Return(warm, "", nil).Twice()
	cli.On("UpdateInstanceState", warm.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").
		Return(stopOperation, nil).Once()
	cli.On("DeleteInstance", warm.Name).Return(deleteOperation, nil).Once()
	cli.On("CreateInstance", mock.Anything).Return(createOperation, nil).Once()
	cli.On("UpdateInstanceState", "runner-test-instance", api.InstanceStatePut{Action: "start", Timeout: -1}, "").
		Return(createOperation, nil).Once()
	cli.On("GetInstanceFull", "runner-test-instance").Return(ownedInstance("runner-test-instance"), "", nil)

	got, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.NoError(t, err)
	require.Equal(t, "runner-test-instance", got.ProviderID)
	require.Equal(t, 2, control.calls)
	require.Equal(t, []string{warm.Name}, control.markedDeleting)
	require.Equal(t, 1, control.reconciled)
	require.Empty(t, control.released)
	cli.AssertExpectations(t)
}

func TestCreateInstanceReleasesColdReservationWhenPostPreemptionAdmissionFails(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	control := &preemptingAdmission{rejectConfirmation: true}
	provider.admission = control
	prepareCreateMocks(cli, testImageDigest)

	warm := warmInstance("warm-standard-preempt")
	stopOperation := new(MockOperation)
	stopOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	deleteOperation := new(MockOperation)
	deleteOperation.On("WaitContext", mock.Anything).Return(nil).Once()

	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", warm.Name).Return(warm, "", nil).Twice()
	cli.On("UpdateInstanceState", warm.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").
		Return(stopOperation, nil).Once()
	cli.On("DeleteInstance", warm.Name).Return(deleteOperation, nil).Once()

	_, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.ErrorContains(t, err, "post-preemption admission rejected")
	require.Equal(t, 2, control.calls)
	require.Equal(t, []string{warm.Name}, control.markedDeleting)
	require.Equal(t, 1, control.reconciled)
	require.Equal(t, []string{"runner-test-instance"}, control.released)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
	cli.AssertExpectations(t)
}

func TestCreateInstanceClaimsRunningUnregisteredWarmVMWithoutColdCreate(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	warmControl := &warmAdmission{claim: provideradmission.WarmClaimResult{
		InstanceName: "warm-standard-0001",
		State:        providerjournal.ClaimReserved,
		Found:        true,
	}}
	provider.admission = warmControl
	warm := warmInstance(warmControl.claim.InstanceName)
	activated := ownedInstance(warmControl.claim.InstanceName)
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Once()

	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", warm.Name).Return(warm, "warm-etag", nil).Once()
	cli.On("UpdateInstance", warm.Name, mock.MatchedBy(func(update api.InstancePut) bool {
		return update.Config[lifecycleKey] == lifecycleEphemeralOneJob &&
			update.Config[poolIDKey] == "pool-test" &&
			update.Config[garmJobNameKey] == "runner-test-instance" &&
			update.Config[repositoryKey] == "example-org/example-actions" &&
			update.Config[warmReadyKey] == "" && update.Config["user.user-data"] == ""
	}), "warm-etag").Return(op, nil).Once()
	bootstrap := validBootstrap()
	bootstrap.CACertBundle = testCABundle(t)
	cli.On("CreateInstanceFile", warm.Name, warmAssignmentPath("runner-test-instance"), mock.MatchedBy(func(args incus.InstanceFileArgs) bool {
		if args.UID != 0 || args.GID != 0 || args.Mode != 0o700 || args.Type != "file" || args.WriteMode != "overwrite" {
			return false
		}
		content, err := io.ReadAll(args.Content)
		return err == nil && !strings.Contains(string(content), "opaque-test-token") &&
			strings.Contains(string(content), "b3BhcXVlLXRlc3QtdG9rZW4=") && strings.Contains(string(content), expectedMetadataURL) &&
			strings.Contains(string(content), base64.StdEncoding.EncodeToString(bootstrap.CACertBundle)) &&
			!strings.Contains(string(content), string(bootstrap.CACertBundle)) && !strings.Contains(string(content), "set -x")
	})).Return(nil).Once()
	cli.On("GetInstanceFull", warm.Name).Return(activated, "", nil).Once()

	got, err := provider.CreateInstance(context.Background(), bootstrap)
	require.NoError(t, err)
	require.Equal(t, "runner-test-instance", got.Name)
	require.Equal(t, warm.Name, got.ProviderID)
	require.True(t, warmControl.injected)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
	cli.AssertNotCalled(t, "GetImage", mock.Anything)
	cli.AssertNumberOfCalls(t, "CreateInstanceFile", 1)
	cli.AssertExpectations(t)
}

func TestCreateInstanceDirectJITClaimBypassesMetadataInstaller(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	warmControl := &warmAdmission{claim: provideradmission.WarmClaimResult{
		InstanceName: "warm-standard-direct-jit",
		State:        providerjournal.ClaimReserved,
		Found:        true,
	}}
	provider.admission = warmControl
	warm := warmInstance(warmControl.claim.InstanceName)
	activated := ownedInstance(warmControl.claim.InstanceName)
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Once()

	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", warm.Name).Return(warm, "warm-etag", nil).Once()
	cli.On("UpdateInstance", warm.Name, mock.Anything, "warm-etag").Return(op, nil).Once()
	bootstrap := directJITBootstrap(t)
	encoded := testEncodedDirectJIT(t)
	cli.On("CreateInstanceFile", warm.Name, warmAssignmentPath("runner-test-instance"), mock.MatchedBy(func(args incus.InstanceFileArgs) bool {
		content, err := io.ReadAll(args.Content)
		return err == nil && args.UID == 0 && args.GID == 0 && args.Mode == 0o700 &&
			strings.Contains(string(content), encoded) && strings.Contains(string(content), "--jitconfig") &&
			!strings.Contains(string(content), bootstrap.InstanceToken) && !strings.Contains(string(content), expectedMetadataURL)
	})).Return(nil).Once()
	cli.On("GetInstanceFile", warm.Name, directJITPhasePath).Return(
		io.NopCloser(strings.NewReader("{\"schema_version\":1,\"phase\":\"assignment-script-started\",\"unix_ns\":1786327000000000000}\n")),
		&incus.InstanceFileResponse{Type: "file", UID: 1001, GID: 1002, Mode: 0o600}, nil,
	).Once()
	cli.On("GetInstanceFull", warm.Name).Return(activated, "", nil).Once()

	got, err := provider.CreateInstance(context.Background(), bootstrap)
	require.NoError(t, err)
	require.Equal(t, "runner-test-instance", got.Name)
	require.Equal(t, warm.Name, got.ProviderID)
	require.True(t, warmControl.injected)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
	cli.AssertExpectations(t)
}

func TestCreateInstanceRetryAdoptsInjectedWarmClaimWithoutReinjecting(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	warmControl := &warmAdmission{claim: provideradmission.WarmClaimResult{
		InstanceName: "warm-standard-0001",
		State:        providerjournal.ClaimInjected,
		Found:        true,
	}}
	provider.admission = warmControl
	activated := ownedInstance(warmControl.claim.InstanceName)
	cli.On("GetInstanceFull", "runner-test-instance").Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("GetInstanceFull", activated.Name).Return(activated, "warm-etag", nil).Twice()

	got, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.NoError(t, err)
	require.Equal(t, "runner-test-instance", got.Name)
	require.Equal(t, activated.Name, got.ProviderID)
	cli.AssertNotCalled(t, "UpdateInstance", mock.Anything, mock.Anything, mock.Anything)
	cli.AssertNotCalled(t, "CreateInstanceFile", mock.Anything, mock.Anything, mock.Anything)
}

func TestCreateInstanceIdempotentlyAdoptsMatchingOwnedVM(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := ownedInstance("runner-test-instance")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()

	got, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.NoError(t, err)
	require.Equal(t, instance.Name, got.ProviderID)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestCreateInstanceRetryAdoptsVMCreatedBeforeAmbiguousOperationTimeout(t *testing.T) {
	stubCloudConfig(t)
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	prepareCreateMocks(cli, testImageDigest)
	instance := ownedInstance("runner-test-instance")
	createOperation := new(MockOperation)
	createOperation.On("WaitContext", mock.Anything).Return(context.DeadlineExceeded).Once()

	cli.On("GetInstanceFull", instance.Name).
		Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()
	cli.On("CreateInstance", mock.Anything).Return(createOperation, nil).Once()
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()

	_, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.ErrorContains(t, err, "waiting for instance creation")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	got, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.NoError(t, err)
	require.Equal(t, instance.Name, got.ProviderID)
	cli.AssertNumberOfCalls(t, "CreateInstance", 1)
	cli.AssertNotCalled(t, "UpdateInstanceState", mock.Anything, mock.Anything, mock.Anything)
	cli.AssertExpectations(t)
}

func TestCreateInstanceRejectsMutablePoolExtraSpecsBeforeIncusMutation(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	bootstrap := validBootstrap()
	bootstrap.ExtraSpecs = []byte(`{"disable_updates":true,"extra_packages":["curl"]}`)
	cli.On("GetInstanceFull", bootstrap.Name).Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()

	_, err := provider.CreateInstance(context.Background(), bootstrap)
	require.ErrorContains(t, err, "only disable_updates=true and the pinned GARM runtime wrapper are allowed")
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestCreateInstanceRejectsUnpinnedRuntimeWrapperBeforeIncusMutation(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	bootstrap := validBootstrap()
	bootstrap.InstanceToken = "sensitive-runtime-token"
	raw, err := json.Marshal(extraSpecs{
		DisableUpdates: true,
		CloudConfigSpec: cloudconfig.CloudConfigSpec{
			RunnerInstallTemplate: []byte("#!/bin/bash\necho unexpected\n"),
		},
	})
	require.NoError(t, err)
	bootstrap.ExtraSpecs = raw

	_, err = provider.CreateInstance(context.Background(), bootstrap)
	require.ErrorContains(t, err, "does not match the pinned GARM v0.2.1 Linux wrapper")
	require.NotContains(t, err.Error(), bootstrap.InstanceToken)
	require.NotContains(t, err.Error(), "echo unexpected")
	cli.AssertNotCalled(t, "GetInstanceFull", mock.Anything)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestCreateInstanceRefusesMismatchedExistingVM(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	instance := ownedInstance("runner-test-instance")
	instance.ExpandedConfig[imageFingerprintKey] = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Once()

	_, err := provider.CreateInstance(context.Background(), validBootstrap())
	require.ErrorContains(t, err, imageFingerprintKey)
	cli.AssertNotCalled(t, "CreateInstance", mock.Anything)
}

func TestGetInstanceRejectsForeignOwnership(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	foreign := ownedInstance("foreign")
	foreign.ExpandedConfig[controllerIDKeyName] = "different-controller"
	cli.On("GetInstanceFull", "foreign").Return(foreign, "", nil)

	_, err := provider.GetInstance(context.Background(), "foreign")
	require.ErrorContains(t, err, "is not owned by controller")
}

func TestDeleteInstanceChecksOwnershipBeforeMutation(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	admissionControl := &recordingAdmission{}
	provider.admission = admissionControl
	instance := ownedInstance("runner-test-instance")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Twice()
	cli.On("UpdateInstanceState", instance.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").Return(op, nil).Once()
	cli.On("DeleteInstance", instance.Name).Return(op, nil).Once()

	require.NoError(t, provider.DeleteInstance(context.Background(), instance.Name))
	require.Equal(t, 1, admissionControl.reconciled)
	require.Empty(t, admissionControl.released)
	cli.AssertExpectations(t)
}

func TestDeleteInstanceRetryReleasesVMDeletedBeforeAmbiguousOperationTimeout(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	admissionControl := &recordingAdmission{}
	provider.admission = admissionControl
	instance := ownedInstance("runner-test-instance")
	stopOperation := new(MockOperation)
	stopOperation.On("WaitContext", mock.Anything).Return(nil).Once()
	deleteOperation := new(MockOperation)
	deleteOperation.On("WaitContext", mock.Anything).Return(context.DeadlineExceeded).Once()

	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()
	cli.On("UpdateInstanceState", instance.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").
		Return(stopOperation, nil).Once()
	cli.On("DeleteInstance", instance.Name).Return(deleteOperation, nil).Once()
	cli.On("GetInstanceFull", instance.Name).
		Return((*api.InstanceFull)(nil), "", os.ErrNotExist).Once()

	err := provider.DeleteInstance(context.Background(), instance.Name)
	require.ErrorContains(t, err, "waiting for instance deletion")
	require.ErrorIs(t, err, context.DeadlineExceeded)

	require.NoError(t, provider.DeleteInstance(context.Background(), instance.Name))
	require.Equal(t, []string{instance.Name}, admissionControl.released)
	cli.AssertNumberOfCalls(t, "DeleteInstance", 1)
	cli.AssertNumberOfCalls(t, "UpdateInstanceState", 1)
	cli.AssertExpectations(t)
}

func TestDeleteInstanceNeverMutatesForeignVM(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	foreign := ownedInstance("foreign")
	foreign.ExpandedConfig[controllerIDKeyName] = "other-controller"
	cli.On("GetInstanceFull", foreign.Name).Return(foreign, "", nil).Once()

	err := provider.DeleteInstance(context.Background(), foreign.Name)
	require.ErrorContains(t, err, "authorizing instance deletion")
	cli.AssertNotCalled(t, "UpdateInstanceState", mock.Anything, mock.Anything, mock.Anything)
	cli.AssertNotCalled(t, "DeleteInstance", mock.Anything)
}

func TestDeleteInstanceContinuesAfterBoundedDiagnosticFailure(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	diagnostics := &recordingDiagnostics{err: errors.New("synthetic diagnostic failure")}
	provider.diagnostics = diagnostics
	instance := ownedInstance("runner-test-instance")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Twice()
	cli.On("UpdateInstanceState", instance.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").
		Run(func(mock.Arguments) {
			if !diagnostics.called {
				t.Fatal("instance stop began before diagnostic capture")
			}
		}).Return(op, nil).Once()
	cli.On("DeleteInstance", instance.Name).Return(op, nil).Once()

	require.NoError(t, provider.DeleteInstance(context.Background(), instance.Name))
	require.True(t, diagnostics.called)
	cli.AssertExpectations(t)
}

func TestForeignInstanceNeverReachesDiagnostics(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	diagnostics := &recordingDiagnostics{}
	provider.diagnostics = diagnostics
	foreign := ownedInstance("foreign")
	foreign.ExpandedConfig[controllerIDKeyName] = "other-controller"
	cli.On("GetInstanceFull", foreign.Name).Return(foreign, "", nil).Once()

	err := provider.DeleteInstance(context.Background(), foreign.Name)
	require.ErrorContains(t, err, "authorizing instance deletion")
	require.False(t, diagnostics.called)
}

func TestDeleteInstanceDoesNotWaitPastDiagnosticBudget(t *testing.T) {
	originalTimeout := diagnosticCollectionTimeout
	diagnosticCollectionTimeout = 10 * time.Millisecond
	t.Cleanup(func() { diagnosticCollectionTimeout = originalTimeout })

	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	provider.diagnostics = blockingDiagnostics{}
	instance := ownedInstance("runner-test-instance")
	cli.On("GetInstanceFull", instance.Name).Return(instance, "", nil).Twice()
	op := new(MockOperation)
	op.On("WaitContext", mock.Anything).Return(nil).Twice()
	cli.On("UpdateInstanceState", instance.Name, api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, "").Return(op, nil).Once()
	cli.On("DeleteInstance", instance.Name).Return(op, nil).Once()

	started := time.Now()
	require.NoError(t, provider.DeleteInstance(context.Background(), instance.Name))
	require.Less(t, time.Since(started), 500*time.Millisecond)
	cli.AssertExpectations(t)
}

func TestStartAndStopRequireOwnership(t *testing.T) {
	for _, test := range []struct {
		name  string
		state api.InstanceStatePut
		run   func(*Incus) error
	}{
		{"start", api.InstanceStatePut{Action: "start", Timeout: -1}, func(p *Incus) error { return p.Start(context.Background(), "runner") }},
		{"stop", api.InstanceStatePut{Action: "stop", Timeout: -1, Force: true}, func(p *Incus) error { return p.Stop(context.Background(), "runner", true) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := new(MockIncusServer)
			provider := newTestProvider(cli)
			cli.On("GetInstanceFull", "runner").Return(ownedInstance("runner"), "", nil).Once()
			op := new(MockOperation)
			op.On("WaitContext", mock.Anything).Return(nil).Once()
			cli.On("UpdateInstanceState", "runner", test.state, "").Return(op, nil).Once()
			require.NoError(t, test.run(provider))
			cli.AssertExpectations(t)
		})
	}
}

func TestListInstancesFiltersControllerAndPool(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	owned := *ownedInstance("owned")
	owned.ExpandedConfig[garmJobNameKey] = owned.Name
	wrongPool := *ownedInstance("wrong-pool")
	wrongPool.ExpandedConfig[garmJobNameKey] = wrongPool.Name
	wrongPool.ExpandedConfig[poolIDKey] = "other-pool"
	foreign := *ownedInstance("foreign")
	foreign.ExpandedConfig[garmJobNameKey] = foreign.Name
	foreign.ExpandedConfig[controllerIDKeyName] = "other-controller"
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{owned, wrongPool, foreign}, nil)

	got, err := provider.ListInstances(context.Background(), "pool-test")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "owned", got[0].Name)
	require.Equal(t, "owned", got[0].ProviderID)
}

func TestListInstancesProjectsClaimedWarmVMToLogicalGARMName(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	warmControl := &warmAdmission{claim: provideradmission.WarmClaimResult{
		InstanceName: "warm-standard-claimed",
		State:        providerjournal.ClaimInjected,
		Found:        true,
	}}
	provider.admission = warmControl
	claimed := *ownedInstance(warmControl.claim.InstanceName)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{claimed}, nil).Once()

	got, err := provider.ListInstances(context.Background(), "pool-test")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "runner-test-instance", got[0].Name)
	require.Equal(t, claimed.Name, got[0].ProviderID)
	cli.AssertExpectations(t)
}

func TestListInstancesRejectsUnboundWarmIdentityProjection(t *testing.T) {
	cli := new(MockIncusServer)
	provider := newTestProvider(cli)
	claimed := *ownedInstance("warm-standard-unbound")
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{claimed}, nil).Once()

	_, err := provider.ListInstances(context.Background(), "pool-test")
	require.ErrorContains(t, err, `GARM instance identity "runner-test-instance" resolves to "runner-test-instance" instead of provider instance "warm-standard-unbound"`)
	cli.AssertExpectations(t)
}

func TestGetVersion(t *testing.T) {
	provider := newTestProvider(new(MockIncusServer))
	require.Equal(t, Version, provider.GetVersion(context.Background()))
}
