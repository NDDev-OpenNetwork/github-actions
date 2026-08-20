package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/provideradmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

func TestDiagnosticWALHighWatermarkBlocksNewProviderAdmission(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o700))
	name := "runner-diagnostics-v1-runner-20260820T040000.000000000Z-000000000001.tar.gz"
	require.NoError(t, os.WriteFile(filepath.Join(directory, name), make([]byte, 80), 0o600))
	control := &nddevAdmission{diagnosticsDirectory: directory, diagnosticsMaxBytes: 100}
	blocked, err := control.diagnosticsBlocked()
	require.NoError(t, err)
	require.True(t, blocked)
}

func testNDDevAdmission() *nddevAdmission {
	return &nddevAdmission{
		cfg:          &providerconfig.Incus{},
		controllerID: testControllerID,
		workerImages: map[string]providerconfig.WorkerImage{
			"nddev-linux-standard": {
				Alias:       testImageAlias,
				Fingerprint: testImageDigest,
				Variant:     "standard",
			},
		},
		platform: platformconfig.Config{Pools: []platformconfig.Pool{{
			Name:         "nddev-linux-standard",
			ScaleSetName: "nddev-linux-standard",
			Trust:        "trusted",
			Resources: platformconfig.Resources{
				VCPU:      4,
				MemoryMiB: 10240,
			},
			Capabilities: platformconfig.Capabilities{
				NetworkPolicy:   "public-internet",
				CacheWriteScope: "trusted",
			},
		}}},
	}
}

func TestWorkerImageMappingsMatchPlatformPoolsAndCapabilities(t *testing.T) {
	admission := testNDDevAdmission()
	require.NoError(t, validateWorkerImageMappings(admission.platform, admission.workerImages))

	unknown := map[string]providerconfig.WorkerImage{
		"unknown": {Alias: testImageAlias, Fingerprint: testImageDigest, Variant: "standard"},
	}
	require.ErrorContains(t, validateWorkerImageMappings(admission.platform, unknown), "targets unknown pool")

	wrongVariant := map[string]providerconfig.WorkerImage{
		"nddev-linux-standard": {Alias: testImageAlias, Fingerprint: testImageDigest, Variant: "integration"},
	}
	require.ErrorContains(t, validateWorkerImageMappings(admission.platform, wrongVariant), "expected \"standard\"")
}

func TestObservedAllocationsRequireCompleteSecurityPolicy(t *testing.T) {
	cli := new(MockIncusServer)
	instance := *ownedInstance("runner")
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{instance}, nil).Once()

	allocations, err := testNDDevAdmission().observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Equal(t, []provideradmission.Allocation{
		{
			InstanceName:     "runner",
			ControllerID:     testControllerID,
			PoolID:           "pool-test",
			PoolName:         "nddev-linux-standard",
			VCPU:             4,
			MemoryMiB:        10240,
			ImageFingerprint: testImageDigest,
			State:            providerjournal.StateCreated,
			JobName:          "runner-test-instance",
		},
	}, allocations)
}

func TestObservedAllocationsAcceptNMinusOneOnlyForExecutingWorker(t *testing.T) {
	t.Parallel()
	previous := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	admission := testNDDevAdmission()
	image := admission.workerImages["nddev-linux-standard"]
	image.PreviousFingerprint = previous
	admission.workerImages["nddev-linux-standard"] = image

	instance := *ownedInstance("runner-previous")
	instance.ExpandedConfig[imageFingerprintKey] = previous
	cli := new(MockIncusServer)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{instance}, nil).Once()
	allocations, err := admission.observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Equal(t, previous, allocations[0].ImageFingerprint)

	instance.ExpandedConfig[lifecycleKey] = lifecycleWarmUnregistered
	instance.ExpandedConfig[warmReadyKey] = "true"
	instance.ExpandedConfig[poolIDKey] = warmPoolIDPrefix + "nddev-linux-standard"
	instance.ExpandedConfig[repositoryKey] = ""
	instance.ExpandedConfig[garmJobNameKey] = ""
	cli = new(MockIncusServer)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{instance}, nil).Once()
	_, err = admission.observedAllocations(context.Background(), cli)
	require.ErrorContains(t, err, "exact current fingerprint")
}

