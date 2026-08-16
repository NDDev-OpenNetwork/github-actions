package deploycontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/garmproviderincus/provider"
	"github.com/NDDev-OpenNetwork/github-actions/internal/providerrelease"
)

func loadProviderRelease(t *testing.T) providerrelease.Manifest {
	t.Helper()
	manifest, err := providerrelease.Load(filepath.Join("../..", providerrelease.DefaultManifestPath))
	if err != nil {
		t.Fatalf("load provider release manifest: %v", err)
	}
	return manifest
}

// The GARM derivative has had this binding since it existed; the provider never
// did, and two hosts drifted apart under one version string as a result (#263).
// This is the same binding for the other derivative.
func TestEveryHostPinsTheProviderThisTreeDeclares(t *testing.T) {
	t.Parallel()
	manifest := loadProviderRelease(t)
	for name, platform := range declaredHostConfigs(t) {
		if platform.ControlPlane.ProviderVersion != manifest.DerivativeVersion {
			t.Errorf("%s pins provider %q, manifest declares %q", name,
				platform.ControlPlane.ProviderVersion, manifest.DerivativeVersion)
		}
		if platform.ControlPlane.ProviderInterface != manifest.InterfaceVersion {
			t.Errorf("%s pins interface %q, manifest declares %q", name,
				platform.ControlPlane.ProviderInterface, manifest.InterfaceVersion)
		}
	}
}

// The Incus client library decides which Incus API the fleet can speak, so it is
// part of the release identity. It was a bare constant in the provider package
// and a sentence in the upstream baseline; this makes the manifest the statement
// and the constant its consumer.
func TestProviderReportsTheIncusSDKTheManifestDeclares(t *testing.T) {
	t.Parallel()
	manifest := loadProviderRelease(t)
	if provider.IncusSDKVersion != manifest.Runtime.IncusSDKVersion {
		t.Fatalf("provider compiles against Incus SDK %q, manifest declares %q",
			provider.IncusSDKVersion, manifest.Runtime.IncusSDKVersion)
	}
}

// The build stamp has to come from the manifest, or the binary can once again
// report a version nothing chose. Reading the Makefile is the only way to hold
// that from a test, because the alternative is trusting that whoever edits the
// ldflags remembers.
func TestBuildStampIsDerivedFromTheManifest(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("../..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	makefile := string(raw)
	if !strings.Contains(makefile, "PROVIDER_VERSION = $(shell go run ./cmd/gha-fleet provider-release --field derivative_version)") {
		t.Fatal("Makefile does not derive PROVIDER_VERSION from the provider release manifest")
	}
	manifest := loadProviderRelease(t)
	if strings.Contains(makefile, manifest.DerivativeVersion) {
		t.Fatalf("Makefile still writes %q as a literal; that is the second authority this manifest replaces", manifest.DerivativeVersion)
	}
}

// An unstamped build must be unable to claim a release. Before this, main.go
// defaulted to the release version, so `go build ./cmd/...` produced a binary
// that called itself v0.1.5-nddev.30 and would admit a policy pinning it.
func TestAnUnstampedProviderCannotClaimARelease(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile(filepath.Join("../..", "cmd", "garm-provider-incus-nddev", "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "version      = providerrelease.DevelopmentVersion") {
		t.Fatal("the provider's version fallback is not the development sentinel")
	}
	if providerrelease.IsRelease(providerrelease.DevelopmentVersion) {
		t.Fatalf("%q is accepted as a release, so an unstamped build would admit a platform policy",
			providerrelease.DevelopmentVersion)
	}
	manifest := loadProviderRelease(t)
	if !providerrelease.IsRelease(manifest.DerivativeVersion) {
		t.Fatalf("%q is not accepted as a release, so a correctly stamped build would refuse every policy",
			manifest.DerivativeVersion)
	}
}
