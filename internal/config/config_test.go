package config

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryConfigurationIsValid(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	if len(cfg.Pools) != 10 {
		t.Fatalf("pool count = %d, want 10", len(cfg.Pools))
	}
	var untrusted *Pool
	for index := range cfg.Pools {
		if cfg.Pools[index].Name == "nddev-linux-untrusted" {
			untrusted = &cfg.Pools[index]
		}
	}
	if untrusted == nil || untrusted.Trust != "untrusted" || !untrusted.Capabilities.Docker ||
		untrusted.Capabilities.Credentials != "none" || untrusted.Capabilities.CacheWriteScope != "none" ||
		untrusted.Warm.TargetReady != 0 || untrusted.Warm.MaxReady != 0 {
		t.Fatalf("untrusted Docker class is not fail-closed: %#v", untrusted)
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Platform != "linux" ||
		cfg.Backends[0].Architecture != "amd64" || cfg.Backends[0].Implementation != "incus-container" ||
		!cfg.Backends[0].Capabilities.Docker {
		t.Fatalf("unexpected execution backends: %#v", cfg.Backends)
	}
	if cfg.Cache.ObjectStore.Implementation != "rustfs" {
		t.Fatalf("object store = %q, want rustfs", cfg.Cache.ObjectStore.Implementation)
	}
	fingerprint, err := cfg.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
}

func TestClusterConfigsShareTheCompletePublicHostDenySet(t *testing.T) {
	t.Parallel()
	want := []string{"203.0.113.10", "203.0.113.11", "203.0.113.12", "203.0.113.13", "203.0.113.14"}
	for _, filename := range []string{
		"example-runner-1.yaml", "example-runner-2.yaml", "example-runner-3.yaml",
		"example-runner-4.yaml", "example-services.yaml",
	} {
		cfg, err := Load(filepath.Join("..", "..", "config", filename))
		if err != nil {
			t.Fatalf("load %s: %v", filename, err)
		}
		if !slices.Equal(cfg.Incus.EstatePublicHostAddresses, want) {
			t.Errorf("%s estate public hosts = %v, want %v", filename, cfg.Incus.EstatePublicHostAddresses, want)
		}
	}
}

func TestDecodeRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	_, err := Decode(strings.NewReader("schema_version: 2\nunknown: true\n"))
	if err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected strict unknown-field error, got %v", err)
	}
}

func TestDecodeRejectsMultipleDocuments(t *testing.T) {
	t.Parallel()

	_, err := Decode(strings.NewReader("schema_version: 2\n---\nschema_version: 2\n"))
	if err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

func TestValidateAggregatesSecurityViolations(t *testing.T) {
	t.Parallel()

	cfg := Config{SchemaVersion: 2}
	err := cfg.Validate()
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected ValidationError, got %T: %v", err, err)
	}
	if len(validationErr.Issues) < 10 {
		t.Fatalf("only %d issues returned; validation should aggregate errors", len(validationErr.Issues))
	}
}

func TestReleasePoolCannotWriteCache(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	for index := range cfg.Pools {
		if cfg.Pools[index].Trust == "release" {
			cfg.Pools[index].Capabilities.CacheWriteScope = "trusted"
		}
	}
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "release pools cannot write shared caches") {
		t.Fatalf("expected release cache-write rejection, got %v", err)
	}
}

func TestFloatingControlPlaneVersionIsRejected(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	cfg.ControlPlane.RunnerVersion = "latest"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "must be an exact vMAJOR.MINOR.PATCH version") {
		t.Fatalf("expected floating-version rejection, got %v", err)
	}
}

func TestUnsupportedProviderInterfaceIsRejected(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	cfg.ControlPlane.ProviderInterface = "v0.1.1"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "control_plane.provider_interface: must be v0.1.0") {
		t.Fatalf("expected provider-interface rejection, got %v", err)
	}
}

func TestEveryContainerPoolKeepsWarmCapacityDisabled(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	for _, pool := range cfg.Pools {
		if pool.Warm.TargetReady != 0 || pool.Warm.MaxReady != 0 {
			t.Fatalf("container pool %q requests warm capacity: %#v", pool.Name, pool.Warm)
		}
	}
}

func TestNestedVirtualizationGuardrailIsRequired(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	cfg.Guardrails.DenyNestedVirtualization = false
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "guardrails.deny_nested_virtualization: must be true") {
		t.Fatalf("expected nested-virtualization rejection, got %v", err)
	}
}

func TestUnsafeIncusSettingsAreRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
		path   string
	}{
		{"non-loopback API", func(cfg *Config) { cfg.Incus.APIAddress = "0.0.0.0:8443" }, "incus.api_address"},
		{"wrong version", func(cfg *Config) { cfg.Incus.Version = "v6.1.0" }, "incus.version"},
		{"unbounded driver", func(cfg *Config) { cfg.Incus.StorageDriver = "dir" }, "incus.storage_driver"},
		{"overlapping bridge", func(cfg *Config) { cfg.Incus.NetworkCIDR = "10.10.10.2/24" }, "incus.network_cidr"},
		{"undersized project disk", func(cfg *Config) { cfg.Incus.ProjectDiskLimitGiB = 40 }, "must fit the largest pool disk request"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
			if err != nil {
				t.Fatalf("load repository config: %v", err)
			}
			test.mutate(&cfg)
			err = cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), test.path) {
				t.Fatalf("expected %q validation failure, got %v", test.path, err)
			}
		})
	}
}
