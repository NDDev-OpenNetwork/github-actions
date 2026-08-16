package deploycontract

import (
	"path/filepath"
	"sort"
	"testing"

	fleetconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

// declaredHostConfigs finds every platform configuration in the repository.
//
// It is a glob rather than a list because the list was the defect: three tests
// named server-gha-runner-{1,2,3} and a fourth host was declared without any of
// them noticing. A host whose provider version, derivative version or pool
// tenancy nothing checks is a host that fails on the box instead of here.
func declaredHostConfigs(t *testing.T) map[string]fleetconfig.Config {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("..", "..", "config", "server-*.yaml"))
	if err != nil {
		t.Fatalf("glob platform configs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no config/server-*.yaml found; the glob no longer locates the repository root")
	}
	sort.Strings(matches)
	configs := make(map[string]fleetconfig.Config, len(matches))
	for _, path := range matches {
		cfg, loadErr := fleetconfig.Load(path)
		if loadErr != nil {
			t.Fatalf("load %s: %v", filepath.Base(path), loadErr)
		}
		configs[filepath.Base(path)] = cfg
	}
	return configs
}
