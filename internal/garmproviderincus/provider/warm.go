package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	errors2 "errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/admission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incuspolicy"
	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/pkg/errors"
)

const (
	warmReadyGuestPath = "/run/gha-warm/ready"
	warmReadyEvidence  = "ready-unregistered-v1\n"
)

var newWarmSuffix = func() (string, error) {
	value := make([]byte, 6)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

type WarmPoolResult struct {
	Applied        bool             `json:"applied"`
	Pool           string           `json:"pool"`
	TargetReady    int              `json:"target_ready"`
	ReadyBefore    int              `json:"ready_before"`
	ReadyAfter     int              `json:"ready_after"`
	Preparing      int              `json:"preparing"`
	Claimed        int              `json:"claimed"`
	Deferred       bool             `json:"deferred"`
	DeferralReason admission.Reason `json:"deferral_reason,omitempty"`
	// RecycledStale names warm-ready instances whose release currency -- the
	// provider identity or the pinned worker image -- no longer matches the
	// current contract. They are deleted (a warm instance is disposable by
	// definition) and the deficit refills at the current identity, which
	// removes the manual delete every provider bump and image wave used to
	// require. Dry runs record the plan without deleting.
	RecycledStale     []string            `json:"recycled_stale,omitempty"`
	AdmissionDecision *admission.Decision `json:"admission_decision,omitempty"`
	Created           []string            `json:"created"`
	Consumed          []string            `json:"consumed_during_create"`
	Promoted          []string            `json:"promoted"`
	DeletedExcess     []string            `json:"deleted_excess"`
}

type WarmDrainResult struct {
	Applied  bool   `json:"applied"`
	Pool     string `json:"pool"`
	Instance string `json:"instance"`
	Deleted  bool   `json:"deleted"`
}

// DrainWarm authorizes and optionally deletes one exact, never-assigned warm
// instance through the normal diagnostic and journal-aware teardown path. The
// caller must close manager admission and prove zero claims before apply; the
// metadata checks below independently prevent a job VM from entering the path.
func (l *Incus) DrainWarm(ctx context.Context, flavor, instanceName string, apply bool) (WarmDrainResult, error) {
	result := WarmDrainResult{Applied: apply, Pool: flavor, Instance: instanceName}
	if l.admission == nil {
		return result, fmt.Errorf("admission controller is not configured")
	}
	if err := l.admission.AuthorizeWarmDrain(ctx, instanceName); err != nil {
		return result, errors.Wrap(err, "authorizing zero-claim warm drain")
	}
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return result, fmt.Errorf("pool policy %q does not exist", flavor)
	}
	imagePolicy, err := l.workerImagePolicy(flavor)
	if err != nil {
		return result, err
	}
	instance, err := l.getManagedInstance(ctx, instanceName)
	if err != nil {
		return result, errors.Wrap(err, "authorizing warm drain")
	}
	checks := []struct {
		key      string
		expected string
	}{
		{poolIDKey, warmPoolIDPrefix + flavor},
		{imageAliasKey, imagePolicy.Alias},
		{imageFingerprintKey, imagePolicy.Fingerprint},
		{flavorKey, flavor},
		{trustKey, pool.Trust},
		{scaleSetKey, pool.ScaleSetName},
		{repositoryKey, ""},
		{garmJobNameKey, ""},
		{osTypeKeyName, string(commonParams.Linux)},
		{osArchKeyNAme, string(commonParams.Amd64)},
	}
	// A drain deletes; an instance prepared under a capability the pool has
	// since moved away from is exactly what a drain must be able to remove,
	// so the capability keys are not held against it here.
	// Same reason as validateWarmReadyMetadata: the isolation contract belongs
	// to the pool's image, not to a virtual-machine assumption this function
	// used to hard-code. A container pool could not be drained at all.
	wantedType, securityChecks := managedSecurityContract(imagePolicy)
	for _, check := range securityChecks {
		if check.key == imageAliasKey {
			continue
		}
		checks = append(checks, struct {
			key      string
			expected string
		}{check.key, check.expected})
	}
	for _, check := range checks {
		if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
			return result, fmt.Errorf("warm drain instance %q has %s=%q, expected %q", instanceName, check.key, actual, check.expected)
		}
	}
	if lifecycle := instance.ExpandedConfig[lifecycleKey]; lifecycle != lifecycleWarmPreparing && lifecycle != lifecycleWarmUnregistered {
		return result, fmt.Errorf("warm drain instance %q has lifecycle %q", instanceName, lifecycle)
	}
	if instance.Type != wantedType || instance.Architecture != "x86_64" ||
		len(instance.Profiles) != 1 || instance.Profiles[0] != flavor {
		return result, fmt.Errorf("warm drain instance %q is not an exact-profile amd64 %s", instanceName, wantedType)
	}
	if !apply {
		return result, nil
	}
	if err := l.DeleteInstance(ctx, instanceName); err != nil {
		return result, errors.Wrap(err, "deleting drained warm instance")
	}
	result.Deleted = true
	return result, nil
}

