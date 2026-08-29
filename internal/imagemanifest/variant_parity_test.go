package imagemanifest

import (
	"path/filepath"
	"reflect"
	"testing"
)

// The two worker images are one baseline plus a Docker delta. Nothing compared
// them.
//
// It is not the silent drift it looks like: TestToolchainImageStageAudit binds
// each manifest's fingerprint to the image built from it, so no edit lands
// green. What that catches is the *edit*, not the *divergence*. A maintainer
// changing only the integration manifest is told to rebuild integration,
// rebuilds integration, re-records the audit -- and from then on the two
// baselines differ permanently with nothing looking at them again.
//
// So this compares the variants directly, and the delta is an allow-list rather
// than a judgement: a field that is neither compared nor listed fails, which is
// what makes it survive a new field being added.
func TestImageVariantsShareOneBaseline(t *testing.T) {
	t.Parallel()
	base, err := Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if err != nil {
		t.Fatalf("load base manifest: %v", err)
	}
	integration, err := Load(filepath.Join("..", "..", "config", "golden-image-integration.yaml"))
	if err != nil {
		t.Fatalf("load integration manifest: %v", err)
	}

	// Everything a job's behaviour depends on that is not the Docker capability.
	// A worker of either class runs the same operating system, the same official
	// runner and the same compiler cache; that is what makes them one fleet
	// rather than two.
	if !reflect.DeepEqual(base.Source, integration.Source) {
		t.Errorf("the two variants build from different Ubuntu sources:\n base:        %#v\n integration: %#v",
			base.Source, integration.Source)
	}
	if !reflect.DeepEqual(base.Runner, integration.Runner) {
		t.Errorf("the two variants bake different official runners:\n base:        %#v\n integration: %#v",
			base.Runner, integration.Runner)
	}
	if !reflect.DeepEqual(base.CompilerCache, integration.CompilerCache) {
		t.Errorf("the two variants bake different compiler caches:\n base:        %#v\n integration: %#v",
			base.CompilerCache, integration.CompilerCache)
	}
	if !reflect.DeepEqual(base.GoCacheSeed, integration.GoCacheSeed) {
		t.Errorf("the two variants bake different Go cache seeds:\n base:        %#v\n integration: %#v",
			base.GoCacheSeed, integration.GoCacheSeed)
	}
	if !reflect.DeepEqual(base.Toolchains, integration.Toolchains) {
		t.Errorf("the two variants bake different toolchains:\n base:        %#v\n integration: %#v",
			base.Toolchains, integration.Toolchains)
	}
	if base.SchemaVersion != integration.SchemaVersion {
		t.Errorf("the two variants are written to different schemas: %d and %d",
			base.SchemaVersion, integration.SchemaVersion)
	}

	// The image block differs only in the aliases, which name the artifact.
	if base.Image.OS != integration.Image.OS ||
		base.Image.Release != integration.Image.Release ||
		base.Image.Architecture != integration.Image.Architecture {
		t.Errorf("the two variants target different platforms:\n base:        %#v\n integration: %#v",
			base.Image, integration.Image)
	}
}

// The delta is declared, so a field that is neither compared above nor listed
// here fails. Without this, adding a field to Manifest would quietly leave it
// uncompared -- which is the shape of every defect this repository keeps finding.
func TestTheOnlyDifferenceIsTheDeclaredDelta(t *testing.T) {
	t.Parallel()
	compared := map[string]bool{
		"SchemaVersion": true,
		"Source":        true,
		"Runner":        true,
		"CompilerCache": true,
		"GoCacheSeed":   true,
		"Toolchains":    true,
	}
	// Image holds the artifact's own names; Guest holds the capability delta.
	// Both are compared field by field rather than wholesale, above and below.
	delta := map[string]string{
		"Image":        "aliases name the artifact, and the two artifacts are different images",
		"Guest":        "Docker and browser OS capability are the integration-image delta",
		"BrowserSmoke": "only the browser-capable image carries disposable launch-test bytes",
	}

	manifest := reflect.TypeOf(Manifest{})
	for index := 0; index < manifest.NumField(); index++ {
		name := manifest.Field(index).Name
		if compared[name] {
			continue
		}
		if _, listed := delta[name]; listed {
			continue
		}
		t.Errorf("Manifest field %q is neither compared between the variants nor listed as part of "+
			"the delta; decide which it is, because right now nothing would notice it diverging", name)
	}

	// And the Guest delta itself is bounded: the two variants may differ in
	// packages, their versions, the variant name, the action base and the disk
	// the builder needs -- and in nothing else.
	guest := reflect.TypeOf(Guest{})
	allowed := map[string]bool{
		"BuilderDiskGiB":  true,
		"Packages":        true,
		"PackageVersions": true,
		// The promised command surface follows the packages, and the packages
		// differ: integration adds docker and busybox, and its container form
		// adds Xvfb for browser work.
		"Provides": true,
		// Listed only so a new field cannot slip past this enumeration. The
		// variants must pin the SAME path binaries, which is a stronger claim
		// than "may differ" and is asserted by
		// TestVariantsPinTheSamePathBinaries.
		"PathBinaries":        true,
		"Variant":             true,
		"DockerActionBaseRef": true,
		"Browser":             true,
	}
	for index := 0; index < guest.NumField(); index++ {
		name := guest.Field(index).Name
		if !allowed[name] {
			t.Errorf("Guest field %q is not part of the declared Docker delta; if the variants may "+
				"differ in it, say so here and say why", name)
		}
	}
}
