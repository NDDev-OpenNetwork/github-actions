package providerrollout

import (
	"path/filepath"
	"testing"
)

func TestProviderRolloutContract(t *testing.T) {
	t.Parallel()
	contract, err := Load(filepath.Join("..", "..", "config", "provider-rollout-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if contract.OrderedPhases[2] != "observer-restart" {
		t.Fatal("observer restart is not ordered after the services identity swap")
	}
}

func TestProviderOnlyRolloutCannotRestartManagerOrOmitConvergence(t *testing.T) {
	contract, err := Load(filepath.Join("..", "..", "config", "provider-rollout-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Contract){
		func(c *Contract) { c.PreserveManager = false },
		func(c *Contract) { c.RestartUnits = append(c.RestartUnits, "garm.service") },
		func(c *Contract) { c.Convergence.NaturalJobRequired = false },
		func(c *Contract) { c.OrderedPhases = []string{"bounded-convergence"} },
	} {
		changed := contract
		mutate(&changed)
		if changed.Validate() == nil {
			t.Fatal("unsafe or incomplete rollout contract accepted")
		}
	}
}
