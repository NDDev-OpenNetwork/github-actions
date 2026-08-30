// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachebroker"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incuspolicy"
	"github.com/NDDev-OpenNetwork/github-actions/internal/provideradmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/telemetryattrs"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	execution "github.com/cloudbase/garm-provider-common/execution/v0.1.0"

	incus "github.com/lxc/incus/v7/client"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/cloudbase/garm-provider-common/cloudconfig"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm-provider-common/util"
)

var _ execution.ExternalProvider = &Incus{}

var Version = "v0.0.0-unknown"
var Commit = "unknown"

const IncusSDKVersion = "v7.3.0"

const (
	expectedCallbackURL = "https://198.51.100.1:9443/api/v1/callbacks"
	expectedMetadataURL = "https://198.51.100.1:9443/api/v1/metadata"
	expectedRunnerGroup = "Default"
)

// The boundary stays closed and compile-time; it stopped being singular. It was
// one constant naming the only repository the fleet then had, which meant a
// second tenant reached this far — App verified, credential created, entity and
// scale set registered, job assigned — and only then failed, once per retry, on
// a check nothing upstream could see. The registry is the one place a tenant is
// onboarded, so the set is exactly as wide as that list and no wider.
var (
	expectedRepositoryURLs   = tenant.RepositoryURLs()
	expectedAccountURLPrefix = tenant.AccountURLPrefixes()
)

const (
	createOperationTimeout = 2 * time.Minute
	deleteOperationTimeout = time.Minute
	stateOperationTimeout  = time.Minute
	defaultPlacementLock   = "/var/lib/gha-fleet/placement.lock"
)

const (
	directJITPhasePath        = "/home/runner/actions-runner/_diag/nddev-direct-jit-phase.log"
	directJITPhaseMaxBytes    = 4096
	directJITStartWaitTimeout = 5 * time.Second
	directJITStartPoll        = 20 * time.Millisecond
)

const (
	// We look for this key in the config of the instances to determine if they are
	// created by us or not.
	controllerIDKeyName = "user.runner-controller-id"
	poolIDKey           = "user.runner-pool-id"
	imageAliasKey       = "user.nddev.image-alias"
	imageFingerprintKey = "user.nddev.image-fingerprint"
	providerVersionKey  = "user.nddev.provider-version"
	providerCommitKey   = "user.nddev.provider-commit"
	flavorKey           = "user.nddev.flavor"
	lifecycleKey        = "user.nddev.lifecycle"
	trustKey            = "user.nddev.trust"
	scaleSetKey         = "user.nddev.scale-set"
	repositoryKey       = "user.nddev.repository"
	networkPolicyKey    = "user.nddev.network-policy"
	cacheWriteScopeKey  = "user.nddev.cache-write-scope"
	garmJobNameKey      = "user.nddev.garm-job-name"
	warmReadyKey        = "user.nddev.warm-ready"

	// osTypeKeyName is the key we use in the instance config to indicate the OS
	// platform a runner is supposed to have. This value is defined in the pool and
	// passed into the provider as bootstrap params.
	osTypeKeyName = "user.os-type"

	// osArchKeyNAme is the key we use in the instance config to indicate the OS
	// architecture a runner is supposed to have. This value is defined in the pool and
	// passed into the provider as bootstrap params.
	osArchKeyNAme = "user.os-arch"
)

const (
	lifecycleEphemeralOneJob  = "ephemeral-one-job"
	lifecycleWarmPreparing    = "warm-preparing"
	lifecycleWarmUnregistered = "warm-unregistered"
	warmPoolIDPrefix          = "warm/"
	warmAssignmentDirectory   = "/run/gha-warm/assignments"
)

var (
	instanceTokenPattern                                = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
	configToIncusArchMap map[commonParams.OSArch]string = map[commonParams.OSArch]string{
		commonParams.Amd64: "x86_64",
		commonParams.Arm64: "aarch64",
		commonParams.Arm:   "armv7l",
	}

	incusToConfigArch map[string]commonParams.OSArch = map[string]commonParams.OSArch{
		"x86_64":  commonParams.Amd64,
		"aarch64": commonParams.Arm64,
		"armv7l":  commonParams.Arm,
	}
)

const (
	DefaultProjectDescription = "This project was created automatically by garm to be used for github ephemeral action runners."
	DefaultProjectName        = "garm-project"
)

type ToolFetchFunc func(osType commonParams.OSType, osArch commonParams.OSArch, tools []commonParams.RunnerApplicationDownload) (commonParams.RunnerApplicationDownload, error)

type GetCloudConfigFunc func(bootstrapParams commonParams.BootstrapInstance, tools commonParams.RunnerApplicationDownload, runnerName string) (string, error)

var (
	DefaultToolFetch      ToolFetchFunc      = util.GetTools
	DefaultGetCloudconfig GetCloudConfigFunc = cloudconfig.GetCloudConfig
)

func NewIncusProvider(configFile, controllerID string) (*Incus, error) {
	if controllerID == "" {
		return nil, runnerErrors.NewBadRequestError("missing controller ID")
	}
	cfg, err := config.NewConfig(configFile)
	if err != nil {
		return nil, errors.Wrap(err, "parsing config")
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "validating provider config")
	}
	if cfg.CurrentProviderIdentity.Version != Version || cfg.CurrentProviderIdentity.Commit != Commit {
		return nil, errors.Errorf(
			"provider config pins %s (%s), binary reports %s (%s)",
			cfg.CurrentProviderIdentity.Version, cfg.CurrentProviderIdentity.Commit, Version, Commit,
		)
	}

	provider := &Incus{
		cfg:               cfg,
		controllerID:      controllerID,
		placementLockPath: defaultPlacementLock,
		imageManager: &image{
			remotes: cfg.ImageRemotes,
		},
	}
	nddevAdmissionController, err := newNDDevAdmission(cfg, controllerID)
	if err != nil {
		return nil, errors.Wrap(err, "initializing admission controller")
	}
	provider.admission = nddevAdmissionController
	provider.platform = nddevAdmissionController.platform
	provider.diagnostics = newProviderDiagnostics(cfg, provider.platform.ControlPlane.RunnerVersion)
	provider.cacheClaim = productionCacheClaim
	provider.cacheClaimRandom = rand.Reader

	return provider, nil
}

type CompatibilityProbe struct {
	Compatible           bool     `json:"compatible"`
	Project              string   `json:"project"`
	Profile              string   `json:"profile"`
	ImageAlias           string   `json:"image_alias"`
	ImageFingerprint     string   `json:"image_fingerprint"`
	IncusSDKVersion      string   `json:"incus_sdk_version"`
	VisibleInstanceCount int      `json:"visible_instance_count"`
	VisibleInstances     []string `json:"visible_instances"`
	CacheDeliveryReady   bool     `json:"cache_delivery_ready"`
	CacheRole            string   `json:"cache_role,omitempty"`
}

func (l *Incus) ReconcileMaintenanceLeases(ctx context.Context, apply bool) (providerjournal.MaintenanceReconcileResult, error) {
	cli, err := l.getCLI(ctx)
	if err != nil {
		return providerjournal.MaintenanceReconcileResult{}, errors.Wrap(err, "connecting to Incus")
	}
	instances, err := cli.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return providerjournal.MaintenanceReconcileResult{}, errors.Wrap(err, "listing instances")
	}
	visible := make(map[string]struct{}, len(instances))
	for _, instance := range instances {
		visible[instance.Name] = struct{}{}
	}
	return providerjournal.ReconcileExpiredMaintenance(ctx, providerjournal.Store{
		Path: l.cfg.JournalFile, LockPath: l.cfg.JournalLockFile,
	}, visible, time.Now(), apply)
}

func (l *Incus) workerImagePolicy(flavor string) (config.WorkerImage, error) {
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return config.WorkerImage{}, runnerErrors.NewBadRequestError("pool policy %q does not exist", flavor)
	}
	image, exists := l.cfg.WorkerImageForFlavor(flavor)
	if !exists {
		return config.WorkerImage{}, runnerErrors.NewBadRequestError("pool %q has no pinned worker image", flavor)
	}
	backend, exists := l.platform.Backend(pool.Backend)
	if !exists {
		return config.WorkerImage{}, runnerErrors.NewBadRequestError("pool %q backend %q does not exist", flavor, pool.Backend)
	}
	wantedType := config.IncusImageVirtualMachine
	if backend.Implementation == "incus-container" {
		wantedType = config.IncusImageContainer
	}
	if image.InstanceType != wantedType {
		return config.WorkerImage{}, runnerErrors.NewUnprocessableError(
			"pool %q backend %q requires image type %q, configured %q",
			flavor, backend.Implementation, wantedType, image.InstanceType,
		)
	}
	return image, nil
}