func TestObservedAllocationsUseDurableLeaseForIncompleteIncusTransition(t *testing.T) {
	t.Parallel()
	admission := testNDDevAdmission()
	directory := t.TempDir()
	admission.controller.Store = providerjournal.Store{
		Path: filepath.Join(directory, "provider-journal.json"), LockPath: filepath.Join(directory, "provider-journal.lock"),
	}
	now := time.Now().UTC()
	_, err := admission.controller.Store.Update(context.Background(), func(journal *providerjournal.Journal) error {
		journal.Leases["runner-pending"] = providerjournal.Lease{
			InstanceName: "runner-pending", ControllerID: testControllerID, PoolID: "pool-test",
			PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 10240,
			ImageFingerprint: testImageDigest, State: providerjournal.StateAdmitted,
			AdmittedAt: now, UpdatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		}
		return nil
	})
	require.NoError(t, err)
	cli := new(MockIncusServer)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{{Instance: api.Instance{Name: "runner-pending"}}}, nil).Once()
	allocations, err := admission.observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Equal(t, []provideradmission.Allocation{{
		InstanceName: "runner-pending", ControllerID: testControllerID, PoolID: "pool-test",
		PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 10240,
		ImageFingerprint: testImageDigest, State: providerjournal.StateAdmitted, JobName: "runner-pending",
	}}, allocations)
}

func TestReconcileRetainsDeletingLeaseUntilClusterTombstoneDisappears(t *testing.T) {
	t.Parallel()
	admission := testNDDevAdmission()
	directory := t.TempDir()
	admission.controller = provideradmission.Controller{
		Store: providerjournal.Store{
			Path: filepath.Join(directory, "provider-journal.json"), LockPath: filepath.Join(directory, "provider-journal.lock"),
		},
		ControllerID: testControllerID,
		LeaseTTL:     5 * time.Minute,
	}
	now := time.Now().UTC()
	_, err := admission.controller.Store.Update(context.Background(), func(journal *providerjournal.Journal) error {
		journal.Leases["runner-deleting"] = providerjournal.Lease{
			InstanceName: "runner-deleting", ControllerID: testControllerID, PoolID: "pool-test",
			PoolName: "nddev-linux-standard", VCPU: 4, MemoryMiB: 10240,
			ImageFingerprint: testImageDigest, State: providerjournal.StateDeleting,
			AdmittedAt: now, UpdatedAt: now, ExpiresAt: now.Add(5 * time.Minute),
		}
		return nil
	})
	require.NoError(t, err)

	cli := new(MockIncusServer)
	cli.On("GetInstancesFull", api.InstanceTypeAny).
		Return([]api.InstanceFull{{Instance: api.Instance{Name: "runner-deleting"}}}, nil).Once()
	require.NoError(t, admission.Reconcile(context.Background(), cli))
	journal, err := admission.controller.Store.ReadOnly(context.Background())
	require.NoError(t, err)
	require.Equal(t, providerjournal.StateDeleting, journal.Leases["runner-deleting"].State)

	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{}, nil).Once()
	require.NoError(t, admission.Reconcile(context.Background(), cli))
	journal, err = admission.controller.Store.ReadOnly(context.Background())
	require.NoError(t, err)
	require.NotContains(t, journal.Leases, "runner-deleting")
	cli.AssertExpectations(t)
}

