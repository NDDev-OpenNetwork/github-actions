package provider

import (
	"path/filepath"
	"strings"
	"testing"

	providerconfig "github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
)

// The version check used to compare the platform policy against a literal, so a
// binary could disagree with the policy it admitted and nothing would say so.
// gha-runner-1 and gha-runner-2 ran binaries built from ad8efaa and cae2d18 --
// a tenancy enforcement change apart -- and both reported v0.1.5-nddev.30 (#263).
//
// These exercise the constructor directly, because that is the only place the
// binary's identity meets the policy's.
func TestAdmissionRefusesAPolicyPinningADifferentProvider(t *testing.T) {
	useRuntimeHostname(t, "example-runner-1")
	original := Version
	t.Cleanup(func() { Version = original })

	cfg := &providerconfig.Incus{
		PlatformConfigFile: filepath.Join("..", "..", "..", "config", "example-runner-1.yaml"),
	}

	// A stamped binary whose version is not the one the policy pins.
	Version = "v0.1.5-nddev.29"
	_, err := newNDDevAdmission(cfg, "controller")
	if err == nil {
		t.Fatal("a binary one revision behind the policy was admitted")
	}
	if !strings.Contains(err.Error(), "v0.1.5-nddev.29") || !strings.Contains(err.Error(), "but this binary is") {
		t.Fatalf("error does not name the disagreement: %v", err)
	}
}

// An unstamped build must refuse every policy rather than claim the release it
// was built beside. Before this, main.go's fallback was the release version, so
// `go build ./cmd/garm-provider-incus-nddev` produced a binary that would admit
// a policy pinning it.
func TestAdmissionRefusesAnUnstampedBuild(t *testing.T) {
	useRuntimeHostname(t, "example-runner-1")
	original := Version
	t.Cleanup(func() { Version = original })

	cfg := &providerconfig.Incus{
		PlatformConfigFile: filepath.Join("..", "..", "..", "config", "example-runner-1.yaml"),
	}

	Version = providerrelease.DevelopmentVersion
	_, err := newNDDevAdmission(cfg, "controller")
	if err == nil {
		t.Fatal("a development build admitted a production platform policy")
	}
	if !strings.Contains(err.Error(), "not a stamped release") {
		t.Fatalf("error does not say why it refused: %v", err)
	}
}

// The counter-check: a binary stamped with the version the policy pins gets past
// the identity gate. Without this the two tests above would pass on a check that
// refuses everything.
func TestAdmissionAcceptsTheProviderThePolicyPins(t *testing.T) {
	useRuntimeHostname(t, "example-runner-1")
	original := Version
	t.Cleanup(func() { Version = original })

	manifest, err := providerrelease.Load(filepath.Join("..", "..", "..", providerrelease.DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &providerconfig.Incus{
		PlatformConfigFile: filepath.Join("..", "..", "..", "config", "example-runner-1.yaml"),
	}

	Version = manifest.DerivativeVersion
	if _, err := newNDDevAdmission(cfg, "controller"); err != nil {
		if strings.Contains(err.Error(), "but this binary is") || strings.Contains(err.Error(), "not a stamped release") {
			t.Fatalf("the identity gate refused the version the policy pins: %v", err)
		}
		// Anything else is a later stage of the constructor and not what this
		// test is about; the identity gate is what had to pass.
	}
}

func useRuntimeHostname(t *testing.T, hostname string) {
	t.Helper()
	original := runtimeHostname
	runtimeHostname = func() (string, error) { return hostname, nil }
	t.Cleanup(func() { runtimeHostname = original })
}