// ReconcileWarm converges one pool toward its configured unregistered ready
// target. It never creates GitHub registration state and never reuses a VM
// after the provider has durably claimed it for a job.
func (l *Incus) ReconcileWarm(ctx context.Context, flavor string, apply bool) (WarmPoolResult, error) {
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return WarmPoolResult{}, fmt.Errorf("pool policy %q does not exist", flavor)
	}
	result := WarmPoolResult{Applied: apply, Pool: flavor, TargetReady: pool.Warm.TargetReady}
	cli, err := l.getCLI(ctx)
	if err != nil {
		return result, errors.Wrap(err, "fetching client")
	}
	if l.admission == nil {
		return result, fmt.Errorf("admission controller is not configured")
	}
	if err := l.admission.Reconcile(ctx, cli); err != nil {
		return result, errors.Wrap(err, "reconciling provider inventory")
	}
	instances, err := cli.GetInstancesFull(api.InstanceTypeAny)
	if err != nil {
		return result, errors.Wrap(err, "listing warm inventory")
	}
	preparing := make([]string, 0)
	ready := make([]string, 0)
	for index := range instances {
		instance := &instances[index]
		if instance.ExpandedConfig[controllerIDKeyName] != l.controllerID || instance.ExpandedConfig[flavorKey] != flavor {
			continue
		}
		switch instance.ExpandedConfig[lifecycleKey] {
		case lifecycleWarmPreparing:
			preparing = append(preparing, instance.Name)
		case lifecycleWarmUnregistered:
			if err := l.validateWarmReadyMetadata(instance, flavor); err != nil {
				if errors2.Is(err, errWarmOutdated) {
					result.RecycledStale = append(result.RecycledStale, instance.Name)
					if apply {
						if err := l.DeleteInstance(ctx, instance.Name); err != nil {
							return result, errors.Wrap(err, "recycling outdated warm instance")
						}
					}
					continue
				}
				return result, errors.Wrap(err, "validating warm-ready inventory")
			}
			ready = append(ready, instance.Name)
		case lifecycleEphemeralOneJob:
			result.Claimed++
		}
	}
	sort.Strings(preparing)
	sort.Strings(ready)
	result.ReadyBefore = len(ready)
	result.Preparing = len(preparing)
	if apply {
		for _, name := range preparing {
			promoted, err := l.promoteWarmReady(ctx, name, flavor)
			if err != nil {
				return result, err
			}
			if promoted {
				result.Promoted = append(result.Promoted, name)
				ready = append(ready, name)
			}
		}
		sort.Strings(ready)
	}

	// DeletedExcess is the plan as well as the record. It used to be appended
	// only under apply, so a dry run reduced the ready set to the target and
	// reported deleting nothing -- the one output an operator reads before
	// authorising a destructive converge showed an empty list precisely when it
	// was being consulted. The name is recorded either way; only the deletion
	// is conditional.
	for len(ready) > pool.Warm.TargetReady {
		name := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		if apply {
			if err := l.DeleteInstance(ctx, name); err != nil {
				return result, errors.Wrap(err, "deleting excess warm instance")
			}
		}
		result.DeletedExcess = append(result.DeletedExcess, name)
	}

	deficit := pool.Warm.TargetReady - len(ready) - (len(preparing) - len(result.Promoted))
	if deficit > 0 && apply {
		for range deficit {
			name, consumed, decision, err := l.createWarm(ctx, flavor)
			if err != nil {
				return result, err
			}
			if consumed {
				// A job claim or pressure preemption may retire a newly created
				// warm instance while this reconciler is still waiting for its
				// network. That is useful concurrent fleet work, not a failed
				// systemd reconcile. The next timer pass observes the resulting
				// inventory and refills any remaining deficit.
				result.Consumed = append(result.Consumed, name)
				continue
			}
			if decision != nil {
				result.Deferred = true
				result.DeferralReason = decision.Reason
				result.AdmissionDecision = decision
				break
			}
			result.Created = append(result.Created, name)
			ready = append(ready, name)
		}
	}
	if apply {
		if err := l.admission.Reconcile(ctx, cli); err != nil {
			return result, errors.Wrap(err, "recording reconciled warm inventory")
		}
	}
	result.ReadyAfter = len(ready)
	return result, nil
}

