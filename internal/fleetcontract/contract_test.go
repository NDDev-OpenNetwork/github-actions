package fleetcontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
)

func TestQueueIntentSchemaMustMoveAsOneControlPlaneContract(t *testing.T) {
	garm := garmderivative.Manifest{RuntimeContract: garmderivative.RuntimeContract{QueueIntentSchemaVersion: 3}}
	provider := providerrelease.Manifest{Runtime: providerrelease.Runtime{QueueIntentSchemaVersion: 2}}
	if err := validateQueueIntentSchemaPair(garm, provider); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("incompatible queue journal readers were accepted: %v", err)
	}
	provider.Runtime.QueueIntentSchemaVersion = 3
	if err := validateQueueIntentSchemaPair(garm, provider); err != nil {
		t.Fatalf("matching queue journal readers were rejected: %v", err)
	}
}

func TestPublicExampleContractBuildsWithoutEstateAccess(t *testing.T) {
	contract, err := Build(Sources{Root: "../.."}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if contract.Repository != "NDDev-OpenNetwork/github-actions" || len(contract.RunnerClasses) == 0 ||
		len(contract.Tenants) == 0 || len(contract.Merge.RequiredContexts) != 1 || contract.Merge.RequiredContexts[0] != "Gate" {
		t.Fatalf("public contract = %#v", contract)
	}
	if contract.SchemaVersion != 2 || contract.ContractVersion != 3 || contract.Execution.WorkerKind != "incus-container" ||
		!contract.Execution.Ephemeral || contract.Execution.JobsPerWorker != 1 || !contract.ResourceSemantics.HardMemoryExcludesEmergencySwap ||
		contract.ResourceSemantics.EmergencySwapSchedulable || contract.ResourceSemantics.CPUMode != "weighted-overcommit" {
		t.Fatalf("contract v2 semantics = %#v", contract)
	}
	for _, class := range contract.RunnerClasses {
		if class.WorkerKind != "incus-container" || !class.Ephemeral || class.JobsPerWorker != 1 || class.Warm.Supported ||
			class.Resources.VCPU < 1 || class.Resources.VCPUMin < 1 || class.Resources.VCPUMax != class.Resources.VCPU ||
			class.Resources.VCPUMin > class.Resources.VCPUMax || class.Resources.MemoryMiB < 1024 || class.Resources.DiskGiB < 10 {
			t.Fatalf("runner class is not a complete cold container contract: %#v", class)
		}
	}
}

func TestPublishedClassContractMatchesPortableServicesConfig(t *testing.T) {
	contract, err := Build(Sources{Root: "../.."}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	platform, err := config.Load(filepath.Join("..", "..", "config", "example-services.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateConfig(contract, platform); err != nil {
		t.Fatal(err)
	}
	pools := make(map[string]config.Pool, len(platform.Pools))
	for _, pool := range platform.Pools {
		pools[pool.ScaleSetName] = pool
	}
	for _, class := range contract.RunnerClasses {
		pool, ok := pools[class.Label]
		if !ok {
			t.Fatalf("published class %q has no portable pool", class.Label)
		}
		if class.Trust != pool.Trust || class.Credentials != pool.Capabilities.Credentials ||
			class.NetworkPolicy != pool.Capabilities.NetworkPolicy || class.CacheWriteScope != pool.Capabilities.CacheWriteScope ||
			class.Docker != pool.Capabilities.Docker || class.Resources.VCPU != pool.Resources.VCPU ||
			class.Resources.MemoryMiB != pool.Resources.MemoryMiB || class.Resources.DiskGiB != pool.Resources.DiskGiB ||
			class.Warm.TargetReady != pool.Warm.TargetReady || class.Warm.MaxReady != pool.Warm.MaxReady {
			t.Fatalf("published class and portable pool disagree: class=%#v pool=%#v", class, pool)
		}
	}
}

func TestDeploymentOverlayCannotWeakenContract(t *testing.T) {
	contract, err := Build(Sources{Root: "../.."}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	platform, err := config.Load(filepath.Join("..", "..", "config", "example-services.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*config.Config){
		"provider version drift": func(candidate *config.Config) { candidate.ControlPlane.ProviderVersion = "v0.1.5-nddev.40" },
		"vm worker":              func(candidate *config.Config) { candidate.ControlPlane.WorkerKind = "incus-vm" },
		"schedulable swap":       func(candidate *config.Config) { candidate.Guardrails.EmergencySwapSchedulable = true },
		"pressure disabled":      func(candidate *config.Config) { candidate.Pressure.Required = false },
		"resource drift":         func(candidate *config.Config) { candidate.Pools[0].Resources.MemoryMiB++ },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := platform
			candidate.Pools = append([]config.Pool(nil), platform.Pools...)
			mutate(&candidate)
			if err := ValidateConfig(contract, candidate); err == nil {
				t.Fatal("weakened overlay was accepted")
			}
		})
	}
}

func TestDeploymentOverlayMayTuneCPUInsidePublishedEnvelope(t *testing.T) {
	contract, err := Build(Sources{Root: "../.."}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	platform, err := config.Load(filepath.Join("..", "..", "config", "example-services.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	index := -1
	for candidate := range platform.Pools {
		if platform.Pools[candidate].ScaleSetName == "nddev-priority-integration" {
			index = candidate
			break
		}
	}
	if index < 0 {
		t.Fatal("portable priority integration pool is missing")
	}
	platform.Pools[index].Resources.VCPU = 2
	if err := ValidateConfig(contract, platform); err != nil {
		t.Fatalf("safe CPU tuning was rejected: %v", err)
	}
	platform.Pools[index].Resources.VCPU = 1
	if err := ValidateConfig(contract, platform); err == nil {
		t.Fatal("CPU below the published envelope was accepted")
	}
	platform.Pools[index].Resources.VCPU = 5
	if err := ValidateConfig(contract, platform); err == nil {
		t.Fatal("CPU above the published envelope was accepted")
	}
}

func TestDeclarationRejectsVMAndSchedulableSwapRegression(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", DefaultDeclarationPath))
	if err != nil {
		t.Fatal(err)
	}
	for name, mutation := range map[string]func(string) string{
		"vm worker": func(value string) string {
			return strings.Replace(value, "worker_kind: incus-container", "worker_kind: incus-vm", 1)
		},
		"schedulable swap": func(value string) string {
			return strings.Replace(value, "emergency_swap_schedulable: false", "emergency_swap_schedulable: true", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fleet-contract.yaml")
			if err := os.WriteFile(path, []byte(mutation(string(raw))), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadDeclaration(path); err == nil {
				t.Fatal("invalid v2 declaration was accepted")
			}
		})
	}
}
