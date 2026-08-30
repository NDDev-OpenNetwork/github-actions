package provider

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/NDDev-OpenNetwork/github-actions/internal/provideradmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/NDDev-OpenNetwork/github-actions/internal/queueintent"
	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
	"github.com/cloudbase/garm-provider-common/params"
	"github.com/lxc/incus/v7/shared/api"
)

var runtimeHostname = os.Hostname

type admissionController interface {
	Admit(context.Context, InstanceServerInterface, params.BootstrapInstance) (provideradmission.AdmissionResult, error)
	Reconcile(context.Context, InstanceServerInterface) error
	MarkCreated(context.Context, string) error
	MarkDeleting(context.Context, string) error
	Release(context.Context, string) error
	ClaimWarm(context.Context, InstanceServerInterface, params.BootstrapInstance) (provideradmission.WarmClaimResult, error)
	MarkWarmInjected(context.Context, string, string) error
	Resolve(context.Context, string) (string, error)
	AdmitWarm(context.Context, InstanceServerInterface, string, string) (admission.Decision, error)
	AuthorizeWarmDrain(context.Context, string) error
}

type repositoryResolver interface {
	ResolveRepository(context.Context, params.BootstrapInstance) (string, error)
}

func (n *nddevAdmission) ResolveRepository(ctx context.Context, bootstrap params.BootstrapInstance) (string, error) {
	owner := bootstrapOwner(bootstrap)
	pool, exists := n.platform.Pool(bootstrap.Flavor)
	if !exists {
		return "", fmt.Errorf("pool policy %q does not exist", bootstrap.Flavor)
	}
	return n.queueIntents.RepositoryForScaleSet(ctx, owner, pool.ScaleSetName)
}

func (n *nddevAdmission) AuthorizeWarmDrain(ctx context.Context, instanceName string) error {
	return n.controller.AuthorizeWarmDrain(ctx, instanceName)
}

type nddevAdmission struct {
	cfg                  *providerconfig.Incus
	platform             platformconfig.Config
	controllerID         string
	workerImages         map[string]providerconfig.WorkerImage
	pressurePolicy       pressuregate.Policy
	controller           provideradmission.Controller
	queueIntents         queueintent.Reader
	diagnosticsDirectory string
	diagnosticsMaxBytes  int64
}

