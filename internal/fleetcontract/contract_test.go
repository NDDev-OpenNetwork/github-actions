package fleetcontract

import "testing"

func TestPublicExampleContractBuildsWithoutEstateAccess(t *testing.T) {
	contract, err := Build(Sources{Root: "../.."}, "0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if contract.Repository != "NDDev-OpenNetwork/github-actions" || len(contract.RunnerClasses) == 0 ||
		len(contract.Tenants) == 0 || len(contract.Merge.RequiredContexts) != 1 || contract.Merge.RequiredContexts[0] != "Gate" {
		t.Fatalf("public contract = %#v", contract)
	}
}
