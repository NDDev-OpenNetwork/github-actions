package fleetcontract

import (
	"os"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmbootstrap"
	"github.com/NDDev-OpenNetwork/github-actions/internal/garmderivative"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

const repositoryRoot = "../.."

func build(t *testing.T) Contract {
	t.Helper()
	contract, err := Build(Sources{Root: repositoryRoot}, "0000000000000000000000000000000000000000")
	if err != nil {
		t.Fatalf("build contract: %v", err)
	}
	return contract
}

// Every runner class the reconciler can register has to appear, because a
// consumer choosing a label reads this and nothing else. Deriving it from
// PublishedScaleSets rather than listing it is what keeps a newly published
// class from being invisible to the consumers it was published for.
func TestRunnerClassesAreEveryPublishedClass(t *testing.T) {
	t.Parallel()
	contract := build(t)
	published := garmbootstrap.PublishedScaleSets()
	if len(contract.RunnerClasses) != len(published) {
		t.Fatalf("contract offers %d classes, the reconciler publishes %d", len(contract.RunnerClasses), len(published))
	}
	for index, class := range published {
		if contract.RunnerClasses[index].Label != class.Name {
			t.Errorf("class %d is %q, reconciler publishes %q", index, contract.RunnerClasses[index].Label, class.Name)
		}
		if contract.RunnerClasses[index].Image != class.Image {
			t.Errorf("class %q boots %q, reconciler registers %q", class.Name, contract.RunnerClasses[index].Image, class.Image)
		}
	}
	// Docker capability is the one thing a consumer picks a class on, so getting
	// it from the image alias rather than the label keeps the two from drifting.
	for _, class := range contract.RunnerClasses {
		wantDocker := class.Image == garmbootstrap.IntegrationImage
		if class.Docker != wantDocker {
			t.Errorf("class %q reports docker=%v while booting %q", class.Label, class.Docker, class.Image)
		}
	}
}

// The tenant registry is the only place a tenant is onboarded, so it is the only
// place the contract may learn one from.
func TestTenantsAreTheWholeRegistry(t *testing.T) {
	t.Parallel()
	contract := build(t)
	if len(contract.Tenants) != len(tenant.IDs()) {
		t.Fatalf("contract names %d tenants, the registry holds %d", len(contract.Tenants), len(tenant.IDs()))
	}
	for _, entry := range contract.Tenants {
		registered, err := tenant.ByID(entry.ID)
		if err != nil {
			t.Fatalf("contract names tenant %q, which the registry does not: %v", entry.ID, err)
		}
		if entry.Owner != registered.Owner || entry.ServesWholeAccount != registered.ServesWholeAccount {
			t.Errorf("tenant %q in the contract does not match the registry: %#v vs %#v", entry.ID, entry, registered)
		}
	}
}

// Artifact identities come from the manifests that already state them. A
// contract restating a version is a contract that can be wrong about it, which
// is the defect #263 and the upstream-baseline digest both were.
func TestArtifactIdentitiesComeFromTheManifests(t *testing.T) {
	t.Parallel()
	contract := build(t)
	garm, err := garmderivative.Load(repositoryRoot + "/" + garmderivative.DefaultManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := providerrelease.Load(repositoryRoot + "/" + providerrelease.DefaultManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if contract.Artifacts.GARMVersion != garm.DerivativeVersion ||
		contract.Artifacts.GARMBinarySHA256 != garm.Build.BinarySHA256 {
		t.Errorf("GARM identity %#v does not match the manifest", contract.Artifacts)
	}
	if contract.Artifacts.ProviderVersion != provider.DerivativeVersion ||
		contract.Artifacts.ProviderInterface != provider.InterfaceVersion ||
		contract.Artifacts.IncusSDKVersion != provider.Runtime.IncusSDKVersion {
		t.Errorf("provider identity %#v does not match the manifest", contract.Artifacts)
	}
}

// A consumer pinning a commit is relying on what that commit was proven to
// satisfy, so the required merge context is part of the contract and has to come
// from the governance declaration rather than from prose.
func TestMergeRequirementComesFromTheProtectionDeclaration(t *testing.T) {
	t.Parallel()
	contract := build(t)
	raw, err := os.ReadFile(repositoryRoot + "/" + branchProtectionPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, context := range contract.Merge.RequiredContexts {
		if !strings.Contains(string(raw), context) {
			t.Errorf("contract requires context %q, which the protection declaration does not name", context)
		}
	}
	if len(contract.Merge.RequiredContexts) == 0 || contract.Merge.Branch == "" {
		t.Fatal("the contract states no merge requirement at all")
	}
}

// The declaration must hold only what cannot be derived. A version or a label
// written there would be a second authority, and the whole point of assembling
// the contract is that it has none.
func TestTheDeclarationRestatesNothingItsSourcesAlreadyState(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(repositoryRoot + "/" + DefaultDeclarationPath)
	if err != nil {
		t.Fatal(err)
	}
	// Comments are scanned out. A comment explaining why a field exists may name
	// a repository or an issue -- the header points at
	// NDDev-OpenNetwork/github-device-sync#172, which is the reason this file exists --
	// and that is prose, not a second declaration of a value.
	var values strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		values.WriteString(line)
		values.WriteString("\n")
	}
	declaration := values.String()
	contract := build(t)

	forbidden := map[string]string{
		contract.Artifacts.GARMVersion:      "the GARM derivative version",
		contract.Artifacts.GARMBinarySHA256: "the GARM binary digest",
		contract.Artifacts.ProviderVersion:  "the provider version",
		contract.Artifacts.IncusSDKVersion:  "the Incus SDK version",
	}
	for _, class := range contract.RunnerClasses {
		forbidden[class.Label] = "a runner class label"
		forbidden[class.Image] = "a worker image alias"
	}
	for _, entry := range contract.Tenants {
		forbidden[entry.Owner] = "a tenant owner"
	}
	for value, what := range forbidden {
		if strings.Contains(declaration, value) {
			t.Errorf("the declaration writes down %s (%q); it is derived, and writing it here makes a second authority", what, value)
		}
	}
}

func TestValidateRejectsAContractNobodyCouldRelyOn(t *testing.T) {
	t.Parallel()
	base, err := LoadDeclaration(repositoryRoot + "/" + DefaultDeclarationPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*Declaration)
		message string
	}{
		{"unknown schema", func(d *Declaration) { d.SchemaVersion = 2 }, "schema_version"},
		{"unversioned contract", func(d *Declaration) { d.ContractVersion = 0 }, "contract_version"},
		{"promises nothing", func(d *Declaration) { d.Guarantees = nil }, "promises nothing"},
		{"promise with no subject", func(d *Declaration) { d.Guarantees[0].Subject = " " }, "subject and a promise"},
		{"blocker with no issue", func(d *Declaration) { d.OpenBlockers[0].Issue = 0 }, "needs the issue"},
	} {
		mutated := base
		mutated.Guarantees = append([]Guarantee(nil), base.Guarantees...)
		mutated.OpenBlockers = append([]Blocker(nil), base.OpenBlockers...)
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil {
			t.Errorf("%s: accepted", testCase.name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: error %q does not mention %q", testCase.name, err, testCase.message)
		}
	}
}