func newNDDevAdmission(cfg *providerconfig.Incus, controllerID string) (*nddevAdmission, error) {
	platform, err := platformconfig.Load(cfg.PlatformConfigFile)
	if err != nil {
		return nil, fmt.Errorf("load platform policy: %w", err)
	}
	hostname, err := runtimeHostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return nil, fmt.Errorf("resolve runtime hostname: %w", err)
	}
	if !platformHostMatches(platform.Platform.Host, hostname) {
		return nil, fmt.Errorf("platform policy targets host %q but provider runs on %q", platform.Platform.Host, hostname)
	}
	// Compare the policy against what this binary actually is, not against a
	// literal. A literal cannot notice that runner-1 and runner-2 were running
	// binaries built from ad8efaa and cae2d18 while both configs and both
	// binaries said v0.1.5-nddev.30 (#263).
	//
	// provider_interface is not checked here: internal/config rejects any value
	// but v0.1.0 during Load above, so a second check could never fire.
	if !providerrelease.IsRelease(Version) {
		return nil, fmt.Errorf("provider reports %q, which is not a stamped release; a development build must not admit a platform policy", Version)
	}
	if platform.ControlPlane.ProviderVersion != Version {
		return nil, fmt.Errorf("platform policy pins provider %q but this binary is %q", platform.ControlPlane.ProviderVersion, Version)
	}
	if err := validateWorkerImageMappings(platform, cfg.WorkerImages); err != nil {
		return nil, err
	}
	return &nddevAdmission{
		cfg:            cfg,
		platform:       platform,
		controllerID:   controllerID,
		workerImages:   cfg.WorkerImages,
		pressurePolicy: platform.Pressure,
		controller: provideradmission.Controller{
			Store: providerjournal.Store{
				Path:     cfg.JournalFile,
				LockPath: cfg.JournalLockFile,
			},
			ControllerID: controllerID,
			Policy: admission.ReservePolicy{
				MinimumCPUUnits:        platform.HostReserve.MinimumCPUUnits,
				MaximumFleetCPUPercent: platform.HostReserve.MaximumFleetCPUPercent,
				CPUAllowanceOvercommit: platform.HostReserve.CPUAllowanceOvercommit,
				MinimumMemoryMiB:       platform.HostReserve.MinimumMemoryMiB,
				MinimumPercent:         platform.HostReserve.MinimumPercent,
				MinimumFreeDiskPercent: platform.HostReserve.MinimumFreeDiskPercent,
				RequirePressure:        platform.Pressure.Required,
				MaxCPUSomeAvg10:        platform.Pressure.CPUSomeClose,
				MaxMemoryFullAvg10:     platform.Pressure.MemoryFullClose,
				MaxIOFullAvg10:         platform.Pressure.IOFullClose,
				MaxRecentOOMKills:      int(platform.Pressure.MaximumRecentOOMKills),
			},
			LeaseTTL: time.Duration(cfg.AdmissionLeaseSeconds) * time.Second,
		},
		queueIntents:         queueintent.Reader{Path: cfg.QueueIntentFile},
		diagnosticsDirectory: cfg.DiagnosticsDirectory,
		diagnosticsMaxBytes:  cfg.DiagnosticsMaxTotalBytes,
	}, nil
}

func platformHostMatches(declared, observed string) bool {
	return declared != "" && declared == observed
}

func validateWorkerImageMappings(platform platformconfig.Config, images map[string]providerconfig.WorkerImage) error {
	for flavor, image := range images {
		pool, exists := platform.Pool(flavor)
		if !exists {
			return fmt.Errorf("worker image mapping targets unknown pool %q", flavor)
		}
		expectedVariant := "standard"
		if pool.Capabilities.Docker {
			expectedVariant = "integration"
		}
		if image.Variant != expectedVariant {
			return fmt.Errorf(
				"worker image mapping for pool %q has variant %q, expected %q",
				flavor,
				image.Variant,
				expectedVariant,
			)
		}
	}
	return nil
}

func (n *nddevAdmission) Reconcile(ctx context.Context, cli InstanceServerInterface) error {
	observed, err := n.observedAllocations(ctx, cli)
	if err != nil {
		return err
	}
	return n.controller.Reconcile(ctx, observed)
}

