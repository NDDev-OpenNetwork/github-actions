// Package fleetcontract assembles the one statement of what this fleet offers.
//
// It exists because a consumer -- github-device-sync, which pins this repository
// as a submodule -- had no artifact to point at. Its pin has been frozen since
// 2d277bd, 108 commits back, waiting for "an immutable surface:5 handoff that
// defines the supported runner label, the admission and authorization contract,
// the rollout state, and the exact module commit with its acceptance evidence"
// (example-org/github-device-sync#172).
//
// Almost all of it is derived. config/fleet-contract.yaml holds only what cannot
// be: which promises are contractual, which are not, and what is still open. The
// runner classes, the tenants, the artifact identities and the required merge
// context all come from the code and manifests that already state them, so the
// contract cannot disagree with the fleet it describes.
package fleetcontract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

// DefaultDeclarationPath is the declared half, relative to the repository root.
const DefaultDeclarationPath = "config/fleet-contract.yaml"

const maxDeclarationBytes = 64 * 1024

// Declaration is config/fleet-contract.yaml.
type Declaration struct {
	SchemaVersion     int               `json:"schema_version" yaml:"schema_version"`
	ContractVersion   int               `json:"contract_version" yaml:"contract_version"`
	Execution         Execution         `json:"execution" yaml:"execution"`
	ResourceSemantics ResourceSemantics `json:"resource_semantics" yaml:"resource_semantics"`
	Lifecycle         Lifecycle         `json:"lifecycle" yaml:"lifecycle"`
	Observability     Observability     `json:"observability" yaml:"observability"`
	Guarantees        []Guarantee       `json:"guarantees" yaml:"guarantees"`
	NotContractual    []string          `json:"not_contractual" yaml:"not_contractual"`
	OpenBlockers      []Blocker         `json:"open_blockers" yaml:"open_blockers"`
}

// ValidateConfig proves that one deployment overlay implements this exact
// public contract without making private topology part of the public source.
func ValidateConfig(contract Contract, platform platformconfig.Config) error {
	if platform.ControlPlane.WorkerKind != contract.Execution.WorkerKind ||
		platform.Guardrails.RequireEphemeral != contract.Execution.Ephemeral ||
		platform.Guardrails.JobsPerWorker != contract.Execution.JobsPerWorker {
		return fmt.Errorf("execution semantics differ from fleet contract v%d", contract.ContractVersion)
	}
	if platform.Guardrails.CPUSchedulingMode != contract.ResourceSemantics.CPUMode ||
		platform.Guardrails.HardMemoryExcludesEmergencySwap != contract.ResourceSemantics.HardMemoryExcludesEmergencySwap ||
		platform.Guardrails.EmergencySwapSchedulable != contract.ResourceSemantics.EmergencySwapSchedulable {
		return fmt.Errorf("resource semantics differ from fleet contract v%d", contract.ContractVersion)
	}
	for _, class := range contract.RunnerClasses {
		pool, exists := platform.Pool(class.Label)
		if !exists {
			return fmt.Errorf("deployment overlay has no pool for published class %q", class.Label)
		}
		backend, exists := platform.Backend(pool.Backend)
		if !exists || backend.Implementation != class.WorkerKind {
			return fmt.Errorf("pool %q backend does not implement %s", class.Label, class.WorkerKind)
		}
		if pool.Trust != class.Trust || pool.Capabilities.Credentials != class.Credentials ||
			pool.Capabilities.NetworkPolicy != class.NetworkPolicy || pool.Capabilities.CacheWriteScope != class.CacheWriteScope ||
			pool.Capabilities.Docker != class.Docker || pool.Resources.VCPU != class.Resources.VCPU ||
			pool.Resources.MemoryMiB != class.Resources.MemoryMiB || pool.Resources.DiskGiB != class.Resources.DiskGiB ||
			pool.Warm.TargetReady != class.Warm.TargetReady || pool.Warm.MaxReady != class.Warm.MaxReady {
			return fmt.Errorf("pool %q capability or resource shape differs from fleet contract v%d", class.Label, contract.ContractVersion)
		}
	}
	return nil
}

