package garmbootstrap

import "testing"

// The fast class is what almost every job in this estate actually needs: of the
// 46 reusable workflows the org publishes, 44 want nothing but a shell. It was
// declared in every host configuration and could not be registered, so no job
// could ever reach it -- the same shape as the pool once declaring an egress
// policy no bridge implemented.
func TestFastScaleSetResolves(t *testing.T) {
	spec, err := resolveScaleSetSpec(FastScaleSetName)
	if err != nil {
		t.Fatalf("the fast class could not be registered: %v", err)
	}
	if spec.Name != "nddev-linux-fast" || spec.Flavor != "nddev-linux-fast" {
		t.Fatalf("fast spec = %+v", spec)
	}
	// It now uses the promoted unprivileged container image; integration and
	// release retain VM images as the explicit compatibility fallback.
	if spec.Image != ContainerCanaryImage {
		t.Fatalf("fast image = %q, want the promoted container image", spec.Image)
	}
}

// The set stays closed. A name outside it is a typo that would otherwise
// create a scale set GitHub keeps and nothing on the host serves.
func TestScaleSetNamesRemainClosed(t *testing.T) {
	for _, name := range []string{"nddev-linux-release-2", "nddev-linux-fast-2", "ubuntu-latest", "nddev"} {
		if _, err := resolveScaleSetSpec(name); err == nil {
			t.Fatalf("scale set %q was accepted", name)
		}
	}
	for _, name := range []string{DefaultScaleSetName, IntegrationScaleSetName, FastScaleSetName, UntrustedScaleSetName, ReleaseScaleSetName, ContainerCanaryScaleSetName} {
		if _, err := resolveScaleSetSpec(name); err != nil {
			t.Fatalf("published class %q was refused: %v", name, err)
		}
	}
}

func TestUntrustedScaleSetUsesDockerImageAndDistinctFlavor(t *testing.T) {
	spec, err := resolveScaleSetSpec(UntrustedScaleSetName)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != IntegrationImage || spec.Flavor != UntrustedFlavor {
		t.Fatalf("untrusted spec=%+v", spec)
	}
}

func TestReleaseScaleSetUsesStandardVMImageAndReleaseFlavor(t *testing.T) {
	spec, err := resolveScaleSetSpec(ReleaseScaleSetName)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Image != ReleaseImage || spec.Flavor != ReleaseFlavor {
		t.Fatalf("release spec=%+v", spec)
	}
}