func (n *nddevAdmission) Admit(
	ctx context.Context,
	cli InstanceServerInterface,
	bootstrap params.BootstrapInstance,
) (provideradmission.AdmissionResult, error) {
	if blocked, err := n.diagnosticsBlocked(); err != nil {
		return provideradmission.AdmissionResult{}, err
	} else if blocked {
		return provideradmission.AdmissionResult{Decision: admission.Decision{
			Admitted: false, Reason: admission.ReasonDiagnosticWAL, Pool: bootstrap.Flavor,
		}}, nil
	}
	// The flavor is this host's local pool name, which must be unique here.
	// The journal records GitHub's scale set name, which is unique only per
	// tenant. They are equal for every single-tenant pool, so authorizing on
	// the flavor worked until a pool needed a local name of its own; then it
	// would have refused every create as unauthorized. Resolve the policy
	// first and authorize on the forge identity it declares.
	pool, exists := n.platform.Pool(bootstrap.Flavor)
	if !exists {
		return provideradmission.AdmissionResult{}, fmt.Errorf("pool policy %q does not exist", bootstrap.Flavor)
	}
	queueAuthorized, err := n.queueIntents.ActiveForScaleSet(ctx, bootstrapOwner(bootstrap), pool.ScaleSetName)
	if err != nil {
		return provideradmission.AdmissionResult{}, fmt.Errorf("read pre-AcquireJobs queue intent: %w", err)
	}
	if !queueAuthorized {
		return provideradmission.AdmissionResult{}, fmt.Errorf(
			"pool %q has no active pre-AcquireJobs queue intent for scale set %q of account %q",
			bootstrap.Flavor, pool.ScaleSetName, bootstrapOwner(bootstrap))
	}
	if pool.Resources.VCPU <= 0 || pool.Resources.MemoryMiB <= 0 || pool.MaxRunning <= 0 {
		return provideradmission.AdmissionResult{}, fmt.Errorf("pool policy %q has invalid resources", bootstrap.Flavor)
	}
	imagePolicy, exists := n.workerImages[bootstrap.Flavor]
	if !exists {
		return provideradmission.AdmissionResult{}, fmt.Errorf("pool %q has no pinned worker image", bootstrap.Flavor)
	}
	observed, err := n.observedAllocations(ctx, cli)
	if err != nil {
		return provideradmission.AdmissionResult{}, err
	}
	host, err := fleetHostState(ctx, cli, n.platform, pool, n.pressurePolicy)
	if err != nil {
		return provideradmission.AdmissionResult{}, err
	}
	return n.controller.AdmitPreemptible(ctx, host, observed, provideradmission.Request{
		Allocation: provideradmission.Allocation{
			InstanceName:      bootstrap.Name,
			ControllerID:      n.controllerID,
			PoolID:            bootstrap.PoolID,
			PoolName:          bootstrap.Flavor,
			VCPU:              pool.EffectiveReservation().CPUUnits,
			CPUAllowanceUnits: pool.Resources.VCPU,
			MemoryMiB:         pool.EffectiveReservation().MemoryMiB,
			ImageFingerprint:  imagePolicy.Fingerprint,
		},
		QueueIntentAuthorized: true,
	})
}

func (n *nddevAdmission) AdmitWarm(ctx context.Context, cli InstanceServerInterface, flavor, instanceName string) (admission.Decision, error) {
	if blocked, err := n.diagnosticsBlocked(); err != nil {
		return admission.Decision{}, err
	} else if blocked {
		return admission.Decision{Admitted: false, Reason: admission.ReasonDiagnosticWAL, Pool: flavor}, nil
	}
	pool, exists := n.platform.Pool(flavor)
	if !exists {
		return admission.Decision{}, fmt.Errorf("pool policy %q does not exist", flavor)
	}
	imagePolicy, exists := n.workerImages[flavor]
	if !exists {
		return admission.Decision{}, fmt.Errorf("pool %q has no pinned worker image", flavor)
	}
	observed, err := n.observedAllocations(ctx, cli)
	if err != nil {
		return admission.Decision{}, err
	}
	host, err := fleetHostState(ctx, cli, n.platform, pool, n.pressurePolicy)
	if err != nil {
		return admission.Decision{}, err
	}
	return n.controller.Admit(ctx, host, observed, provideradmission.Request{
		Allocation: provideradmission.Allocation{
			InstanceName:      instanceName,
			ControllerID:      n.controllerID,
			PoolID:            warmPoolIDPrefix + flavor,
			PoolName:          flavor,
			VCPU:              pool.EffectiveReservation().CPUUnits,
			CPUAllowanceUnits: pool.Resources.VCPU,
			MemoryMiB:         pool.EffectiveReservation().MemoryMiB,
			ImageFingerprint:  imagePolicy.Fingerprint,
		},
	})
}

func (n *nddevAdmission) diagnosticsBlocked() (bool, error) {
	stats, err := workerdiagnostics.Inspect(n.diagnosticsDirectory, time.Now().UTC())
	if err != nil {
		return false, fmt.Errorf("inspect diagnostic durable WAL: %w", err)
	}
	return workerdiagnostics.AtDurableWALHighWatermark(stats, n.diagnosticsMaxBytes), nil
}

