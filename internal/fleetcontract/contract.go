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
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

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
	SchemaVersion   int         `json:"schema_version" yaml:"schema_version"`
	ContractVersion int         `json:"contract_version" yaml:"contract_version"`
	Guarantees      []Guarantee `json:"guarantees" yaml:"guarantees"`
	NotContractual  []string    `json:"not_contractual" yaml:"not_contractual"`
	OpenBlockers    []Blocker   `json:"open_blockers" yaml:"open_blockers"`
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

	RunnerClasses []RunnerClass `json:"runner_classes"`
	Tenants       []TenantEntry `json:"tenants"`
	Artifacts     Artifacts     `json:"artifacts"`
	Merge         Merge         `json:"merge"`

	Guarantees     []Guarantee `json:"guarantees"`
	NotContractual []string    `json:"not_contractual"`
	OpenBlockers   []Blocker   `json:"open_blockers"`
}

// RunnerClass is one label a consumer may put in `runs-on`.
type RunnerClass struct {
	Label string `json:"label"`
	Image string `json:"image"`
	// Docker reports whether a job of this class can run containers and service
	// containers. It is the one capability difference a consumer chooses on.
	Docker bool `json:"docker"`
}

// TenantEntry is one account the fleet admits work from.
type TenantEntry struct {
	ID                 string `json:"id"`
	Owner              string `json:"owner"`
	Repository         string `json:"repository,omitempty"`
	ServesWholeAccount bool   `json:"serves_whole_account"`
}

type Artifacts struct {
	GARMVersion      string `json:"garm_version"`
	GARMBinarySHA256 string `json:"garm_binary_sha256"`
	// ProviderBinarySHA256 is deliberately absent: the provider is not built
	// reproducibly to a declared digest, so there is none to state. See #263.
	ProviderVersion   string `json:"provider_version"`
	ProviderInterface string `json:"provider_interface"`
	IncusSDKVersion   string `json:"incus_sdk_version"`
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
			Label: class.Name,
			Image: class.Image,
			// A class boots the Docker-capable image exactly when it is the
			// integration class; deriving it from the image alias rather than
			// from the name keeps the two from disagreeing.
			Docker: class.Image == garmbootstrap.IntegrationImage,
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
			GARMVersion:       garm.DerivativeVersion,
			GARMBinarySHA256:  garm.Build.BinarySHA256,
			ProviderVersion:   provider.DerivativeVersion,
			ProviderInterface: provider.InterfaceVersion,
			IncusSDKVersion:   provider.Runtime.IncusSDKVersion,
		},
		Merge:          merge,
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
	if d.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: %d is not a schema this reader speaks", d.SchemaVersion)
	}
	if d.ContractVersion < 1 {
		return fmt.Errorf("contract_version: %d must be at least 1", d.ContractVersion)
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