type Execution struct {
	WorkerKind                string `json:"worker_kind" yaml:"worker_kind"`
	Ephemeral                 bool   `json:"ephemeral" yaml:"ephemeral"`
	JobsPerWorker             int    `json:"jobs_per_worker" yaml:"jobs_per_worker"`
	ExecutedWorkerDisposition string `json:"executed_worker_disposition" yaml:"executed_worker_disposition"`
	WarmWorkerReuse           string `json:"warm_worker_reuse" yaml:"warm_worker_reuse"`
}

type ResourceSemantics struct {
	MemoryCommitment                string   `json:"memory_commitment" yaml:"memory_commitment"`
	HardMemoryExcludesEmergencySwap bool     `json:"hard_memory_excludes_emergency_swap" yaml:"hard_memory_excludes_emergency_swap"`
	EmergencySwapSchedulable        bool     `json:"emergency_swap_schedulable" yaml:"emergency_swap_schedulable"`
	CPUMode                         string   `json:"cpu_mode" yaml:"cpu_mode"`
	CPUHardQuota                    bool     `json:"cpu_hard_quota" yaml:"cpu_hard_quota"`
	PressureSignals                 []string `json:"pressure_signals" yaml:"pressure_signals"`
	AdmissionHysteresisRequired     bool     `json:"admission_hysteresis_required" yaml:"admission_hysteresis_required"`
}

type Lifecycle struct {
	States                      []string          `json:"states" yaml:"states"`
	PhaseSources                map[string]string `json:"phase_sources" yaml:"phase_sources"`
	AmbiguousAuthoritativeState string            `json:"ambiguous_authoritative_state" yaml:"ambiguous_authoritative_state"`
	CapacityRelease             string            `json:"capacity_release" yaml:"capacity_release"`
}

type Observability struct {
	PhaseCounts                 bool   `json:"phase_counts" yaml:"phase_counts"`
	PhaseOldestAge              bool   `json:"phase_oldest_age" yaml:"phase_oldest_age"`
	TransitionHistograms        bool   `json:"transition_histograms" yaml:"transition_histograms"`
	BoundedCorrelationIdentity  bool   `json:"bounded_correlation_identity" yaml:"bounded_correlation_identity"`
	ContainerAdmissionReadiness string `json:"container_admission_readiness" yaml:"container_admission_readiness"`
	VMPilotReadiness            string `json:"vm_pilot_readiness" yaml:"vm_pilot_readiness"`
}

type Guarantee struct {
	Subject string `json:"subject" yaml:"subject"`
	Promise string `json:"promise" yaml:"promise"`
}

type Blocker struct {
	Issue   int    `json:"issue" yaml:"issue"`
	Subject string `json:"subject" yaml:"subject"`
}

// Contract is the assembled statement: the declaration plus everything derived.
type Contract struct {
	SchemaVersion   int    `json:"schema_version"`
	ContractVersion int    `json:"contract_version"`
	Repository      string `json:"repository"`
	// Commit is the tree this contract describes. A consumer pins that commit;
	// anything else would be a contract about a tree nobody named.
	Commit string `json:"commit"`

	RunnerClasses     []RunnerClass     `json:"runner_classes"`
	Tenants           []TenantEntry     `json:"tenants"`
	Artifacts         Artifacts         `json:"artifacts"`
	Merge             Merge             `json:"merge"`
	Execution         Execution         `json:"execution"`
	ResourceSemantics ResourceSemantics `json:"resource_semantics"`
	Lifecycle         Lifecycle         `json:"lifecycle"`
	Observability     Observability     `json:"observability"`

	Guarantees     []Guarantee `json:"guarantees"`
	NotContractual []string    `json:"not_contractual"`
	OpenBlockers   []Blocker   `json:"open_blockers"`
}