func TestObservedAllocationsShareOneControllerAcrossPinnedPoolImages(t *testing.T) {
	admission := testNDDevAdmission()
	admission.platform.Pools = append(admission.platform.Pools, platformconfig.Pool{
		Name:         "nddev-linux-integration",
		ScaleSetName: "nddev-linux-integration",
		Trust:        "trusted",
		Resources: platformconfig.Resources{
			VCPU:      4,
			MemoryMiB: 10240,
		},
		Capabilities: platformconfig.Capabilities{
			Docker:          true,
			NetworkPolicy:   "public-internet",
			CacheWriteScope: "trusted",
		},
	})
	admission.workerImages["nddev-linux-integration"] = providerconfig.WorkerImage{
		Alias:       testIntegrationImageAlias,
		Fingerprint: testIntegrationImageDigest,
		Variant:     "integration",
	}
	standard := *ownedInstance("runner-standard")
	integration := *ownedInstance("runner-integration")
	integration.Profiles = []string{"nddev-linux-integration"}
	integration.ExpandedConfig = make(map[string]string, len(standard.ExpandedConfig))
	for key, value := range standard.ExpandedConfig {
		integration.ExpandedConfig[key] = value
	}
	integration.ExpandedConfig[poolIDKey] = "pool-integration-test"
	integration.ExpandedConfig[flavorKey] = "nddev-linux-integration"
	integration.ExpandedConfig[scaleSetKey] = "nddev-linux-integration"
	integration.ExpandedConfig[imageAliasKey] = testIntegrationImageAlias
	integration.ExpandedConfig[imageFingerprintKey] = testIntegrationImageDigest

	cli := new(MockIncusServer)
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{standard, integration}, nil).Once()
	allocations, err := admission.observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Len(t, allocations, 2)
	require.Equal(t, testImageDigest, allocations[0].ImageFingerprint)
	require.Equal(t, testIntegrationImageDigest, allocations[1].ImageFingerprint)
}

func TestObservedAllocationsRejectDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*api.InstanceFull)
	}{
		{"container", func(instance *api.InstanceFull) { instance.Type = string(api.InstanceTypeContainer) }},
		{"controller", func(instance *api.InstanceFull) { instance.ExpandedConfig[controllerIDKeyName] = "foreign" }},
		{"image", func(instance *api.InstanceFull) { instance.ExpandedConfig[imageFingerprintKey] = "drift" }},
		{"nested virtualization", func(instance *api.InstanceFull) { instance.ExpandedConfig["raw.qemu"] = "" }},
		{"unknown flavor", func(instance *api.InstanceFull) { instance.ExpandedConfig[flavorKey] = "unknown" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cli := new(MockIncusServer)
			instance := ownedInstance("runner")
			test.mutate(instance)
			cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*instance}, nil).Once()
			_, err := testNDDevAdmission().observedAllocations(context.Background(), cli)
			require.Error(t, err)
		})
	}
}

func TestObservedAllocationsIgnoreStoppedCanceledCreateWithoutLease(t *testing.T) {
	cli := new(MockIncusServer)
	instance := ownedInstance("runner-canceled")
	delete(instance.ExpandedConfig, flavorKey)
	instance.State.Status = "Stopped"
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*instance}, nil).Once()

	allocations, err := testNDDevAdmission().observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Empty(t, allocations)
}

func TestObservedAllocationsAccountImageMaintenanceInstance(t *testing.T) {
	cli := new(MockIncusServer)
	instance := ownedInstance("gha-image-builder-test")
	instance.Profiles = []string{"nddev-linux-standard"}
	instance.ExpandedConfig = map[string]string{}
	cli.On("GetInstancesFull", api.InstanceTypeAny).Return([]api.InstanceFull{*instance}, nil).Once()

	admission := testNDDevAdmission()
	allocations, err := admission.observedAllocations(context.Background(), cli)
	require.NoError(t, err)
	require.Len(t, allocations, 1)
	require.Equal(t, "nddev-linux-standard", allocations[0].PoolName)
	pool, exists := admission.platform.Pool("nddev-linux-standard")
	require.True(t, exists)
	require.Equal(t, pool.Resources.VCPU, allocations[0].VCPU)
	require.Equal(t, pool.Resources.MemoryMiB, allocations[0].MemoryMiB)
}