func (l *Incus) createWarm(ctx context.Context, flavor string) (name string, consumed bool, deferred *admission.Decision, err error) {
	suffix, err := newWarmSuffix()
	if err != nil {
		return "", false, nil, fmt.Errorf("generate warm instance identity: %w", err)
	}
	name = "warm-" + strings.TrimPrefix(flavor, "nddev-linux-") + "-" + suffix
	decision, err := l.admission.AdmitWarm(ctx, l.cli, flavor, name)
	if err != nil {
		return "", false, nil, errors.Wrap(err, "evaluating warm capacity")
	}
	if !decision.Admitted {
		return "", false, &decision, nil
	}
	created := false
	defer func() {
		if err == nil {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		var cleanupErr error
		if created {
			cleanupErr = l.DeleteInstance(cleanupContext, name)
		} else {
			cleanupErr = l.admission.Release(cleanupContext, name)
		}
		if cleanupErr != nil {
			err = errors.Wrapf(err, "cleaning failed warm instance %q: %v", name, cleanupErr)
		}
	}()
	args, err := l.getWarmCreateArgs(ctx, flavor, name)
	if err != nil {
		return "", false, nil, err
	}
	if err = l.launchInstance(ctx, args); err != nil {
		if isPlacementRefusal(err) {
			// The scriptlet is the per-member truth and fleet-level admission
			// is the ledger; when they disagree under load, the member that
			// had ledger room lacked live room. That is a capacity deferral,
			// not a failed reconcile.
			return "", false, &admission.Decision{Admitted: false, Reason: admission.ReasonPlacementRefused, Pool: flavor}, nil
		}
		return "", false, nil, errors.Wrap(err, "launching warm instance")
	}
	created = true
	if err = l.admission.MarkCreated(ctx, name); err != nil {
		return "", false, nil, errors.Wrap(err, "recording warm instance creation")
	}
	if _, err = l.waitInstanceHasIP(ctx, name); err != nil {
		if warmInstanceRetiredDuringCreate(err) {
			err = nil
			return name, true, nil, nil
		}
		return "", false, nil, errors.Wrap(err, "waiting for warm instance network")
	}
	if err = l.waitWarmReady(ctx, name, flavor, 2*time.Second); err != nil {
		if warmInstanceRetiredDuringCreate(err) {
			err = nil
			return name, true, nil, nil
		}
		return "", false, nil, err
	}
	return name, false, nil, nil
}

func warmInstanceRetiredDuringCreate(err error) bool {
	if isNotFoundError(err) {
		return true
	}
	// The network and readiness waits wrap their retries, and a claim or
	// preemption can delete the instance mid-wait: the not-found cause then
	// hides inside "attempt count exceeded: fetching instance: ...". A warm
	// instance that vanished during create was consumed by useful concurrent
	// work, whatever the wrapper says.
	return err != nil && strings.Contains(err.Error(), "Instance not found")
}

// isPlacementRefusal recognizes the scriptlet's per-member capacity refusal
// inside a create error.
func isPlacementRefusal(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "Failed instance placement scriptlet") ||
		strings.Contains(message, "no fleet member has room")
}

func (l *Incus) waitWarmReady(ctx context.Context, name, flavor string, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		return fmt.Errorf("warm readiness poll interval must be positive")
	}
	readinessContext, cancel := context.WithTimeout(ctx, stateOperationTimeout)
	defer cancel()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		promoted, err := l.promoteWarmReady(readinessContext, name, flavor)
		if err != nil {
			return err
		}
		if promoted {
			return nil
		}
		select {
		case <-readinessContext.Done():
			return errors.Wrapf(runnerErrors.ErrTimeout, "warm instance %q did not publish readiness evidence", name)
		case <-ticker.C:
		}
	}
}