// RunnerClass is one label a consumer may put in `runs-on`.
type RunnerClass struct {
	Label           string `json:"label"`
	Image           string `json:"image"`
	WorkerKind      string `json:"worker_kind"`
	Ephemeral       bool   `json:"ephemeral"`
	JobsPerWorker   int    `json:"jobs_per_worker"`
	Trust           string `json:"trust"`
	Credentials     string `json:"credentials"`
	NetworkPolicy   string `json:"network_policy"`
	CacheWriteScope string `json:"cache_write_scope"`
	// Docker reports whether a job of this class can run containers and service
	// containers. It is the one capability difference a consumer chooses on.
	Docker    bool      `json:"docker"`
	Resources Resources `json:"resources"`
	Warm      Warm      `json:"warm"`
}

type Resources struct {
	VCPU      int `json:"vcpu"`
	MemoryMiB int `json:"memory_mib"`
	DiskGiB   int `json:"disk_gib"`
}
type Warm struct {
	Supported   bool `json:"supported"`
	TargetReady int  `json:"target_ready"`
	MaxReady    int  `json:"max_ready"`
}

// TenantEntry is one account the fleet admits work from.
type TenantEntry struct {
	ID                 string `json:"id"`
	Owner              string `json:"owner"`
	Repository         string `json:"repository,omitempty"`
	ServesWholeAccount bool   `json:"serves_whole_account"`
}

type Artifacts struct {
	GARMVersion          string `json:"garm_version"`
	GARMBinarySHA256     string `json:"garm_binary_sha256"`
	ProviderVersion      string `json:"provider_version"`
	ProviderCommit       string `json:"provider_commit"`
	ProviderBinarySHA256 string `json:"provider_binary_sha256"`
	ProviderInterface    string `json:"provider_interface"`
	IncusSDKVersion      string `json:"incus_sdk_version"`
}

// Merge is what a commit on the default branch has been proven to satisfy. A
// consumer pinning a commit is relying on this, so it is part of the contract.
type Merge struct {
	Branch           string   `json:"branch"`
	RequiredContexts []string `json:"required_contexts"`
	Strict           bool     `json:"strict"`
	PullRequest      bool     `json:"pull_request_required"`
	EnforceAdmins    bool     `json:"enforce_admins"`
}

// Sources are the paths Build reads, all relative to the repository root.
type Sources struct {
	Root string
}

func (s Sources) path(relative string) string { return filepath.Join(s.Root, relative) }

// Build assembles the contract. Commit is passed in rather than shelled out for,
// because the caller knows whether it is describing HEAD or a specific tree.
func Build(sources Sources, commit string) (Contract, error) {
	declaration, err := LoadDeclaration(sources.path(DefaultDeclarationPath))
	if err != nil {
		return Contract{}, err
	}
	garm, err := garmderivative.Load(sources.path(garmderivative.DefaultManifestPath))
	if err != nil {
		return Contract{}, err
	}
	provider, err := providerrelease.Load(sources.path(providerrelease.DefaultManifestPath))
	if err != nil {
		return Contract{}, err
	}
	merge, err := loadMerge(sources.path(branchProtectionPath))
	if err != nil {
		return Contract{}, err
	}

	classes := make([]RunnerClass, 0, len(garmbootstrap.PublishedScaleSets()))
	for _, class := range garmbootstrap.PublishedScaleSets() {
		classes = append(classes, RunnerClass{
			Label:         class.Name,
			Image:         class.Image,
			WorkerKind:    declaration.Execution.WorkerKind,
			Ephemeral:     declaration.Execution.Ephemeral,
			JobsPerWorker: declaration.Execution.JobsPerWorker,
			Trust:         class.Trust, Credentials: class.Credentials,
			NetworkPolicy: class.NetworkPolicy, CacheWriteScope: class.CacheWriteScope,
			Docker:    class.Docker,
			Resources: Resources{VCPU: class.VCPU, MemoryMiB: class.MemoryMiB, DiskGiB: class.DiskGiB},
			Warm:      Warm{Supported: false, TargetReady: 0, MaxReady: 0},
		})
	}

	tenants := make([]TenantEntry, 0, len(tenant.IDs()))
	for _, id := range tenant.IDs() {
		entry, tenantErr := tenant.ByID(id)
		if tenantErr != nil {
			return Contract{}, fmt.Errorf("tenant %q: %w", id, tenantErr)
		}
		tenants = append(tenants, TenantEntry{
			ID:                 entry.ID,
			Owner:              entry.Owner,
			Repository:         entry.Repository,
			ServesWholeAccount: entry.ServesWholeAccount,
		})
	}

	return Contract{
		SchemaVersion:   declaration.SchemaVersion,
		ContractVersion: declaration.ContractVersion,
		Repository:      "NDDev-OpenNetwork/github-actions",
		Commit:          commit,
		RunnerClasses:   classes,
		Tenants:         tenants,
		Artifacts: Artifacts{
			GARMVersion:          garm.DerivativeVersion,
			GARMBinarySHA256:     garm.Build.BinarySHA256,
			ProviderVersion:      provider.DerivativeVersion,
			ProviderCommit:       provider.Build.SourceCommit,
			ProviderBinarySHA256: provider.Build.BinarySHA256,
			ProviderInterface:    provider.InterfaceVersion,
			IncusSDKVersion:      provider.Runtime.IncusSDKVersion,
		},
		Merge:     merge,
		Execution: declaration.Execution, ResourceSemantics: declaration.ResourceSemantics,
		Lifecycle: declaration.Lifecycle, Observability: declaration.Observability,
		Guarantees:     declaration.Guarantees,
		NotContractual: declaration.NotContractual,
		OpenBlockers:   declaration.OpenBlockers,
	}, nil
}

