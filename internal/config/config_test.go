package config

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryConfigurationIsValid(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	if len(cfg.Pools) != 4 {
		t.Fatalf("pool count = %d, want 4", len(cfg.Pools))
	}
	if len(cfg.Backends) != 1 || cfg.Backends[0].Platform != "linux" ||
		cfg.Backends[0].Architecture != "amd64" || cfg.Backends[0].Implementation != "incus-vm" {
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

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
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

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
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

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	cfg.ControlPlane.ProviderInterface = "v0.1.1"
	err = cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "control_plane.provider_interface: must be v0.1.0") {
		t.Fatalf("expected provider-interface rejection, got %v", err)
	}
}

func TestUnregisteredWarmTargetWithinPoolLimitIsAccepted(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load repository config: %v", err)
	}
	// The first pool holds no warm capacity on this host, so raising the target
	// alone would exceed its ceiling. Raise both: the property under test is
	// that an unregistered warm target inside the pool limit is accepted.
	cfg.Pools[0].Warm.MaxReady = 1
	cfg.Pools[0].Warm.TargetReady = 1
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unregistered warm target was rejected: %v", err)
	}
}

func TestNestedVirtualizationGuardrailIsRequired(t *testing.T) {
	t.Parallel()

	cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
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
		{"overlapping bridge", func(cfg *Config) { cfg.Incus.NetworkCIDR = "10.10.10.1/24" }, "incus.network_cidr"},
		{"undersized project disk", func(cfg *Config) { cfg.Incus.ProjectDiskLimitGiB = 40 }, "must fit the largest pool disk request"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(filepath.Join("..", "..", "config", "server-gha-runner-1.yaml"))
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