func (n *nddevAdmission) observedAllocations(ctx context.Context, cli InstanceServerInterface) ([]provideradmission.Allocation, error) {
	instances, err := cli.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("observe Incus allocations: %w", err)
	}
	allocations := make([]provideradmission.Allocation, 0, len(instances))
	for _, instance := range instances {
		flavor := instance.ExpandedConfig[flavorKey]
		if flavor == "" {
			if strings.HasPrefix(instance.Name, "gha-image-builder-") || strings.HasPrefix(instance.Name, "gha-image-smoke-") {
				if len(instance.Profiles) != 1 {
					return nil, fmt.Errorf("image maintenance instance %q has ambiguous profiles", instance.Name)
				}
				maintenancePool, exists := n.platform.Pool(instance.Profiles[0])
				if !exists {
					return nil, fmt.Errorf("image maintenance instance %q uses unknown profile %q", instance.Name, instance.Profiles[0])
				}
				imagePolicy, exists := n.workerImages[maintenancePool.Name]
				if !exists {
					return nil, fmt.Errorf("image maintenance profile %q has no worker image policy", maintenancePool.Name)
				}
				allocations = append(allocations, provideradmission.Allocation{
					InstanceName: instance.Name, ControllerID: n.controllerID,
					PoolID: "image-maintenance/" + maintenancePool.Name, PoolName: maintenancePool.Name,
					VCPU: maintenancePool.EffectiveReservation().CPUUnits, CPUAllowanceUnits: maintenancePool.Resources.VCPU,
					MemoryMiB:        maintenancePool.EffectiveReservation().MemoryMiB,
					ImageFingerprint: imagePolicy.Fingerprint, State: providerjournal.StateCreated,
				})
				continue
			}
			if instance.Status == "Stopped" {
				// A canceled create can stop the Incus instance before its
				// asynchronous delete removes it. It consumes no CPU or memory
				// and remains visible to orphan/missing reconciliation.
				continue
			}
			// Incus may briefly expose a pending/deleting instance without its
			// expanded config. The durable lease is authoritative during that
			// bounded transition; an unjournaled incomplete instance remains a
			// hard error rather than disappearing from capacity accounting.
			state, err := n.controller.Store.ReadOnly(ctx)
			if err != nil {
				return nil, fmt.Errorf("read provider journal for incomplete instance %q: %w", instance.Name, err)
			}
			lease, owned := state.Leases[instance.Name]
			if !owned || (lease.State != providerjournal.StateAdmitted && lease.State != providerjournal.StateCreated && lease.State != providerjournal.StateDeleting) {
				return nil, fmt.Errorf(
					"incomplete instance metadata: instance %q has no flavor and no active provider lease",
					instance.Name,
				)
			}
			allocations = append(allocations, provideradmission.Allocation{
				InstanceName: lease.InstanceName, ControllerID: lease.ControllerID,
				PoolID: lease.PoolID, PoolName: lease.PoolName, VCPU: lease.VCPU, CPUAllowanceUnits: lease.CPUAllowanceUnits,
				MemoryMiB: lease.MemoryMiB, ImageFingerprint: lease.ImageFingerprint,
				State: lease.State, JobName: instance.Name,
			})
			continue
		}
		pool, exists := n.platform.Pool(flavor)
		if !exists {
			return nil, fmt.Errorf("instance %q has unknown flavor %q", instance.Name, flavor)
		}
		imagePolicy, exists := n.workerImages[flavor]
		if !exists {
			return nil, fmt.Errorf("instance %q flavor %q has no pinned worker image", instance.Name, flavor)
		}
		wantedType, securityChecks := managedSecurityContract(imagePolicy)
		if instance.Type != wantedType {
			return nil, fmt.Errorf("instance %q has type %q, expected %q", instance.Name, instance.Type, wantedType)
		}
		lifecycle := instance.ExpandedConfig[lifecycleKey]
		if lifecycle != lifecycleEphemeralOneJob && lifecycle != lifecycleWarmPreparing && lifecycle != lifecycleWarmUnregistered {
			return nil, fmt.Errorf("instance %q has unsupported lifecycle %q", instance.Name, lifecycle)
		}
		actualFingerprint := instance.ExpandedConfig[imageFingerprintKey]
		fingerprintAllowed := actualFingerprint == imagePolicy.Fingerprint
		if lifecycle == lifecycleEphemeralOneJob {
			fingerprintAllowed = imagePolicy.AllowsExistingFingerprint(actualFingerprint)
		}
		if !fingerprintAllowed {
			return nil, fmt.Errorf(
				"instance %q has %s=%q, expected exact current fingerprint for lifecycle %q or declared N-1 for an executing one-job worker",
				instance.Name, imageFingerprintKey, actualFingerprint, lifecycle,
			)
		}
		if lifecycle == lifecycleEphemeralOneJob && !n.cfg.AllowsProviderIdentity(
			instance.ExpandedConfig[providerVersionKey], instance.ExpandedConfig[providerCommitKey], Version, Commit,
		) {
			return nil, fmt.Errorf(
				"instance %q has unsupported provider identity %q@%q, current is %q@%q",
				instance.Name, instance.ExpandedConfig[providerVersionKey], instance.ExpandedConfig[providerCommitKey], Version, Commit,
			)
		}
		checks := []struct {
			key      string
			expected string
		}{
			{controllerIDKeyName, n.controllerID},
			{imageAliasKey, imagePolicy.Alias},
			{osTypeKeyName, string(params.Linux)},
			{osArchKeyNAme, string(params.Amd64)},
		}
		checks = append(checks, securityChecks[1:]...)
		for _, check := range checks {
			if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
				return nil, fmt.Errorf(
					"instance %q has %s=%q, expected %q",
					instance.Name,
					check.key,
					actual,
					check.expected,
				)
			}
		}
		poolChecks := []struct {
			key      string
			expected string
		}{
			{trustKey, pool.Trust},
			{scaleSetKey, pool.ScaleSetName},
			{networkPolicyKey, pool.Capabilities.NetworkPolicy},
			{cacheWriteScopeKey, pool.Capabilities.CacheWriteScope},
		}
		for _, check := range poolChecks {
			if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
				return nil, fmt.Errorf(
					"instance %q has %s=%q, expected %q",
					instance.Name,
					check.key,
					actual,
					check.expected,
				)
			}
		}
		state := providerjournal.StateCreated
		if lifecycle == lifecycleWarmUnregistered {
			if instance.ExpandedConfig[warmReadyKey] != "true" {
				return nil, fmt.Errorf("warm instance %q is unregistered but not marked ready", instance.Name)
			}
			if instance.ExpandedConfig[poolIDKey] != warmPoolIDPrefix+flavor || instance.ExpandedConfig[repositoryKey] != "" || instance.ExpandedConfig[garmJobNameKey] != "" {
				return nil, fmt.Errorf("warm instance %q contains job ownership metadata", instance.Name)
			}
			state = providerjournal.StateWarmReady
		}
		if lifecycle == lifecycleWarmPreparing {
			if instance.ExpandedConfig[poolIDKey] != warmPoolIDPrefix+flavor || instance.ExpandedConfig[repositoryKey] != "" || instance.ExpandedConfig[garmJobNameKey] != "" || instance.ExpandedConfig[warmReadyKey] == "true" {
				return nil, fmt.Errorf("preparing warm instance %q has invalid readiness or ownership metadata", instance.Name)
			}
		}
		allocations = append(allocations, provideradmission.Allocation{
			InstanceName:      instance.Name,
			ControllerID:      instance.ExpandedConfig[controllerIDKeyName],
			PoolID:            instance.ExpandedConfig[poolIDKey],
			PoolName:          flavor,
			VCPU:              pool.EffectiveReservation().CPUUnits,
			CPUAllowanceUnits: pool.Resources.VCPU,
			MemoryMiB:         pool.EffectiveReservation().MemoryMiB,
			ImageFingerprint:  instance.ExpandedConfig[imageFingerprintKey],
			State:             state,
			JobName:           instance.ExpandedConfig[garmJobNameKey],
		})
	}
	return allocations, nil
}

