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
	// release use their capability-specific container images.
	if spec.Image != ContainerCanaryImage {
		t.Fatalf("fast image = %q, want the promoted container image", spec.Image)
	}
	for _, class := range PublishedScaleSets() {
		if class.Name == FastScaleSetName && (class.Credentials != "repository" || class.CacheWriteScope != "trusted") {
			t.Fatalf("fast cache claim contract = %+v", class)
		}
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

func TestAndroidScaleSetsPublishEightGiBHardLimits(t *testing.T) {
	classes := make(map[string]ScaleSetClass, len(PublishedScaleSets()))
	for _, class := range PublishedScaleSets() {
		classes[class.Name] = class
	}
	release := classes[ReleaseScaleSetName]
	if release.Image != ReleaseImage || release.Flavor != ReleaseFlavor || release.MemoryMiB != 8192 {
		t.Fatalf("release class=%+v", release)
	}
	priority := classes[PriorityIntegrationScaleSetName]
	if priority.Image != PriorityIntegrationImage || priority.Flavor != PriorityIntegrationFlavor || priority.MemoryMiB != 8192 {
		t.Fatalf("priority integration class=%+v", priority)
	}
}