func (l *Incus) getWarmCreateArgs(ctx context.Context, flavor, name string) (api.InstancesPost, error) {
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return api.InstancesPost{}, fmt.Errorf("pool policy %q does not exist", flavor)
	}
	imagePolicy, err := l.workerImagePolicy(flavor)
	if err != nil {
		return api.InstancesPost{}, err
	}
	profiles, err := l.getProfiles(ctx, flavor)
	if err != nil {
		return api.InstancesPost{}, err
	}
	image, err := l.imageManager.getLocalImageByAlias(imagePolicy.Alias, imagePolicy.InstanceType, "x86_64", l.cli)
	if err != nil {
		return api.InstancesPost{}, errors.Wrap(err, "resolving warm image")
	}
	if err := validateResolvedWorkerImage(image, imagePolicy); err != nil {
		return api.InstancesPost{}, err
	}
	// No user.user-data on purpose. The image's cloud-init unit treats an
	// absent key as "nothing to apply" and exits; a cloud-config that installs
	// nothing made it run every stage, find no runner service, fail, and
	// restart every five seconds for the life of the instance -- measured on
	// 2026-09-01 at one core per warm worker, with the restart counter past
	// twenty on a two-minute-old instance.
	configMap := map[string]string{
		osTypeKeyName:       string(commonParams.Linux),
		osArchKeyNAme:       string(commonParams.Amd64),
		controllerIDKeyName: l.controllerID,
		poolIDKey:           warmPoolIDPrefix + flavor,
		imageAliasKey:       imagePolicy.Alias,
		imageFingerprintKey: imagePolicy.Fingerprint,
		providerVersionKey:  Version,
		providerCommitKey:   Commit,
		flavorKey:           flavor,
		lifecycleKey:        lifecycleWarmPreparing,
		warmReadyKey:        "false",
		trustKey:            pool.Trust,
		scaleSetKey:         pool.ScaleSetName,
		repositoryKey:       "",
		garmJobNameKey:      "",
		networkPolicyKey:    pool.Capabilities.NetworkPolicy,
		cacheWriteScopeKey:  pool.Capabilities.CacheWriteScope,
		"boot.autostart":    "true",
	}
	// The same split the one-job path makes in getCreateInstanceArgs. Warm was
	// written when every pool was a virtual machine; every pool is a container
	// now, and a VM-shaped create is refused by the backend before it reaches
	// any of the checks below.
	instanceType := imagePolicy.InstanceType
	if instanceType == config.IncusImageVirtualMachine {
		configMap["security.secureboot"] = l.secureBootEnabled()
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
	description := "Unregistered warm GitHub runner"
	return api.InstancesPost{
		InstancePut: api.InstancePut{
			Architecture: "x86_64",
			Profiles:     profiles,
			Description:  description,
			Config:       configMap,
		},
		Source: api.InstanceSource{Type: "image", Fingerprint: image.Fingerprint},
		Name:   name,
		Type:   api.InstanceType(instanceType),
	}, nil
}

func (l *Incus) promoteWarmReady(ctx context.Context, name, flavor string) (bool, error) {
	instance, etag, err := l.cli.GetInstanceFull(name)
	if err != nil && !isNotFoundError(err) {
		// A booting instance can answer one poll with a transient parse-level
		// error from the state endpoint ("Invalid PID"). One bounded retry
		// separates that blink from a real failure.
		time.Sleep(2 * time.Second)
		instance, etag, err = l.cli.GetInstanceFull(name)
	}
	if err != nil {
		return false, errors.Wrap(err, "fetching preparing warm instance")
	}
	if instance.ExpandedConfig[lifecycleKey] == lifecycleWarmUnregistered {
		return true, l.validateWarmReadyMetadata(instance, flavor)
	}
	if instance.ExpandedConfig[lifecycleKey] != lifecycleWarmPreparing {
		return false, fmt.Errorf("instance %q is not warm-preparing", name)
	}
	content, response, err := l.cli.GetInstanceFile(name, warmReadyGuestPath)
	if err != nil {
		if isNotFoundError(err) {
			return false, nil
		}
		return false, errors.Wrap(err, "reading warm readiness evidence")
	}
	defer content.Close()
	data, err := io.ReadAll(io.LimitReader(content, 129))
	if err != nil {
		return false, errors.Wrap(err, "reading warm readiness evidence")
	}
	if len(data) > 128 || string(data) != warmReadyEvidence || response == nil || response.UID != 0 || response.GID != 0 || response.Mode != 0o644 || response.Type != "file" {
		return false, fmt.Errorf("instance %q published invalid warm readiness evidence", name)
	}
	writable := instance.Writable()
	writable.Config = make(map[string]string, len(instance.Config)+2)
	for key, value := range instance.Config {
		writable.Config[key] = value
	}
	writable.Config[lifecycleKey] = lifecycleWarmUnregistered
	writable.Config[warmReadyKey] = "true"
	op, err := l.cli.UpdateInstance(name, writable, etag)
	if err != nil {
		return false, errors.Wrap(err, "promoting warm readiness metadata")
	}
	updateContext, cancel := context.WithTimeout(ctx, stateOperationTimeout)
	err = op.WaitContext(updateContext)
	cancel()
	if err != nil {
		return false, errors.Wrap(err, "waiting for warm readiness promotion")
	}
	return true, nil
}

