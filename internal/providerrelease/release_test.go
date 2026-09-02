package providerrelease

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryManifestLoads(t *testing.T) {
	t.Parallel()
	manifest, err := Load(filepath.Join("../..", DefaultManifestPath))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !IsRelease(manifest.DerivativeVersion) {
		t.Fatalf("declared version %q is not a release", manifest.DerivativeVersion)
	}
}

func TestValidateRejectsAnIdentityNothingCouldRelyOn(t *testing.T) {
	t.Parallel()
	base, err := Load(filepath.Join("../..", DefaultManifestPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*Manifest)
		message string
	}{
		{"unknown schema", func(m *Manifest) { m.SchemaVersion = 2 }, "schema_version"},
		{"foreign artifact", func(m *Manifest) { m.Artifact = "garm" }, "artifact"},
		// An upstream-shaped version would let a host config name upstream's own
		// release and be admitted by a binary several NDDev revisions past it.
		{"upstream-shaped version", func(m *Manifest) { m.DerivativeVersion = "v0.1.5" }, "vMAJOR.MINOR.PATCH-nddev.N"},
		{"development sentinel declared", func(m *Manifest) { m.DerivativeVersion = DevelopmentVersion }, "vMAJOR.MINOR.PATCH-nddev.N"},
		{"version does not derive from upstream", func(m *Manifest) { m.DerivativeVersion = "v0.9.9-nddev.1" }, "does not derive from"},
		{"interface carries an nddev suffix", func(m *Manifest) { m.InterfaceVersion = "v0.1.0-nddev.1" }, "interface_version"},
		{"repository with .git", func(m *Manifest) { m.Upstream.Repository += ".git" }, "without a .git suffix"},
		{"short commit", func(m *Manifest) { m.Upstream.Commit = "f3ae319" }, "not a full commit id"},
		{"asset digest is not one", func(m *Manifest) { m.Upstream.ReleaseAssetSHA256 = "none" }, "is not a sha256"},
		{"sdk is not a version", func(m *Manifest) { m.Runtime.IncusSDKVersion = "latest" }, "incus_sdk_version"},
		{"source commit is short", func(m *Manifest) { m.Build.SourceCommit = "bb11bfb" }, "build.source_commit"},
		{"binary digest is absent", func(m *Manifest) { m.Build.BinarySHA256 = "" }, "build.binary_sha256"},
		{"CGO enabled", func(m *Manifest) { m.Build.CGOEnabled = true }, "CGO disabled"},
		{"implicit VCS metadata", func(m *Manifest) { m.Build.VCSMetadata = "embedded" }, "build: release requires"},
		{"one rebuild", func(m *Manifest) { m.Build.ReproducibleRebuilds = 1 }, "build: release requires"},
		{"another toolchain", func(m *Manifest) { m.Build.GoVersion = "go1.26.6" }, "build.go_version: release names go1.26.6 but this toolchain is " + runtime.Version()},
	} {
		mutated := base
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil {
			t.Errorf("%s: accepted", testCase.name)
			continue
		}
		if !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: error %q does not mention %q", testCase.name, err, testCase.message)
		}
	}
}

func TestDecodeRejectsUnknownAndMultipleDocuments(t *testing.T) {
	t.Parallel()
	if _, err := Decode(strings.NewReader("schema_version: 1\nunknown: true\n")); err == nil ||
		!strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("unknown field accepted: %v", err)
	}
}