func validateResolvedWorkerImage(image *api.Image, policy config.WorkerImage) error {
	if image == nil {
		return runnerErrors.NewUnprocessableError("image alias %q resolved without image metadata", policy.Alias)
	}
	if image.Fingerprint != policy.Fingerprint {
		return runnerErrors.NewUnprocessableError(
			"image alias %q resolves to %q, expected %q",
			policy.Alias,
			image.Fingerprint,
			policy.Fingerprint,
		)
	}
	if variant := image.Properties["user.nddev.image.variant"]; variant != policy.Variant {
		return runnerErrors.NewUnprocessableError(
			"image alias %q has variant %q, expected %q",
			policy.Alias,
			variant,
			policy.Variant,
		)
	}
	return nil
}

// Probe performs the same read-only API calls needed before GARM can create a
// worker. It intentionally does not touch the admission journal or mutate
// Incus, and is run as the production garm user during deployment.
func (l *Incus) Probe(ctx context.Context, profile string) (CompatibilityProbe, error) {
	if strings.TrimSpace(profile) == "" || profile != strings.TrimSpace(profile) {
		return CompatibilityProbe{}, runnerErrors.NewBadRequestError("profile is required")
	}
	imagePolicy, err := l.workerImagePolicy(profile)
	if err != nil {
		return CompatibilityProbe{}, err
	}
	pool, exists := l.platform.Pool(profile)
	if !exists {
		return CompatibilityProbe{}, runnerErrors.NewBadRequestError("pool policy %q does not exist", profile)
	}
	cacheRole, cacheEnabled, err := cacheRoleForPool(pool)
	if err != nil {
		return CompatibilityProbe{}, err
	}
	cacheReady := !cacheEnabled
	if cacheEnabled {
		if l.cacheClaim == nil {
			return CompatibilityProbe{}, fmt.Errorf("cache claim loader is not configured")
		}
		store, endpoint, ca, err := l.cacheClaim()
		if err != nil {
			return CompatibilityProbe{}, errors.Wrap(err, "probing cache claim")
		}
		defer clear(ca)
		if err := cachebroker.ValidateClaimEndpoint(endpoint); err != nil {
			return CompatibilityProbe{}, err
		}
		if _, err := store.Read(ctx); err != nil {
			return CompatibilityProbe{}, errors.Wrap(err, "reading cache claim journal")
		}
		cacheReady = true
	}
	cli, err := l.getCLI(ctx)
	if err != nil {
		return CompatibilityProbe{}, errors.Wrap(err, "connecting to Incus")
	}
	profiles, err := cli.GetProfileNames()
	if err != nil {
		return CompatibilityProbe{}, errors.Wrap(err, "listing profiles")
	}
	profileFound := false
	for _, candidate := range profiles {
		if candidate == profile {
			profileFound = true
			break
		}
	}
	if !profileFound {
		return CompatibilityProbe{}, runnerErrors.NewNotFoundError("profile %q does not exist", profile)
	}
	image, err := l.imageManager.getLocalImageByAlias(
		imagePolicy.Alias,
		imagePolicy.InstanceType,
		configToIncusArchMap[commonParams.Amd64],
		cli,
	)
	if err != nil {
		return CompatibilityProbe{}, errors.Wrap(err, "resolving pinned image")
	}
	if err := validateResolvedWorkerImage(image, imagePolicy); err != nil {
		return CompatibilityProbe{}, err
	}
	instances, err := cli.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return CompatibilityProbe{}, errors.Wrap(err, "listing instances")
	}
	instanceNames := make([]string, 0, len(instances))
	for _, instance := range instances {
		instanceNames = append(instanceNames, instance.Name)
	}
	sort.Strings(instanceNames)
	return CompatibilityProbe{
		Compatible:           true,
		Project:              l.cfg.ProjectName,
		Profile:              profile,
		ImageAlias:           imagePolicy.Alias,
		ImageFingerprint:     image.Fingerprint,
		IncusSDKVersion:      IncusSDKVersion,
		VisibleInstanceCount: len(instances),
		VisibleInstances:     instanceNames,
		CacheDeliveryReady:   cacheReady,
		CacheRole:            cacheRole,
	}, nil
}

// isRegisteredRepositoryURL reports whether GARM asked for a repository the
// fleet is registered to serve. Membership, not equality: the provider refuses
// anything the tenant registry does not name.
func isRegisteredRepositoryURL(repositoryURL string) bool {
	if _, registered := expectedRepositoryURLs[repositoryURL]; registered {
		return true
	}
	// A tenant that declared it serves a whole account receives jobs from
	// repositories the registry does not name one by one. The prefix still
	// pins the account, and the remainder must be a single path segment, so
	// this cannot be widened by a crafted URL into another account's space.
	for prefix := range expectedAccountURLPrefix {
		if repositoryURL == strings.TrimSuffix(prefix, "/") {
			return true
		}
		name, found := strings.CutPrefix(repositoryURL, prefix)
		if found && name != "" && !strings.Contains(name, "/") {
			return true
		}
	}
	return false
}