const branchProtectionPath = ".github/branch-protection.yaml"

type branchProtection struct {
	Branch               string `yaml:"branch"`
	RequiredStatusChecks struct {
		Contexts []string `yaml:"contexts"`
		Strict   bool     `yaml:"strict"`
	} `yaml:"required_status_checks"`
	RequiredPullRequestReviews *struct{} `yaml:"required_pull_request_reviews"`
	EnforceAdmins              bool      `yaml:"enforce_admins"`
}

func loadMerge(path string) (Merge, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Merge{}, fmt.Errorf("read branch protection declaration: %w", err)
	}
	var declared branchProtection
	// Not KnownFields: this reads the four fields a consumer cares about out of a
	// document owned by repository governance, and a field added there is not a
	// reason for the contract to stop rendering.
	if err := yaml.Unmarshal(raw, &declared); err != nil {
		return Merge{}, fmt.Errorf("parse branch protection declaration: %w", err)
	}
	if declared.Branch == "" || len(declared.RequiredStatusChecks.Contexts) == 0 {
		return Merge{}, fmt.Errorf("branch protection declaration names no branch or no required context")
	}
	contexts := slices.Clone(declared.RequiredStatusChecks.Contexts)
	slices.Sort(contexts)
	return Merge{
		Branch:           declared.Branch,
		RequiredContexts: contexts,
		Strict:           declared.RequiredStatusChecks.Strict,
		PullRequest:      declared.RequiredPullRequestReviews != nil,
		EnforceAdmins:    declared.EnforceAdmins,
	}, nil
}