func (n *nddevAdmission) ClaimWarm(ctx context.Context, cli InstanceServerInterface, bootstrap params.BootstrapInstance) (provideradmission.WarmClaimResult, error) {
	pool, exists := n.platform.Pool(bootstrap.Flavor)
	if !exists {
		return provideradmission.WarmClaimResult{}, fmt.Errorf("pool policy %q does not exist", bootstrap.Flavor)
	}
	queueAuthorized, err := n.queueIntents.ActiveForScaleSet(ctx, bootstrapOwner(bootstrap), pool.ScaleSetName)
	if err != nil {
		return provideradmission.WarmClaimResult{}, fmt.Errorf("read pre-AcquireJobs queue intent: %w", err)
	}
	if !queueAuthorized {
		return provideradmission.WarmClaimResult{}, fmt.Errorf(
			"pool %q has no active pre-AcquireJobs queue intent for scale set %q of account %q",
			bootstrap.Flavor, pool.ScaleSetName, bootstrapOwner(bootstrap))
	}
	observed, err := n.observedAllocations(ctx, cli)
	if err != nil {
		return provideradmission.WarmClaimResult{}, err
	}
	imagePolicy, exists := n.workerImages[bootstrap.Flavor]
	if !exists {
		return provideradmission.WarmClaimResult{}, fmt.Errorf("pool %q has no pinned worker image", bootstrap.Flavor)
	}
	return n.controller.ClaimWarm(ctx, observed, provideradmission.WarmClaimRequest{
		JobName:          bootstrap.Name,
		ControllerID:     n.controllerID,
		PoolID:           bootstrap.PoolID,
		PoolName:         bootstrap.Flavor,
		ImageFingerprint: imagePolicy.Fingerprint,
	})
}

