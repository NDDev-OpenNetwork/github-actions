package provider

import (
	"path/filepath"
	"sort"
	"testing"

	platformconfig "github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

// The provider refuses to run against a platform policy written for a host it
// was not built for. That check is only meaningful while its allowlist and the
// repository's declared host configurations describe the same fleet: an entry
// with no config is an unreviewed host, and a config with no entry is a host
// whose provider would refuse to start after it was already provisioned. Assert
// both directions so adding either half alone fails here rather than on a box.
func TestSupportedPlatformHostsMatchDeclaredConfigs(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	matches, err := filepath.Glob(filepath.Join(root, "config", "server-*.yaml"))
	if err != nil {
		t.Fatalf("glob platform configs: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no config/server-*.yaml found; the glob no longer locates the repository root")
	}

	declared := map[string]string{}
	for _, path := range matches {
		cfg, err := platformconfig.Load(path)
		if err != nil {
			t.Fatalf("load %s: %v", filepath.Base(path), err)
		}
		host := cfg.Platform.Host
		if host == "" {
			t.Fatalf("%s declares no platform.host", filepath.Base(path))
		}
		if other, clash := declared[host]; clash {
			t.Fatalf("%s and %s both declare platform.host %q", other, filepath.Base(path), host)
		}
		declared[host] = filepath.Base(path)
	}

	for host := range supportedPlatformHosts {
		if _, ok := declared[host]; !ok {
			t.Errorf("supportedPlatformHosts allows %q, but no config/server-*.yaml declares it", host)
		}
	}
	for host, file := range declared {
		if _, ok := supportedPlatformHosts[host]; !ok {
			t.Errorf("config/%s declares host %q, which supportedPlatformHosts rejects", file, host)
		}
	}

	if t.Failed() {
		t.Logf("declared hosts: %s", sortedKeys(declared))
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