func LoadDeclaration(path string) (Declaration, error) {
	file, err := os.Open(path)
	if err != nil {
		return Declaration{}, fmt.Errorf("open fleet contract declaration: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxDeclarationBytes+1))
	if err != nil {
		return Declaration{}, fmt.Errorf("read fleet contract declaration: %w", err)
	}
	if len(data) > maxDeclarationBytes {
		return Declaration{}, fmt.Errorf("fleet contract declaration exceeds %d bytes", maxDeclarationBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var declaration Declaration
	if err := decoder.Decode(&declaration); err != nil {
		return Declaration{}, fmt.Errorf("parse fleet contract declaration: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Declaration{}, fmt.Errorf("multiple YAML documents are not allowed")
		}
		return Declaration{}, fmt.Errorf("parse trailing YAML: %w", err)
	}
	if err := declaration.Validate(); err != nil {
		return Declaration{}, err
	}
	return declaration, nil
}

func (d Declaration) Validate() error {
	if d.SchemaVersion != 2 {
		return fmt.Errorf("schema_version: %d is not a schema this reader speaks", d.SchemaVersion)
	}
	if d.ContractVersion < 1 {
		return fmt.Errorf("contract_version: %d must be at least 1", d.ContractVersion)
	}
	if d.ContractVersion < 2 || d.Execution.WorkerKind != "incus-container" || !d.Execution.Ephemeral || d.Execution.JobsPerWorker != 1 || d.Execution.ExecutedWorkerDisposition != "destroy" || d.Execution.WarmWorkerReuse != "forbidden" {
		return fmt.Errorf("execution: contract v2 requires one-job ephemeral Incus containers that are destroyed and never reused")
	}
	if d.ResourceSemantics.MemoryCommitment != "hard" || !d.ResourceSemantics.HardMemoryExcludesEmergencySwap || d.ResourceSemantics.EmergencySwapSchedulable || d.ResourceSemantics.CPUMode != "weighted-overcommit" || d.ResourceSemantics.CPUHardQuota || !d.ResourceSemantics.AdmissionHysteresisRequired {
		return fmt.Errorf("resource_semantics: hard memory, non-schedulable swap, weighted CPU shares and hysteresis are required")
	}
	wantSignals := []string{"cpu-utilization", "cpu-psi", "memory-psi", "io-psi"}
	if !slices.Equal(d.ResourceSemantics.PressureSignals, wantSignals) {
		return fmt.Errorf("resource_semantics.pressure_signals: must be %v", wantSignals)
	}
	wantStates := []string{"queued", "reserved", "acquiring", "acquired", "provisioning", "running", "stopping", "deleting", "terminal"}
	wantSources := map[string]string{
		"queued": "queue-intent:queued", "reserved": "queue-intent:assigned",
		"acquiring": "queue-intent:acquiring", "acquired": "queue-intent:acquired",
		"provisioning": "provider-lease:admitted", "running": "queue-intent:running",
		"stopping": "provider-lease:deleting", "deleting": "provider-lease:deleting",
		"terminal": "reconciliation:removed",
	}
	if !slices.Equal(d.Lifecycle.States, wantStates) || !maps.Equal(d.Lifecycle.PhaseSources, wantSources) || d.Lifecycle.AmbiguousAuthoritativeState != "retain" || d.Lifecycle.CapacityRelease != "exactly-once" {
		return fmt.Errorf("lifecycle: states and fail-closed exactly-once semantics are incomplete")
	}
	if !d.Observability.PhaseCounts || !d.Observability.PhaseOldestAge || !d.Observability.TransitionHistograms || !d.Observability.BoundedCorrelationIdentity || d.Observability.ContainerAdmissionReadiness != "required" || d.Observability.VMPilotReadiness != "deprecated" {
		return fmt.Errorf("observability: phase metrics, bounded correlation and container readiness are required")
	}
	if len(d.Guarantees) == 0 {
		return fmt.Errorf("guarantees: a contract that promises nothing is not a contract")
	}
	seen := make(map[string]struct{}, len(d.Guarantees))
	for index, guarantee := range d.Guarantees {
		if strings.TrimSpace(guarantee.Subject) == "" || strings.TrimSpace(guarantee.Promise) == "" {
			return fmt.Errorf("guarantees[%d]: needs both a subject and a promise", index)
		}
		if _, duplicate := seen[guarantee.Subject]; duplicate {
			return fmt.Errorf("guarantees[%d]: %q is promised twice", index, guarantee.Subject)
		}
		seen[guarantee.Subject] = struct{}{}
	}
	for index, blocker := range d.OpenBlockers {
		if blocker.Issue <= 0 {
			return fmt.Errorf("open_blockers[%d]: needs the issue it refers to", index)
		}
		if strings.TrimSpace(blocker.Subject) == "" {
			return fmt.Errorf("open_blockers[%d]: needs a subject a consumer can act on", index)
		}
	}
	return nil
}
