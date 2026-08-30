package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionBackendRejectsUnsupportedPlatformTuple(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Backend)
		want   string
	}{
		{"macOS before backend approval", func(b *Backend) { b.Platform = "macos" }, "must be linux"},
		{"arm before backend approval", func(b *Backend) { b.Architecture = "arm64" }, "must be amd64"},
		{"unknown implementation", func(b *Backend) { b.Implementation = "virtualization-framework" }, "must be incus-vm or incus-container"},
		{"foreign failure domain", func(b *Backend) { b.FailureDomain = "example-runner-2" }, "must match platform.host"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&cfg.Backends[0])
			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}

// Warm capacity is gated by the backend declaring it, the same shape as Docker,
// and not by the instance type.
//
// It used to be refused on `incus-container` unconditionally, with no flag any
// evidence could set. That was a prohibition wearing a precondition's words, and
// it stood in front of a feature that could not have run anyway: the provider
// created virtual machines and refused anything else, and the image asserted the
// readiness agent was absent. Both are fixed, so the question the validator asks
// is now the one a backend can answer about itself.
func TestWarmCapacityRequiresABackendThatDeclaresIt(t *testing.T) {
	cfg, err := Load("../../config/example-runner-1.yaml")
	if err != nil {
		t.Fatal(err)
	}
	container := &cfg.Backends[0]
	canary := &cfg.Pools[0]
	if err := cfg.Validate(); err != nil {
		t.Fatalf("cold non-Docker container backend was refused: %v", err)
	}

	canary.Warm.MaxReady = 1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "warm capacity requires a backend that declares it") {
		t.Fatalf("warm capacity was accepted from a backend that does not declare it: %v", err)
	}

	// The half the old guard could never reach: declaring the capability makes
	// the same configuration valid. Without this the test would pass for a
	// validator that refuses warm capacity always, which is what it did before.
	container.Capabilities.Warm = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("warm capacity was refused from a backend that declares it: %v", err)
	}

	container.Capabilities.Warm = false
	canary.Warm.MaxReady = 0
	container.Capabilities.Docker = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Docker-capable container backend was refused after isolation proof: %v", err)
	}
}

func TestPoolMustReferenceDeclaredExecutionBackend(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Pools[0].Backend = "missing-backend"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must reference a declared execution backend") {
		t.Fatalf("expected missing backend rejection, got %v", err)
	}
}

func TestPoolCapabilitiesMustFitExecutionBackend(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Backends[0].Capabilities.Docker = false
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires Docker support") {
		t.Fatalf("expected backend capability rejection, got %v", err)
	}
}

func TestExecutionBackendIdentityMustBeUnique(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate := cfg.Backends[0]
	duplicate.Name = "duplicate-linux-backend"
	cfg.Backends = append(cfg.Backends, duplicate)
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "must not duplicate another") {
		t.Fatalf("expected duplicate backend rejection, got %v", err)
	}
}

func TestLegacyPlatformSchemaFailsClosed(t *testing.T) {
	t.Parallel()
	cfg, err := Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.SchemaVersion = 1
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "schema_version: must be 2") {
		t.Fatalf("expected legacy schema rejection, got %v", err)
	}
}