func (l *Incus) isRegisteredRepositoryURL(repositoryURL string) bool {
	if l == nil || l.cfg == nil {
		return isRegisteredRepositoryURL(repositoryURL)
	}
	parsed, err := url.ParseRequestURI(repositoryURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	identity := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
	parts := strings.Split(identity, "/")
	if len(parts) == 1 && parts[0] != "" {
		for _, account := range l.cfg.AllowedGitHubAccounts {
			if strings.EqualFold(parts[0], account) {
				return true
			}
		}
		return false
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, repository := range l.cfg.AllowedGitHubRepositories {
		if strings.EqualFold(identity, repository) {
			return true
		}
	}
	owner := parts[0]
	for _, account := range l.cfg.AllowedGitHubAccounts {
		if strings.EqualFold(owner, account) {
			return true
		}
	}
	return false
}

// repositoryWithinTenant reports whether a repository URL names something this
// exact tenant may serve. It is the same shape as isRegisteredRepositoryURL
// above, evaluated against one row instead of the whole registry.
//
// The registry-wide check answers "is this any tenant we serve", which is the
// question the provider needed while it served one account. It is not the
// question a pool asks. A pool is declared for a tenant, carries that tenant's
// trust class and cache write scope, and hands the worker a credential chosen
// from them; admitting another tenant's repository onto it gives that job the
// declaring tenant's privileges. Both checks now run, because the registry-wide
// one is still the cheaper refusal and neither is redundant.
func repositoryWithinTenant(selected tenant.Tenant, repositoryURL string) bool {
	if repositoryURL == "https://github.com/"+selected.Repository {
		return true
	}
	if !selected.ServesWholeAccount {
		return false
	}
	// An organization entity receives jobs from repositories the registry never
	// names one by one. The prefix pins the account and the remainder must be a
	// single path segment, so a crafted URL cannot widen this into another
	// account's space.
	prefix := "https://github.com/" + selected.Owner + "/"
	if repositoryURL == strings.TrimSuffix(prefix, "/") {
		return true
	}
	name, found := strings.CutPrefix(repositoryURL, prefix)
	return found && name != "" && !strings.Contains(name, "/")
}

// poolTenant resolves the account a pool is licensed to serve, and reports
// whether the pool declared one at all. Platform validation has already refused
// a pool naming a tenant the registry does not know, so an error here is a
// policy that changed underneath a running process.
//
// The second return is the whole point. Pool.TenantID defaults an undeclared
// tenant to the fleet's own, which is the right answer for reading a config and
// the wrong one for deciding admission: a host that declares no pool tenant is
// not declaring every pool for nddev, it is declaring nothing. Reading the
// default as a declaration refused every other tenant on every deployed host --
// observed on gha-runner-2, which serves example-guild/example-project through its
// own scale set on this same pool and had every create refused.
func (l *Incus) poolTenant(flavor string) (tenant.Tenant, bool, error) {
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return tenant.Tenant{}, false, runnerErrors.NewBadRequestError("pool policy %q does not exist", flavor)
	}
	if pool.Tenant == "" {
		return tenant.Tenant{}, false, nil
	}
	selected, err := tenant.ByID(pool.TenantID())
	if err != nil {
		return tenant.Tenant{}, false, runnerErrors.NewUnprocessableError("pool %q names an unknown tenant: %s", flavor, err)
	}
	return selected, true, nil
}

func (l *Incus) validateBootstrapParams(bootstrapParams commonParams.BootstrapInstance) error {
	switch {
	case bootstrapParams.Name == "":
		return runnerErrors.NewBadRequestError("missing name")
	case bootstrapParams.PoolID == "":
		return runnerErrors.NewBadRequestError("missing pool ID")
	case bootstrapParams.Flavor == "":
		return runnerErrors.NewBadRequestError("missing flavor")
	case !l.isRegisteredRepositoryURL(bootstrapParams.RepoURL):
		return runnerErrors.NewBadRequestError(
			"repository is outside the configured provider boundary: %q", bootstrapParams.RepoURL)
	case bootstrapParams.CallbackURL != l.expectedCallbackURL():
		return runnerErrors.NewBadRequestError("callback URL is outside the configured worker gateway boundary")
	case bootstrapParams.MetadataURL != l.expectedMetadataURL():
		return runnerErrors.NewBadRequestError("metadata URL is outside the configured worker gateway boundary")
	case !instanceTokenPattern.MatchString(bootstrapParams.InstanceToken):
		return runnerErrors.NewBadRequestError("missing or malformed instance token")
	case bootstrapParams.GitHubRunnerGroup != expectedRunnerGroup:
		return runnerErrors.NewBadRequestError("runner group is outside the configured repository boundary")
	case bootstrapParams.OSType != commonParams.Linux:
		return runnerErrors.NewBadRequestError("only Linux workers are supported")
	case bootstrapParams.OSArch != commonParams.Amd64:
		return runnerErrors.NewBadRequestError("only amd64 workers are supported")
	case !bootstrapParams.JitConfigEnabled:
		return runnerErrors.NewBadRequestError("JIT configuration is mandatory for one-job workers")
	}
	selectedTenant, declared, err := l.poolTenant(bootstrapParams.Flavor)
	if err != nil {
		return err
	}
	// A pool that declares no tenant keeps the registry-wide boundary
	// isRegisteredRepositoryURL already applied above. Narrowing to a tenant
	// nobody wrote down would refuse repositories the fleet is deployed to
	// serve, which is a fail-closed the operator never asked for.
	if declared && !repositoryWithinTenant(selectedTenant, bootstrapParams.RepoURL) {
		return runnerErrors.NewBadRequestError(
			"repository %q is outside the boundary of tenant %q declared by pool %q",
			bootstrapParams.RepoURL,
			selectedTenant.ID,
			bootstrapParams.Flavor,
		)
	}
	imagePolicy, err := l.workerImagePolicy(bootstrapParams.Flavor)
	if err != nil {
		return err
	}
	if bootstrapParams.Image != imagePolicy.Alias {
		return runnerErrors.NewBadRequestError(
			"image alias %q is not the configured alias %q for pool %q",
			bootstrapParams.Image,
			imagePolicy.Alias,
			bootstrapParams.Flavor,
		)
	}
	return nil
}

func (l *Incus) expectedCallbackURL() string { return l.cfg.WorkerGatewayURL + "/api/v1/callbacks" }

func (l *Incus) expectedMetadataURL() string { return l.cfg.WorkerGatewayURL + "/api/v1/metadata" }

func (l *Incus) validateExistingInstance(instance *api.InstanceFull, bootstrapParams commonParams.BootstrapInstance) error {
	if instance == nil {
		return runnerErrors.NewUnprocessableError("existing instance response is empty")
	}
	if err := l.validateManagedSecurity(instance); err != nil {
		return err
	}
	if !l.cfg.AllowsProviderIdentity(
		instance.ExpandedConfig[providerVersionKey], instance.ExpandedConfig[providerCommitKey], Version, Commit,
	) {
		return runnerErrors.NewUnprocessableError(
			"existing instance %q has unsupported provider identity %q@%q, current is %q@%q",
			instance.Name, instance.ExpandedConfig[providerVersionKey], instance.ExpandedConfig[providerCommitKey], Version, Commit,
		)
	}
	pool, exists := l.platform.Pool(bootstrapParams.Flavor)
	if !exists {
		return runnerErrors.NewBadRequestError("pool policy %q does not exist", bootstrapParams.Flavor)
	}
	imagePolicy, err := l.workerImagePolicy(bootstrapParams.Flavor)
	if err != nil {
		return err
	}
	repository, err := l.runtimeRepositoryIdentity(bootstrapParams.RepoURL)
	if err != nil {
		return runnerErrors.NewBadRequestError("invalid repository identity: %s", err)
	}
	checks := []struct {
		key      string
		expected string
	}{
		{controllerIDKeyName, l.controllerID},
		{poolIDKey, bootstrapParams.PoolID},
		{imageAliasKey, imagePolicy.Alias},
		{flavorKey, bootstrapParams.Flavor},
		{lifecycleKey, "ephemeral-one-job"},
		{trustKey, pool.Trust},
		{scaleSetKey, pool.ScaleSetName},
		{repositoryKey, repository},
		{networkPolicyKey, pool.Capabilities.NetworkPolicy},
		{cacheWriteScopeKey, pool.Capabilities.CacheWriteScope},
		{garmJobNameKey, bootstrapParams.Name},
		{osTypeKeyName, string(commonParams.Linux)},
		{osArchKeyNAme, string(commonParams.Amd64)},
	}
	if actual := instance.ExpandedConfig[imageFingerprintKey]; !imagePolicy.AllowsExistingFingerprint(actual) {
		return runnerErrors.NewUnprocessableError(
			"existing instance %q has %s=%q, expected current or declared previous fingerprint",
			instance.Name, imageFingerprintKey, actual,
		)
	}
	for _, check := range checks {
		if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
			return runnerErrors.NewUnprocessableError(
				"existing instance %q has %s=%q, expected %q",
				instance.Name,
				check.key,
				actual,
				check.expected,
			)
		}
	}
	if instance.Architecture != "x86_64" || len(instance.Profiles) != 1 || instance.Profiles[0] != bootstrapParams.Flavor {
		return runnerErrors.NewUnprocessableError(
			"existing instance %q does not have the exact architecture/profile policy",
			instance.Name,
		)
	}
	return nil
}

func (l *Incus) validateWarmInstance(instance *api.InstanceFull, bootstrapParams commonParams.BootstrapInstance) error {
	if err := l.validateWarmReadyMetadata(instance, bootstrapParams.Flavor); err != nil {
		return runnerErrors.NewUnprocessableError("%s", err)
	}
	return nil
}

func (l *Incus) validateManagedSecurity(instance *api.InstanceFull) error {
	if instance == nil {
		return runnerErrors.NewUnprocessableError("managed instance is missing")
	}
	flavor := instance.ExpandedConfig[flavorKey]
	imagePolicy, err := l.workerImagePolicy(flavor)
	if err != nil {
		return runnerErrors.NewUnprocessableError("managed instance %q image policy: %s", instance.Name, err)
	}
	wantedType, checks := managedSecurityContract(imagePolicy)
	if instance.Type != wantedType {
		return runnerErrors.NewUnprocessableError("managed instance %q has type %q, expected %q", instance.Name, instance.Type, wantedType)
	}
	lifecycle := instance.ExpandedConfig[lifecycleKey]
	actualFingerprint := instance.ExpandedConfig[imageFingerprintKey]
	fingerprintAllowed := actualFingerprint == imagePolicy.Fingerprint
	if lifecycle == lifecycleEphemeralOneJob {
		fingerprintAllowed = imagePolicy.AllowsExistingFingerprint(actualFingerprint)
	}
	if !fingerprintAllowed {
		return runnerErrors.NewUnprocessableError(
			"managed instance %q has %s=%q, expected exact current fingerprint for lifecycle %q or declared N-1 for an executing one-job worker",
			instance.Name, imageFingerprintKey, actualFingerprint, lifecycle,
		)
	}
	checks = append(checks, struct {
		key      string
		expected string
	}{lifecycleKey, "ephemeral-one-job"})
	for _, check := range checks {
		if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
			return runnerErrors.NewUnprocessableError(
				"managed instance %q has %s=%q, expected %q",
				instance.Name,
				check.key,
				actual,
				check.expected,
			)
		}
	}
	return nil
}

func managedSecurityContract(image config.WorkerImage) (string, []struct {
	key      string
	expected string
}) {
	checks := []struct {
		key      string
		expected string
	}{
		{imageAliasKey, image.Alias},
	}
	if image.InstanceType == config.IncusImageContainer {
		nesting := "false"
		if image.Variant == "integration" {
			nesting = "true"
		}
		return string(api.InstanceTypeContainer), append(checks,
			struct{ key, expected string }{"security.nesting", nesting},
			struct{ key, expected string }{"security.privileged", "false"},
			struct{ key, expected string }{"security.syscalls.intercept.mknod", "false"},
			struct{ key, expected string }{"security.syscalls.intercept.setxattr", "false"},
			struct{ key, expected string }{"raw.lxc", ""},
		)
	}
	return string(api.InstanceTypeVM), append(checks,
		struct{ key, expected string }{"security.nesting", "false"},
		struct{ key, expected string }{"security.secureboot", "true"},
		struct{ key, expected string }{"raw.qemu", incuspolicy.DisableNestedVirtualizationRawQEMU},
	)
}

