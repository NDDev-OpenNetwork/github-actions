package tenant

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"

	"gopkg.in/yaml.v3"
)

const DefaultRegistryPath = "/etc/gha-fleet/tenant-registry.yaml"
const maxRegistryBytes = 64 * 1024

// Registry is the runtime-owned account boundary shared by GARM reconciliation
// and GitHub App bootstrap/verification. The public repository ships a
// synthetic example file; production identities are loaded from the estate.
type Registry struct {
	SchemaVersion int      `json:"schema_version" yaml:"schema_version"`
	DefaultID     string   `json:"default_tenant" yaml:"default_tenant"`
	Tenants       []Tenant `json:"tenants" yaml:"tenants"`
}

func Load(path string) (Registry, error) {
	file, err := os.Open(path)
	if err != nil {
		return Registry{}, fmt.Errorf("open tenant registry: %w", err)
	}
	defer file.Close()
	registry, err := Decode(file)
	if err != nil {
		return Registry{}, fmt.Errorf("decode tenant registry %s: %w", path, err)
	}
	return registry, nil
}

func Decode(reader io.Reader) (Registry, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxRegistryBytes+1))
	if err != nil || len(data) > maxRegistryBytes {
		return Registry{}, fmt.Errorf("tenant registry exceeds the bounded input")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var registry Registry
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, fmt.Errorf("parse YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Registry{}, fmt.Errorf("multiple YAML documents are not allowed")
	}
	if err := registry.Validate(); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func (r Registry) Validate() error {
	if r.SchemaVersion != 1 || r.DefaultID == "" || len(r.Tenants) == 0 {
		return fmt.Errorf("tenant registry identity is incomplete")
	}
	ids := make([]string, len(r.Tenants))
	owners := make(map[string]string, len(r.Tenants))
	bindings := make(map[string]string)
	defaultFound := false
	for index, selected := range r.Tenants {
		if err := selected.Validate(); err != nil {
			return err
		}
		ids[index] = selected.ID
		if index > 0 && selected.ID <= r.Tenants[index-1].ID {
			return fmt.Errorf("tenant registry rows must be strictly sorted by id")
		}
		if previous, exists := owners[selected.Owner]; exists {
			return fmt.Errorf("owner %q is shared by tenants %q and %q", selected.Owner, previous, selected.ID)
		}
		owners[selected.Owner] = selected.ID
		defaultFound = defaultFound || selected.ID == r.DefaultID
		for scaleSet, repository := range selected.ScaleSetRepositories {
			if previous, exists := bindings[scaleSet]; exists {
				return fmt.Errorf("scale set %q is bound to both %q and %q", scaleSet, previous, repository)
			}
			bindings[scaleSet] = repository
		}
	}
	if !defaultFound || !slices.IsSorted(ids) {
		return fmt.Errorf("default tenant is absent or registry order is invalid")
	}
	return nil
}

func (r Registry) ByID(id string) (Tenant, error) {
	if id == "" {
		id = r.DefaultID
	}
	for _, selected := range r.Tenants {
		if selected.ID == id {
			return cloneTenant(selected), nil
		}
	}
	return Tenant{}, fmt.Errorf("unknown tenant %q", id)
}

func (r Registry) IDs() []string {
	ids := make([]string, 0, len(r.Tenants))
	for _, selected := range r.Tenants {
		ids = append(ids, selected.ID)
	}
	sort.Strings(ids)
	return ids
}

func ExampleRegistry() Registry {
	ids := IDs()
	rows := make([]Tenant, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, cloneTenant(tenants[id]))
	}
	return Registry{SchemaVersion: 1, DefaultID: DefaultID, Tenants: rows}
}

func Resolve(registry Registry, id string) (Tenant, Registry, error) {
	if registry.SchemaVersion == 0 {
		registry = ExampleRegistry()
	}
	if err := registry.Validate(); err != nil {
		return Tenant{}, Registry{}, err
	}
	selected, err := registry.ByID(id)
	return selected, registry, err
}

func cloneTenant(source Tenant) Tenant {
	copy := source
	copy.ManagedRepositories = slices.Clone(source.ManagedRepositories)
	copy.ScaleSetRepositories = make(map[string]string, len(source.ScaleSetRepositories))
	for key, value := range source.ScaleSetRepositories {
		copy.ScaleSetRepositories[key] = value
	}
	return copy
}
