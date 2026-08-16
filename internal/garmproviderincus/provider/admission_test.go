package provider

import (
	"testing"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/provideradmission"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerjournal"
	"github.com/lxc/incus/v7/shared/api"
	"github.com/stretchr/testify/require"
)

func testNDDevAdmission() *nddevAdmission {
	return &nddevAdmission{
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

	allocations, err := testNDDevAdmission().observedAllocations(cli)
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
	allocations, err := admission.observedAllocations(cli)
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
			_, err := testNDDevAdmission().observedAllocations(cli)
			require.Error(t, err)
		})
	}
}