type InstanceServerInterface interface {
	GetProject(string) (*api.Project, string, error)
	UseProject(string) incus.InstanceServer
	GetProfileNames() ([]string, error)
	CreateInstance(api.InstancesPost) (incus.Operation, error)
	UpdateInstanceState(string, api.InstanceStatePut, string) (incus.Operation, error)
	GetInstanceFull(string) (*api.InstanceFull, string, error)
	UpdateInstance(string, api.InstancePut, string) (incus.Operation, error)
	CreateInstanceFile(string, string, incus.InstanceFileArgs) error
	GetInstanceFile(string, string) (io.ReadCloser, *incus.InstanceFileResponse, error)
	DeleteInstance(string) (incus.Operation, error)
	GetInstances(api.InstanceType) ([]api.Instance, error)
	GetInstancesFull(api.InstanceType) ([]api.InstanceFull, error)
	GetImageAliasArchitectures(string, string) (map[string]*api.ImageAliasesEntry, error)
	GetImage(string) (*api.Image, string, error)
	// Cluster shape. A fleet that spans hosts answers admission from every
	// member's state, not from whichever machine happens to run the provider.
	GetServer() (*api.Server, string, error)
	GetClusterMembers() ([]api.ClusterMember, error)
	GetClusterMemberState(string) (*api.ClusterMemberState, string, error)
}

type Incus struct {
	// cfg is the provider config for this provider.
	cfg *config.Incus
	// cli is the Incus client.
	cli InstanceServerInterface
	// imageManager downloads images from remotes
	imageManager *image
	// controllerID is the ID of this controller
	controllerID string
	// admission serializes capacity leases across short-lived provider processes.
	admission admissionController
	// platform is the validated policy source for immutable ownership metadata.
	platform platformconfig.Config
	// diagnostics captures bounded, redacted evidence outside a worker before
	// the VM is stopped and destroyed.
	diagnostics      instanceDiagnosticCollector
	cacheClaim       cacheClaimLoader
	cacheClaimRandom io.Reader
	// placementLockPath serializes only the Incus placement request across
	// short-lived provider processes. Guest boot remains fully parallel.
	placementLockPath string

	mux sync.Mutex
}

func (l *Incus) getCLI(ctx context.Context) (InstanceServerInterface, error) {
	l.mux.Lock()
	defer l.mux.Unlock()

	if l.cli != nil {
		return l.cli, nil
	}
	cli, err := getClientFromConfig(ctx, l.cfg)
	if err != nil {
		return nil, errors.Wrap(err, "creating Incus client")
	}

	_, _, err = cli.GetProject(projectName(l.cfg))
	if err != nil {
		return nil, errors.Wrapf(err, "fetching project name: %s", projectName(l.cfg))
	}
	cli = cli.UseProject(projectName(l.cfg))
	l.cli = cli

	return cli, nil
}

func (l *Incus) getManagedInstance(ctx context.Context, instanceName string) (*api.InstanceFull, error) {
	cli, err := l.getCLI(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetching client")
	}
	instance, _, err := cli.GetInstanceFull(instanceName)
	if err != nil {
		if isNotFoundError(err) {
			return nil, errors.Wrapf(runnerErrors.ErrNotFound, "fetching instance: %q", err)
		}
		return nil, errors.Wrap(err, "fetching instance")
	}
	if instance.ExpandedConfig[controllerIDKeyName] != l.controllerID {
		return nil, runnerErrors.NewUnauthorizedError(fmt.Sprintf(
			"instance %q is not owned by controller %q",
			instanceName,
			l.controllerID,
		))
	}
	return instance, nil
}

func (l *Incus) getProfiles(ctx context.Context, flavor string) ([]string, error) {
	ret := []string{}
	if l.cfg.IncludeDefaultProfile {
		ret = append(ret, "default")
	}

	set := map[string]struct{}{}

	cli, err := l.getCLI(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fetching client")
	}

	profiles, err := cli.GetProfileNames()
	if err != nil {
		return nil, errors.Wrap(err, "fetching profile names")
	}
	for _, profile := range profiles {
		set[profile] = struct{}{}
	}

	if _, ok := set[flavor]; !ok {
		return nil, errors.Wrapf(runnerErrors.ErrNotFound, "looking for profile %s", flavor)
	}

	ret = append(ret, flavor)
	return ret, nil
}

// sadly, the security.secureboot flag is a string encoded boolean.
func (l *Incus) secureBootEnabled() string {
	if l.cfg.SecureBoot {
		return "true"
	}
	return "false"
}

func (l *Incus) getCreateInstanceArgs(ctx context.Context, bootstrapParams commonParams.BootstrapInstance, specs extraSpecs) (api.InstancesPost, error) {
	if err := l.validateBootstrapParams(bootstrapParams); err != nil {
		return api.InstancesPost{}, err
	}
	pool, exists := l.platform.Pool(bootstrapParams.Flavor)
	if !exists {
		return api.InstancesPost{}, runnerErrors.NewBadRequestError("pool policy %q does not exist", bootstrapParams.Flavor)
	}
	imagePolicy, err := l.workerImagePolicy(bootstrapParams.Flavor)
	if err != nil {
		return api.InstancesPost{}, err
	}
	profiles, err := l.getProfiles(ctx, bootstrapParams.Flavor)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "fetching profiles")
	}

	arch, err := resolveArchitecture(bootstrapParams.OSArch)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "fetching archictecture")
	}

	instanceType := imagePolicy.InstanceType
	imageDetails, err := l.imageManager.getLocalImageByAlias(bootstrapParams.Image, instanceType, arch, l.cli)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "getting local image")
	}
	if err := validateResolvedWorkerImage(imageDetails, imagePolicy); err != nil {
		return api.InstancesPost{}, err
	}
	instanceSource := api.InstanceSource{Type: "image", Fingerprint: imageDetails.Fingerprint}

	tools, err := DefaultToolFetch(bootstrapParams.OSType, bootstrapParams.OSArch, bootstrapParams.Tools)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "getting tools")
	}
	if err := l.validateRunnerTool(tools); err != nil {
		return api.InstancesPost{}, runnerErrors.NewUnprocessableError("runner tool metadata: %s", err)
	}

	bootstrapParams.UserDataOptions.DisableUpdatesOnBoot = specs.DisableUpdates
	bootstrapParams.UserDataOptions.ExtraPackages = specs.ExtraPackages
	bootstrapParams.UserDataOptions.EnableBootDebug = specs.EnableBootDebug
	_, cacheEnabled, err := cacheRoleForPool(pool)
	if err != nil {
		return api.InstancesPost{}, err
	}
	cacheEnabled = cacheEnabled && l.cacheClaim != nil
	bootstrapParams.ExtraSpecs, err = trustedBootstrapExtraSpecs(pool.Capabilities.Docker, cacheEnabled)
	if err != nil {
		return api.InstancesPost{}, err
	}
	cloudCfg, err := DefaultGetCloudconfig(bootstrapParams, tools, bootstrapParams.Name)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "generating cloud-config")
	}

	if bootstrapParams.OSType == commonParams.Windows {
		cloudCfg = fmt.Sprintf("#ps1_sysnative\n%s", cloudCfg)
	}

	configMap, err := l.jobMetadata(bootstrapParams)
	if err != nil {
		return api.InstancesPost{}, err
	}
	configMap["user.user-data"] = cloudCfg

	if instanceType == config.IncusImageVirtualMachine {
		configMap["security.secureboot"] = l.secureBootEnabled()
		// The reconciled profile already supplies security.nesting=false.
		// Incus 6.0 accepts that profile key but rejects it when duplicated in
		// an individual VM create request. raw.qemu is intentionally instance-
		// owned because profiles are not typed and the project permits this
		// closed low-level value only for the trusted provisioning boundary.
		for key, value := range incuspolicy.VMInstanceConfig() {
			configMap[key] = value
		}
	} else {
		configMap["security.privileged"] = "false"
		configMap["security.nesting"] = "false"
		if pool.Capabilities.Docker {
			configMap["security.nesting"] = "true"
		}
		configMap["security.syscalls.intercept.mknod"] = "false"
		configMap["security.syscalls.intercept.setxattr"] = "false"
	}

	args := api.InstancesPost{
		InstancePut: api.InstancePut{
			Architecture: arch,
			Profiles:     profiles,
			Description:  "GitHub one-job runner provisioned by GARM",
			Config:       configMap,
		},
		Source: instanceSource,
		Name:   bootstrapParams.Name,
		Type:   api.InstanceType(instanceType),
	}
	return args, nil
}

