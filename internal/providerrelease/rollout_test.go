package providerrelease

import (
	"path/filepath"
	"testing"
)

func TestProviderRolloutContract(t *testing.T) {
	t.Parallel()
	contract, err := LoadRolloutContract(filepath.Join("..", "..", "config", "provider-rollout-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	if contract.OrderedPhases[3] != "observer-restart" {
		t.Fatal("observer restart is not ordered after manager restart")
	}
}
