package providerrollout

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
)

type Contract struct {
	SchemaVersion int      `json:"schema_version"`
	OrderedPhases []string `json:"ordered_phases"`
	RestartUnits  []string `json:"restart_units"`
	Convergence   struct {
		HealthRequired                    bool `json:"health_required"`
		FreshSampleRequired               bool `json:"fresh_sample_required"`
		CollectionErrorsMustEqual         int  `json:"collection_errors_must_equal"`
		VisibleInventoryMustMatchProvider bool `json:"visible_inventory_must_match_provider"`
		QueueIdentityMustBePreserved      bool `json:"queue_identity_must_be_preserved"`
		NaturalJobRequired                bool `json:"natural_job_required"`
	} `json:"convergence"`
}

func Load(path string) (Contract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Contract{}, fmt.Errorf("read rollout contract: %w", err)
	}
	var contract Contract
	if err := json.Unmarshal(data, &contract); err != nil {
		return Contract{}, fmt.Errorf("decode rollout contract: %w", err)
	}
	if err := contract.Validate(); err != nil {
		return Contract{}, err
	}
	return contract, nil
}

func (contract Contract) Validate() error {
	wantPhases := []string{
		"platform-policies",
		"provider-binary-and-config",
		"manager-restart",
		"observer-restart",
		"bounded-convergence",
	}
	if contract.SchemaVersion != 1 {
		return fmt.Errorf("unsupported rollout contract schema %d", contract.SchemaVersion)
	}
	if !slices.Equal(contract.OrderedPhases, wantPhases) {
		return fmt.Errorf("provider rollout phases must be %v", wantPhases)
	}
	if !slices.Equal(contract.RestartUnits, []string{"garm.service", "gha-fleet-observer.service"}) {
		return fmt.Errorf("provider rollout must restart manager then observer")
	}
	convergence := contract.Convergence
	if !convergence.HealthRequired || !convergence.FreshSampleRequired ||
		convergence.CollectionErrorsMustEqual != 0 ||
		!convergence.VisibleInventoryMustMatchProvider ||
		!convergence.QueueIdentityMustBePreserved || !convergence.NaturalJobRequired {
		return fmt.Errorf("provider rollout convergence contract is incomplete")
	}
	return nil
}