func (l *Incus) jobMetadata(bootstrapParams commonParams.BootstrapInstance) (map[string]string, error) {
	pool, exists := l.platform.Pool(bootstrapParams.Flavor)
	if !exists {
		return nil, runnerErrors.NewBadRequestError("pool policy %q does not exist", bootstrapParams.Flavor)
	}
	imagePolicy, err := l.workerImagePolicy(bootstrapParams.Flavor)
	if err != nil {
		return nil, err
	}
	repository, err := l.runtimeRepositoryIdentity(bootstrapParams.RepoURL)
	if err != nil {
		return nil, runnerErrors.NewBadRequestError("invalid repository identity: %s", err)
	}
	return map[string]string{
		osTypeKeyName:       string(bootstrapParams.OSType),
		osArchKeyNAme:       string(bootstrapParams.OSArch),
		controllerIDKeyName: l.controllerID,
		poolIDKey:           bootstrapParams.PoolID,
		imageAliasKey:       imagePolicy.Alias,
		imageFingerprintKey: imagePolicy.Fingerprint,
		providerVersionKey:  Version,
		providerCommitKey:   Commit,
		flavorKey:           bootstrapParams.Flavor,
		lifecycleKey:        lifecycleEphemeralOneJob,
		trustKey:            pool.Trust,
		scaleSetKey:         pool.ScaleSetName,
		repositoryKey:       repository,
		garmJobNameKey:      bootstrapParams.Name,
		networkPolicyKey:    pool.Capabilities.NetworkPolicy,
		cacheWriteScopeKey:  pool.Capabilities.CacheWriteScope,
	}, nil
}

func warmAssignmentPath(jobName string) string {
	digest := sha256.Sum256([]byte("nddev-warm-assignment-v1\x00" + jobName))
	return fmt.Sprintf("%s/%x.sh", warmAssignmentDirectory, digest[:])
}