// errWarmOutdated marks a warm instance whose release currency has moved on;
// the reconciler recycles it instead of failing.
var errWarmOutdated = errors2.New("warm instance is outdated")

func (l *Incus) validateWarmReadyMetadata(instance *api.InstanceFull, flavor string) error {
	if instance == nil {
		return fmt.Errorf("warm instance is not present")
	}
	pool, exists := l.platform.Pool(flavor)
	if !exists {
		return fmt.Errorf("pool policy %q does not exist", flavor)
	}
	imagePolicy, err := l.workerImagePolicy(flavor)
	if err != nil {
		return err
	}
	// Currency keys move with every release, image wave and pool capability
	// change; a mismatch there is an OUTDATED disposable, not a boundary
	// violation, and the caller recycles it. Everything below stays a hard
	// contract. The two capabilities are currency because a pool's network
	// policy or cache write scope can move while its warm instances live;
	// held as a hard contract they stalled every warm reconcile on
	// 2026-09-02 until the records were corrected by hand.
	for _, currency := range []struct {
		key      string
		expected string
	}{
		{providerVersionKey, Version},
		{providerCommitKey, Commit},
		{imageAliasKey, imagePolicy.Alias},
		{imageFingerprintKey, imagePolicy.Fingerprint},
		{networkPolicyKey, pool.Capabilities.NetworkPolicy},
		{cacheWriteScopeKey, pool.Capabilities.CacheWriteScope},
	} {
		if actual := instance.ExpandedConfig[currency.key]; actual != currency.expected {
			return fmt.Errorf("%w: warm instance %q has %s=%q, current is %q",
				errWarmOutdated, instance.Name, currency.key, actual, currency.expected)
		}
	}
	checks := []struct {
		key      string
		expected string
	}{
		{controllerIDKeyName, l.controllerID},
		{poolIDKey, warmPoolIDPrefix + flavor},
		{flavorKey, flavor},
		{lifecycleKey, lifecycleWarmUnregistered},
		{warmReadyKey, "true"},
		{trustKey, pool.Trust},
		{scaleSetKey, pool.ScaleSetName},
		{repositoryKey, ""},
		{garmJobNameKey, ""},
		{osTypeKeyName, string(commonParams.Linux)},
		{osArchKeyNAme, string(commonParams.Amd64)},
		{"boot.autostart", "true"},
	}
	// The isolation contract is whatever the pool's own image declares, read
	// through the same helper the one-job path uses, rather than the
	// virtual-machine set this function asserted unconditionally.
	wantedType, securityChecks := managedSecurityContract(imagePolicy)
	for _, check := range securityChecks {
		if check.key == imageAliasKey {
			continue
		}
		checks = append(checks, struct {
			key      string
			expected string
		}{check.key, check.expected})
	}
	for _, check := range checks {
		if actual := instance.ExpandedConfig[check.key]; actual != check.expected {
			return fmt.Errorf("warm instance %q has %s=%q, expected %q", instance.Name, check.key, actual, check.expected)
		}
	}
	if instance.Type != wantedType {
		return fmt.Errorf("warm instance %q is a %s, expected %s", instance.Name, instance.Type, wantedType)
	}
	if instance.Architecture != "x86_64" || len(instance.Profiles) != 1 || instance.Profiles[0] != flavor ||
		instance.State == nil || instance.State.Status != "Running" {
		return fmt.Errorf("warm instance %q is not a running exact-profile amd64 worker", instance.Name)
	}
	return nil
}