func (n *nddevAdmission) MarkWarmInjected(ctx context.Context, jobName, instanceName string) error {
	return n.controller.MarkWarmInjected(ctx, jobName, instanceName)
}

func (n *nddevAdmission) Resolve(ctx context.Context, identifier string) (string, error) {
	return n.controller.Resolve(ctx, identifier)
}

func (n *nddevAdmission) MarkCreated(ctx context.Context, instanceName string) error {
	return n.controller.MarkCreated(ctx, instanceName)
}

func (n *nddevAdmission) MarkDeleting(ctx context.Context, instanceName string) error {
	return n.controller.MarkDeleting(ctx, instanceName)
}

func (n *nddevAdmission) Release(ctx context.Context, instanceName string) error {
	return n.controller.Release(ctx, instanceName)
}

// bootstrapOwner is the account whose job this create is for, taken from the
// repository GARM asked for rather than from the pool that will host it. Two
// tenants share a pool's flavor name -- the flavor is the machine shape, and
// the host declares one of each -- so the pool cannot say whose job this is.
// The repository can, and validateBootstrapParams has already refused any
// repository outside the tenant registry, so this reads an approved value.
func bootstrapOwner(bootstrap params.BootstrapInstance) string {
	trimmed := strings.TrimPrefix(bootstrap.RepoURL, "https://github.com/")
	owner, _, _ := strings.Cut(trimmed, "/")
	if owner == "" {
		// Unreachable behind the boundary check, and fail-closed if reached:
		// an empty account matches no intent.
		return ""
	}
	return owner
}