func renderMetadataWarmAssignment(metadataURL, instanceToken string, caBundle []byte) []byte {
	encodedToken := base64.StdEncoding.EncodeToString([]byte(instanceToken))
	encodedCA := base64.StdEncoding.EncodeToString(caBundle)
	return []byte(fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
umask 077
# NDDEV_CACHE_SETUP_INSERTION_POINT
METADATA_URL=%q
TOKEN_B64=%q
CA_B64=%q
BEARER_TOKEN="$(printf '%%s' "${TOKEN_B64}" | base64 --decode)"
install_script="$(mktemp /tmp/gha-install.XXXXXXXXXX)"
ca_file=
cleanup() {
  rm -f -- "${install_script}"
  if [[ -n "${ca_file}" ]]; then
    rm -f -- "${ca_file}"
  fi
}
trap cleanup EXIT
if [[ -n "${CA_B64}" ]]; then
  ca_file="$(mktemp /tmp/gha-ca.XXXXXXXXXX.pem)"
  cat /etc/ssl/certs/ca-certificates.crt >"${ca_file}"
  printf '%%s' "${CA_B64}" | base64 --decode >>"${ca_file}"
  chmod 0400 "${ca_file}"
  test -s "${ca_file}"
  export CURL_CA_BUNDLE="${ca_file}"
fi
curl -H "Authorization: Bearer ${BEARER_TOKEN}" --retry 2 --retry-delay 5 --retry-connrefused --fail "${METADATA_URL}/install-script/" -o "${install_script}"
chmod 0700 "${install_script}"
# GARM's pinned runtime wrapper enables xtrace before it invokes the generated
# installer. Keep tracing output on an already-open /dev/null descriptor so a
# callback token can never reach the guest journal even if an upstream wrapper
# or installer enables xtrace. The descriptor and setting are inherited by
# every child Bash process; the assignment itself never exports the token.
exec 19>/dev/null
export BASH_XTRACEFD=19
/bin/bash "${install_script}"
`, metadataURL, encodedToken, encodedCA))
}

func renderDirectJITWarmAssignment(encodedJIT string) []byte {
	return []byte(fmt.Sprintf(`#!/bin/bash
set -Eeuo pipefail
umask 077
runner_root=/home/runner/actions-runner
phase_log=%q
test -d "${runner_root}/_diag"
phase_now="$(date +%%s%%N)"
[[ "${phase_now}" =~ ^[0-9]{19}$ ]]
printf '{"schema_version":1,"phase":"assignment-script-started","unix_ns":%%s}\n' "${phase_now}" >"${phase_log}"
chmod 0600 "${phase_log}"
# NDDEV_CACHE_SETUP_INSERTION_POINT
JIT_CONFIG=%q
test -x "${runner_root}/run.sh"
if find "${runner_root}" -type f \( -name .runner -o -name .credentials -o -name .credentials_rsaparams -o -name .service \) -print -quit | grep -q .; then
  echo "registration state exists before direct JIT activation" >&2
  exit 1
fi
phase_now="$(date +%%s%%N)"
[[ "${phase_now}" =~ ^[0-9]{19}$ ]]
printf '{"schema_version":1,"phase":"runner-exec","unix_ns":%%s}\n' "${phase_now}" >>"${phase_log}"
exec "${runner_root}/run.sh" --jitconfig "${JIT_CONFIG}"
`, directJITPhasePath, encodedJIT))
}

type directJITPhaseEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Phase         string `json:"phase"`
	UnixNS        int64  `json:"unix_ns"`
}

func (l *Incus) waitDirectJITAssignmentStarted(ctx context.Context, cli InstanceServerInterface, instanceName, flavor string) error {
	image, exists := l.cfg.WorkerImages[flavor]
	if !exists {
		return fmt.Errorf("worker image mapping %q is missing", flavor)
	}
	waitContext, cancel := context.WithTimeout(ctx, directJITStartWaitTimeout)
	defer cancel()
	ticker := time.NewTicker(directJITStartPoll)
	defer ticker.Stop()
	for {
		content, response, err := cli.GetInstanceFile(instanceName, directJITPhasePath)
		if err == nil {
			raw, readErr := io.ReadAll(io.LimitReader(content, directJITPhaseMaxBytes+1))
			closeErr := content.Close()
			if readErr != nil || closeErr != nil || len(raw) > directJITPhaseMaxBytes || response == nil ||
				response.Type != "file" || response.UID != image.RunnerUID || response.GID != image.RunnerGID || response.Mode != 0o600 {
				return fmt.Errorf("direct JIT phase evidence has invalid content or metadata")
			}
			firstLine, _, _ := strings.Cut(string(raw), "\n")
			var event directJITPhaseEvent
			if json.Unmarshal([]byte(firstLine), &event) != nil || event.SchemaVersion != 1 ||
				event.Phase != "assignment-script-started" || event.UnixNS <= 0 {
				return fmt.Errorf("direct JIT phase evidence is invalid")
			}
			return nil
		}
		select {
		case <-waitContext.Done():
			return fmt.Errorf("waiting for direct JIT assignment start: %w", waitContext.Err())
		case <-ticker.C:
		}
	}
}

func renderWarmAssignment(metadataURL, instanceToken string, caBundle []byte, encodedJIT string) []byte {
	if encodedJIT != "" {
		return renderDirectJITWarmAssignment(encodedJIT)
	}
	return renderMetadataWarmAssignment(metadataURL, instanceToken, caBundle)
}

func (l *Incus) activateWarmInstance(
	ctx context.Context,
	bootstrapParams commonParams.BootstrapInstance,
	claim provideradmission.WarmClaimResult,
	encodedJIT string,
) (commonParams.ProviderInstance, error) {
	if len(bootstrapParams.CACertBundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(bootstrapParams.CACertBundle) {
			return commonParams.ProviderInstance{}, runnerErrors.NewBadRequestError("worker CA bundle is not valid PEM")
		}
	}
	cli, err := l.getCLI(ctx)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching client")
	}
	instance, etag, err := cli.GetInstanceFull(claim.InstanceName)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching claimed warm instance")
	}
	switch instance.ExpandedConfig[lifecycleKey] {
	case lifecycleWarmUnregistered:
		if err := l.validateWarmInstance(instance, bootstrapParams); err != nil {
			return commonParams.ProviderInstance{}, err
		}
		metadata, err := l.jobMetadata(bootstrapParams)
		if err != nil {
			return commonParams.ProviderInstance{}, err
		}
		writable := instance.Writable()
		writable.Config = make(map[string]string, len(instance.Config)+len(metadata))
		for key, value := range instance.Config {
			writable.Config[key] = value
		}
		delete(writable.Config, "user.user-data")
		delete(writable.Config, warmReadyKey)
		for key, value := range metadata {
			writable.Config[key] = value
		}
		op, err := cli.UpdateInstance(instance.Name, writable, etag)
		if err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "binding warm instance metadata")
		}
		updateContext, cancel := context.WithTimeout(ctx, stateOperationTimeout)
		err = op.WaitContext(updateContext)
		cancel()
		if err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "waiting for warm metadata binding")
		}
	case lifecycleEphemeralOneJob:
		if err := l.validateExistingInstance(instance, bootstrapParams); err != nil {
			return commonParams.ProviderInstance{}, err
		}
	default:
		return commonParams.ProviderInstance{}, runnerErrors.NewUnprocessableError(
			"claimed warm instance %q has lifecycle %q",
			instance.Name,
			instance.ExpandedConfig[lifecycleKey],
		)
	}

	if claim.State != providerjournal.ClaimInjected {
		cacheAssignment, claimStore, cacheEnabled, err := l.renderCacheClaim(ctx, instance.Name, bootstrapParams)
		if err != nil {
			return commonParams.ProviderInstance{}, err
		}
		claimCommitted := claimStore != nil
		defer func() {
			if claimCommitted {
				_ = claimStore.Remove(context.Background(), instance.Name)
			}
		}()
		defer clear(cacheAssignment)
		assignment := renderWarmAssignment(l.expectedMetadataURL(), bootstrapParams.InstanceToken, bootstrapParams.CACertBundle, encodedJIT)
		if cacheEnabled {
			assignment = mergeCacheIntoWarmAssignment(assignment, cacheAssignment)
			if len(assignment) == 0 {
				return commonParams.ProviderInstance{}, fmt.Errorf("render one-job warm cache assignment")
			}
		}
		if err := cli.CreateInstanceFile(instance.Name, warmAssignmentPath(bootstrapParams.Name), incus.InstanceFileArgs{
			Content:   bytes.NewReader(assignment),
			UID:       0,
			GID:       0,
			Mode:      0o700,
			Type:      "file",
			WriteMode: "overwrite",
		}); err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "injecting one-job warm assignment")
		}
		clear(assignment)
		if err := l.admission.MarkWarmInjected(ctx, bootstrapParams.Name, instance.Name); err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "recording warm assignment injection")
		}
		claimCommitted = false
	}
	if encodedJIT != "" {
		if err := l.waitDirectJITAssignmentStarted(ctx, cli, instance.Name, bootstrapParams.Flavor); err != nil {
			return commonParams.ProviderInstance{}, err
		}
	}
	ret, err := l.waitInstanceHasIP(ctx, instance.Name)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching activated warm instance")
	}
	return l.projectGARMInstanceIdentity(ctx, instance, ret)
}

func (l *Incus) validateRunnerTool(tool commonParams.RunnerApplicationDownload) error {
	version := strings.TrimPrefix(l.platform.ControlPlane.RunnerVersion, "v")
	if version == "" || version == l.platform.ControlPlane.RunnerVersion {
		return fmt.Errorf("platform runner version must have a v prefix")
	}
	expectedFilename := fmt.Sprintf("actions-runner-linux-x64-%s.tar.gz", version)
	expectedURL := fmt.Sprintf(
		"https://github.com/actions/runner/releases/download/v%s/%s",
		version,
		expectedFilename,
	)
	if tool.GetFilename() != expectedFilename {
		return fmt.Errorf("filename %q does not match pinned %q", tool.GetFilename(), expectedFilename)
	}
	if tool.GetDownloadURL() != expectedURL {
		return fmt.Errorf("download URL %q does not match pinned official URL", tool.GetDownloadURL())
	}
	return nil
}

func canonicalRepositoryIdentity(value string) (string, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("repository URL must be an uncredentialed github.com HTTPS URL")
	}
	path := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 && parts[0] != "" && isRegisteredRepositoryURL(value) {
		return parts[0], nil
	}
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("repository URL must contain owner and repository")
	}
	return parts[0] + "/" + parts[1], nil
}

func (l *Incus) runtimeRepositoryIdentity(value string) (string, error) {
	identity, err := canonicalRepositoryIdentity(value)
	if err == nil {
		return identity, nil
	}
	if !l.isRegisteredRepositoryURL(value) {
		return "", err
	}
	parsed, parseErr := url.ParseRequestURI(value)
	if parseErr != nil {
		return "", err
	}
	account := strings.Trim(parsed.Path, "/")
	if account == "" || strings.Contains(account, "/") {
		return "", err
	}
	return account, nil
}

func (l *Incus) narrowBootstrapRepository(ctx context.Context, bootstrap commonParams.BootstrapInstance) (commonParams.BootstrapInstance, error) {
	identity, identityErr := canonicalRepositoryIdentity(bootstrap.RepoURL)
	if identityErr == nil && strings.Contains(identity, "/") {
		if !l.isRegisteredRepositoryURL(bootstrap.RepoURL) {
			return bootstrap, runnerErrors.NewBadRequestError(
				"repository is outside the configured provider boundary: %q", bootstrap.RepoURL)
		}
		return bootstrap, nil
	}
	// An organization entity gives GARM only the account URL. It is an allowed
	// account but not an executable repository identity; resolve the one exact
	// repository from the active pre-AcquireJobs intent before any admission,
	// cache or instance metadata is derived from it.
	resolver, ok := l.admission.(repositoryResolver)
	if !ok {
		return bootstrap, fmt.Errorf("repository resolver is not configured")
	}
	repository, err := resolver.ResolveRepository(ctx, bootstrap)
	if err != nil {
		if l.isRegisteredRepositoryURL(bootstrap.RepoURL) {
			// GitHub's organization JobAssigned message has no repository and
			// JobAvailable is emitted only after a runner exists. The first worker
			// may therefore carry the reviewed account identity, but cache delivery
			// separately refuses this ambiguous shape. A later repository-bound
			// event remains the authoritative enrichment path.
			return bootstrap, nil
		}
		return bootstrap, runnerErrors.NewBadRequestError(
			"repository %q cannot be narrowed through queue intent: %s", bootstrap.RepoURL, err)
	}
	bootstrap.RepoURL = "https://github.com/" + repository
	if !l.isRegisteredRepositoryURL(bootstrap.RepoURL) {
		return bootstrap, runnerErrors.NewBadRequestError(
			"queue intent resolved repository outside the configured provider boundary: %q", repository)
	}
	return bootstrap, nil
}

func (l *Incus) launchInstance(ctx context.Context, createArgs api.InstancesPost) error {
	cli, err := l.getCLI(ctx)
	if err != nil {
		return errors.Wrap(err, "fetching client")
	}
	var placement *placementLock
	if l.placementLockPath != "" {
		placement, err = acquirePlacementLock(ctx, l.placementLockPath)
		if err != nil {
			return errors.Wrap(err, "serializing Incus placement request")
		}
	}
	// Get Incus to create the instance (background operation). Release the
	// placement lock as soon as Incus has durably selected a member; waiting for
	// boot here would serialize the expensive part of every cold start.
	op, err := cli.CreateInstance(createArgs)
	if placement != nil {
		unlockErr := placement.Close()
		if err == nil && unlockErr != nil {
			return errors.Wrap(unlockErr, "releasing Incus placement request")
		}
	}
	if err != nil {
		return errors.Wrap(err, "creating instance")
	}

	// Wait for the operation to complete
	createContext, cancelCreate := context.WithTimeout(ctx, createOperationTimeout)
	err = op.WaitContext(createContext)
	cancelCreate()
	if err != nil {
		return errors.Wrap(err, "waiting for instance creation")
	}

	// Get Incus to start the instance (background operation)
	reqState := api.InstanceStatePut{
		Action:  "start",
		Timeout: -1,
	}

	op, err = cli.UpdateInstanceState(createArgs.Name, reqState, "")
	if err != nil {
		return errors.Wrap(err, "starting instance")
	}

	// Wait for the operation to complete
	startContext, cancelStart := context.WithTimeout(ctx, stateOperationTimeout)
	err = op.WaitContext(startContext)
	cancelStart()
	if err != nil {
		return errors.Wrap(err, "waiting for instance to start")
	}
	return nil
}

// CreateInstance creates a new compute instance in the provider.
func (l *Incus) CreateInstance(ctx context.Context, bootstrapParams commonParams.BootstrapInstance) (result commonParams.ProviderInstance, err error) {
	ctx, span := otel.Tracer("nddev.drakkars.provider").Start(ctx, "provider.create_instance", trace.WithAttributes(
		attribute.String(telemetryattrs.RunnerName, bootstrapParams.Name),
		attribute.String(telemetryattrs.RunnerPool, bootstrapParams.Flavor),
	))
	defer finishProviderSpan(span, &err)
	bootstrapParams, err = l.narrowBootstrapRepository(ctx, bootstrapParams)
	if err != nil {
		return commonParams.ProviderInstance{}, err
	}
	if err := l.validateBootstrapParams(bootstrapParams); err != nil {
		return commonParams.ProviderInstance{}, err
	}
	extraSpecs, err := parseExtraSpecsFromBootstrapParams(bootstrapParams)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "parsing extra specs")
	}
	if err := validateNDDevExtraSpecs(bootstrapParams, extraSpecs); err != nil {
		return commonParams.ProviderInstance{}, runnerErrors.NewBadRequestError("invalid extra specs: %s", err)
	}
	existing, existingErr := l.getManagedInstance(ctx, bootstrapParams.Name)
	switch {
	case existingErr == nil:
		if err := l.validateExistingInstance(existing, bootstrapParams); err != nil {
			return commonParams.ProviderInstance{}, err
		}
		if l.admission == nil {
			return commonParams.ProviderInstance{}, fmt.Errorf("admission controller is not configured")
		}
		if err := l.admission.Reconcile(ctx, l.cli); err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "reconciling existing instance")
		}
		if existing.State != nil && existing.State.Status == "Stopped" {
			if err := l.Start(ctx, bootstrapParams.Name); err != nil {
				return commonParams.ProviderInstance{}, errors.Wrap(err, "restarting existing instance")
			}
		}
		present, err := l.coldCacheDeliveryPresent(ctx, bootstrapParams.Name, bootstrapParams)
		if err != nil {
			return commonParams.ProviderInstance{}, err
		}
		if !present {
			if err := l.injectColdCacheAssignment(ctx, bootstrapParams.Name, bootstrapParams); err != nil {
				return commonParams.ProviderInstance{}, err
			}
		}
		ret, err := l.waitInstanceHasIP(ctx, bootstrapParams.Name)
		if err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching existing instance")
		}
		return ret, nil
	case !isNotFoundError(existingErr):
		return commonParams.ProviderInstance{}, errors.Wrap(existingErr, "checking for an existing instance")
	}
	if l.admission == nil {
		return commonParams.ProviderInstance{}, fmt.Errorf("admission controller is not configured")
	}
	claim, err := l.admission.ClaimWarm(ctx, l.cli, bootstrapParams)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "claiming warm instance")
	}
	span.SetAttributes(attribute.Bool(telemetryattrs.RunnerWarmClaimed, claim.Found))
	if claim.Found {
		return l.activateWarmInstance(ctx, bootstrapParams, claim, extraSpecs.EncodedJITConfig)
	}

	args, err := l.getCreateInstanceArgs(ctx, bootstrapParams, extraSpecs)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching create args")
	}
	admissionResult, err := l.admission.Admit(ctx, l.cli, bootstrapParams)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "evaluating provider admission")
	}
	span.SetAttributes(
		attribute.String(telemetryattrs.AdmissionReason, string(admissionResult.Decision.Reason)),
		attribute.Int(telemetryattrs.AdmissionPreemptedWorkers, len(admissionResult.PreemptedWarmWorkers)),
	)
	if !admissionResult.Decision.Admitted && len(admissionResult.PreemptedWarmWorkers) == 0 {
		return commonParams.ProviderInstance{}, runnerErrors.NewNoPoolsAvailableError(
			"provider admission rejected pool %q: %s",
			bootstrapParams.Flavor,
			admissionResult.Decision.Reason,
		)
	}
	for _, warmInstance := range admissionResult.PreemptedWarmWorkers {
		if err := l.DeleteInstance(ctx, warmInstance); err != nil {
			return commonParams.ProviderInstance{}, errors.Wrapf(err, "preempting warm instance %q", warmInstance)
		}
	}
	if len(admissionResult.PreemptedWarmWorkers) > 0 {
		confirmed, err := l.admission.Admit(ctx, l.cli, bootstrapParams)
		if err != nil {
			return commonParams.ProviderInstance{}, errors.Wrap(err, "confirming capacity after warm preemption")
		}
		if !confirmed.Decision.Admitted || len(confirmed.PreemptedWarmWorkers) != 0 {
			if releaseErr := l.admission.Release(ctx, bootstrapParams.Name); releaseErr != nil {
				return commonParams.ProviderInstance{}, errors.Wrapf(
					releaseErr,
					"post-preemption admission rejected pool %q with reason %q and reservation cleanup failed",
					bootstrapParams.Flavor,
					confirmed.Decision.Reason,
				)
			}
			return commonParams.ProviderInstance{}, runnerErrors.NewNoPoolsAvailableError(
				"post-preemption admission rejected pool %q: %s",
				bootstrapParams.Flavor,
				confirmed.Decision.Reason,
			)
		}
	}

	span.AddEvent("incus.launch_started")
	if err := l.launchInstance(ctx, args); err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "creating instance")
	}
	span.AddEvent("incus.launch_completed")
	if err := l.injectColdCacheAssignment(ctx, args.Name, bootstrapParams); err != nil {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		cleanupErr := l.DeleteInstance(cleanupContext, args.Name)
		cancel()
		return commonParams.ProviderInstance{}, errors.Wrapf(err, "cache delivery failed; cleanup result: %v", cleanupErr)
	}
	if err := l.admission.MarkCreated(ctx, args.Name); err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "recording created instance")
	}

	ret, err := l.waitInstanceHasIP(ctx, args.Name)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "fetching instance")
	}
	span.AddEvent("instance.ready")

	return ret, nil
}

func finishProviderSpan(span trace.Span, err *error) {
	if err != nil && *err != nil {
		span.SetStatus(codes.Error, "provider operation failed")
		span.SetAttributes(attribute.String(telemetryattrs.OperationOutcome, telemetryattrs.OutcomeError))
	} else {
		span.SetStatus(codes.Ok, "")
		span.SetAttributes(attribute.String(telemetryattrs.OperationOutcome, telemetryattrs.OutcomeSuccess))
	}
	span.End()
}

// setIncusMemberAttribute preserves the distinction between the services host
// executing the provider process and the compute member owning the container.
// Placement telemetry is best-effort: an absent or malformed location must not
// turn an otherwise healthy job into a provider failure.
func setIncusMemberAttribute(span trace.Span, member string) bool {
	if span == nil || member == "" || len(member) > 255 || strings.TrimSpace(member) != member ||
		strings.ContainsAny(member, "\r\n\x00") {
		return false
	}
	span.SetAttributes(attribute.String(telemetryattrs.IncusMember, member))
	return true
}

// GetInstance will return details about one instance.
func (l *Incus) GetInstance(ctx context.Context, instanceName string) (commonParams.ProviderInstance, error) {
	if l.admission == nil {
		return commonParams.ProviderInstance{}, fmt.Errorf("admission controller is not configured")
	}
	resolved, err := l.admission.Resolve(ctx, instanceName)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "resolving instance identity")
	}
	instanceName = resolved
	instance, err := l.getManagedInstance(ctx, instanceName)
	if err != nil {
		return commonParams.ProviderInstance{}, err
	}
	setIncusMemberAttribute(trace.SpanFromContext(ctx), instance.Location)

	return l.projectGARMInstanceIdentity(ctx, instance, incusInstanceToAPIInstance(instance))
}

// projectGARMInstanceIdentity preserves the two identities of a claimed warm
// worker. ProviderID continues to name the physical Incus VM, while Name must
// remain the logical GARM runner identity used by its database and Scale Set
// reconciler. Returning the physical warm name in both fields makes GARM see
// one real VM as two inconsistent runners and may trigger teardown while the
// official runner is executing a job.
//
// The projection fails closed unless the durable admission claim resolves the
// job name back to this exact VM. Instance metadata alone is not sufficient
// authority to rewrite provider identity.
func (l *Incus) projectGARMInstanceIdentity(
	ctx context.Context,
	instance *api.InstanceFull,
	providerInstance commonParams.ProviderInstance,
) (commonParams.ProviderInstance, error) {
	jobName := instance.ExpandedConfig[garmJobNameKey]
	if jobName == "" {
		return providerInstance, nil
	}
	if l.admission == nil {
		return commonParams.ProviderInstance{}, fmt.Errorf("admission controller is not configured")
	}
	resolved, err := l.admission.Resolve(ctx, jobName)
	if err != nil {
		return commonParams.ProviderInstance{}, errors.Wrap(err, "resolving GARM instance identity")
	}
	if resolved != instance.Name {
		return commonParams.ProviderInstance{}, fmt.Errorf(
			"GARM instance identity %q resolves to %q instead of provider instance %q",
			jobName,
			resolved,
			instance.Name,
		)
	}
	providerInstance.Name = jobName
	providerInstance.ProviderID = instance.Name
	return providerInstance, nil
}

// Delete instance will delete the instance in a provider.
func (l *Incus) DeleteInstance(ctx context.Context, instance string) (err error) {
	ctx, span := otel.Tracer("nddev.drakkars.provider").Start(ctx, "provider.delete_instance", trace.WithAttributes(
		attribute.String(telemetryattrs.RunnerName, instance),
	))
	defer finishProviderSpan(span, &err)
	if l.admission == nil {
		return fmt.Errorf("admission controller is not configured")
	}
	resolved, err := l.admission.Resolve(ctx, instance)
	if err != nil {
		return errors.Wrap(err, "resolving instance identity")
	}
	instance = resolved
	cli, err := l.getCLI(ctx)
	if err != nil {
		return errors.Wrap(err, "fetching client")
	}
	managedInstance, err := l.getManagedInstance(ctx, instance)
	if err != nil {
		if isNotFoundError(err) {
			return l.admission.Release(ctx, instance)
		}
		return errors.Wrap(err, "authorizing instance deletion")
	}
	setIncusMemberAttribute(span, managedInstance.Location)
	l.captureDiagnosticsBeforeTeardown(ctx, managedInstance)
	span.AddEvent("diagnostics.capture_attempted")
	if err := l.admission.MarkDeleting(ctx, instance); err != nil {
		return errors.Wrap(err, "recording deleting instance")
	}

	if err := l.setState(ctx, instance, "stop", true); err != nil {
		if isNotFoundError(err) {
			return l.admission.Release(ctx, instance)
		}
		// I am not proud of this, but the drivers.ErrInstanceIsStopped from Incus pulls in
		// a ton of CGO, linux specific dependencies, that don't make sense having
		// in garm.
		if !(errors.Cause(err).Error() == errInstanceIsStopped.Error()) {
			return errors.Wrap(err, "stopping instance")
		}
	}

	opResponse := make(chan struct {
		op  incus.Operation
		err error
	}, 1)
	var op incus.Operation
	go func() {
		op, err := cli.DeleteInstance(instance)
		opResponse <- struct {
			op  incus.Operation
			err error
		}{op: op, err: err}
	}()

	deleteTimer := time.NewTimer(deleteOperationTimeout)
	defer deleteTimer.Stop()
	select {
	case resp := <-opResponse:
		if resp.err != nil {
			if isNotFoundError(resp.err) {
				return l.admission.Release(ctx, instance)
			}
			return errors.Wrap(resp.err, "removing instance")
		}
		op = resp.op
	case <-deleteTimer.C:
		return errors.Wrapf(runnerErrors.ErrTimeout, "removing instance %s", instance)
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "waiting to start instance deletion")
	}

	opTimeout, cancel := context.WithTimeout(ctx, deleteOperationTimeout)
	defer cancel()
	err = op.WaitContext(opTimeout)
	if err != nil {
		if isNotFoundError(err) {
			return l.admission.Release(ctx, instance)
		}
		return errors.Wrap(err, "waiting for instance deletion")
	}
	// A completed cluster delete operation can precede convergence of the
	// global instance inventory. Releasing the durable lease here made a
	// briefly visible tombstone (with no ExpandedConfig) look unowned to a
	// concurrent create. Reconcile against the same inventory used by
	// admission instead: an absent instance releases the lease immediately,
	// while a visible tombstone retains its deleting lease until a subsequent
	// reconciliation observes absence.
	if err := l.admission.Reconcile(ctx, cli); err != nil {
		return errors.Wrap(err, "reconciling deleted instance visibility")
	}
	span.AddEvent("admission.delete_visibility_reconciled")
	return nil
}

func (l *Incus) captureDiagnosticsBeforeTeardown(ctx context.Context, instance *api.InstanceFull) {
	if l.diagnostics == nil {
		slog.Warn("worker diagnostics collector is unavailable", "instance", instance.Name)
		return
	}
	diagnosticContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), diagnosticCollectionTimeout)
	defer cancel()
	outcomes := make(chan diagnosticCaptureOutcome, 1)
	go func() {
		result, err := l.diagnostics.Capture(diagnosticContext, l.cli, instance)
		outcomes <- diagnosticCaptureOutcome{result: result, err: err}
	}()
	var outcome diagnosticCaptureOutcome
	select {
	case outcome = <-outcomes:
	case <-diagnosticContext.Done():
		slog.Warn(
			"worker diagnostics capture exceeded its budget; teardown continues",
			"instance", instance.Name,
			"error", diagnosticContext.Err(),
		)
		return
	}
	if outcome.err != nil {
		slog.Warn("worker diagnostics capture failed; teardown continues", "instance", instance.Name, "error", outcome.err)
		return
	}
	slog.Info(
		"worker diagnostics captured",
		"instance", instance.Name,
		"path", outcome.result.Path,
		"artifacts", outcome.result.ArtifactCount,
		"collection_failures", outcome.result.CollectionFailures,
	)
}

type listResponse struct {
	instances []api.Instance
	err       error
}

// ListInstances will list all instances for a provider.
func (l *Incus) ListInstances(ctx context.Context, poolID string) ([]commonParams.ProviderInstance, error) {
	cli, err := l.getCLI(ctx)
	if err != nil {
		return []commonParams.ProviderInstance{}, errors.Wrap(err, "fetching client")
	}

	result := make(chan listResponse, 1)

	go func() {
		// TODO(gabriel-samfira): if this blocks indefinitely, we will leak a goroutine.
		// Convert the internal provider to an external one. Running the provider as an
		// external process will allow us to not care if a goroutine leaks. Once a timeout
		// is reached, the provider can just exit with an error. Something we can't do with
		// internal providers.
		instances, err := cli.GetInstances(api.InstanceTypeAny)
		result <- listResponse{
			instances: instances,
			err:       err,
		}
	}()

	var instances []api.Instance
	listTimer := time.NewTimer(time.Minute)
	defer listTimer.Stop()
	select {
	case res := <-result:
		if res.err != nil {
			return []commonParams.ProviderInstance{}, errors.Wrap(res.err, "fetching instances")
		}
		instances = res.instances
	case <-listTimer.C:
		return []commonParams.ProviderInstance{}, errors.Wrap(runnerErrors.ErrTimeout, "fetching instances from provider")
	case <-ctx.Done():
		return []commonParams.ProviderInstance{}, errors.Wrap(ctx.Err(), "fetching instances from provider")
	}

	ret := []commonParams.ProviderInstance{}

	for _, instance := range instances {
		if id, ok := instance.ExpandedConfig[controllerIDKeyName]; ok && id == l.controllerID {
			if poolID != "" {
				id := instance.ExpandedConfig[poolIDKey]
				if id != poolID {
					// Pool ID was specified. Filter out instances belonging to other pools.
					continue
				}
			}
			projected, err := l.projectGARMInstanceIdentity(ctx, &api.InstanceFull{Instance: instance}, incusInventoryInstanceToAPIInstance(&instance))
			if err != nil {
				return []commonParams.ProviderInstance{}, errors.Wrapf(err, "projecting instance %q identity", instance.Name)
			}
			ret = append(ret, projected)
		}
	}

	return ret, nil
}

// RemoveAllInstances will remove all instances created by this provider.
func (l *Incus) RemoveAllInstances(ctx context.Context) error {
	instances, err := l.ListInstances(ctx, "")
	if err != nil {
		return errors.Wrap(err, "fetching instance list")
	}

	for _, instance := range instances {
		// TODO: remove in parallel
		if err := l.DeleteInstance(ctx, instance.Name); err != nil {
			return errors.Wrapf(err, "removing instance %s", instance.Name)
		}
	}

	return nil
}

func (l *Incus) setState(ctx context.Context, instance, state string, force bool) error {
	reqState := api.InstanceStatePut{
		Action:  state,
		Timeout: -1,
		Force:   force,
	}

	cli, err := l.getCLI(ctx)
	if err != nil {
		return errors.Wrap(err, "fetching client")
	}
	managedInstance, err := l.getManagedInstance(ctx, instance)
	if err != nil {
		return errors.Wrapf(err, "authorizing instance state transition to %s", state)
	}
	if state == "start" {
		if err := l.validateManagedSecurity(managedInstance); err != nil {
			return errors.Wrap(err, "validating managed instance security policy")
		}
	}

	op, err := cli.UpdateInstanceState(instance, reqState, "")
	if err != nil {
		return errors.Wrapf(err, "setting state to %s", state)
	}
	ctxTimeout, cancel := context.WithTimeout(ctx, stateOperationTimeout)
	defer cancel()
	err = op.WaitContext(ctxTimeout)
	if err != nil {
		return errors.Wrapf(err, "waiting for instance to transition to state %s", state)
	}
	return nil
}

// Stop shuts down the instance.
func (l *Incus) Stop(ctx context.Context, instance string, force bool) error {
	return l.setState(ctx, instance, "stop", force)
}

// Start boots up an instance.
func (l *Incus) Start(ctx context.Context, instance string) error {
	return l.setState(ctx, instance, "start", false)
}

// GetVersion returns the version of the provider.
func (l *Incus) GetVersion(ctx context.Context) string {
	return Version
}
