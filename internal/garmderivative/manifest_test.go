package garmderivative

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const repositoryRoot = "../.."

func manifestPath() string { return filepath.Join(repositoryRoot, DefaultManifestPath) }
func scriptPath() string   { return filepath.Join(repositoryRoot, DefaultScriptPath) }

func loadRepositoryManifest(t *testing.T) Manifest {
	t.Helper()
	manifest, err := Load(manifestPath())
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return manifest
}

// leafFields walks the manifest type the way the manifest is written: a struct
// contributes its members, a slice of structs contributes its members once
// under a `[]` segment because every element has the same shape, and anything
// else is a leaf. The result is the set of facts the manifest states.
func leafFields(t reflect.Type, prefix string) []string {
	var fields []string
	for index := 0; index < t.NumField(); index++ {
		field := t.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "" {
			continue
		}
		path := prefix + name
		fieldType := field.Type
		switch {
		case fieldType.Kind() == reflect.Struct:
			fields = append(fields, leafFields(fieldType, path+".")...)
		case fieldType.Kind() == reflect.Slice && fieldType.Elem().Kind() == reflect.Struct:
			fields = append(fields, leafFields(fieldType.Elem(), path+"[].")...)
		default:
			fields = append(fields, path)
		}
	}
	return fields
}

// A field nobody decided about is how the fifth patch digest went unchecked:
// the contract test listed four digests and stopped, so the manifest could
// declare a fifth and the build could use a different one. Requiring every leaf
// to be dispositioned makes adding a field a decision rather than an omission.
func TestEveryManifestFieldIsDispositioned(t *testing.T) {
	t.Parallel()
	declared := leafFields(reflect.TypeOf(Manifest{}), "")
	dispositions := FieldDispositions()

	for _, field := range declared {
		disposition, exists := dispositions[field]
		if !exists {
			t.Errorf("manifest field %q has no disposition: say which shell assignment it becomes, or why the build does not read it", field)
			continue
		}
		if disposition.Rendered() == (disposition.Reason != "") {
			t.Errorf("manifest field %q must be exactly one of rendered or not-a-build-input, got %#v", field, disposition)
		}
	}
	for field := range dispositions {
		if !slices.Contains(declared, field) {
			t.Errorf("disposition names %q, which the manifest does not declare", field)
		}
	}
}

// The region is generated, so the only thing that keeps it honest is that
// regenerating it changes nothing. This is the test that makes the manifest the
// build's input rather than its description.
func TestGeneratedRegionIsCurrent(t *testing.T) {
	t.Parallel()
	manifest := loadRepositoryManifest(t)
	current, err := os.ReadFile(scriptPath())
	if err != nil {
		t.Fatal(err)
	}
	rendered, err := manifest.SpliceRegion(string(current))
	if err != nil {
		t.Fatalf("splice region: %v", err)
	}
	if rendered != string(current) {
		t.Fatalf("%s is stale; run `make garm-derivative-script`", DefaultScriptPath)
	}
}

// Rendering a field is only half of it: the assignment has to exist under the
// name the disposition promises, or the disposition is describing a mapping
// that is not there.
func TestEveryRenderedFieldIsAssignedInTheRegion(t *testing.T) {
	t.Parallel()
	manifest := loadRepositoryManifest(t)
	region, err := manifest.RenderRegion()
	if err != nil {
		t.Fatal(err)
	}
	for field, disposition := range FieldDispositions() {
		if !disposition.Rendered() {
			continue
		}
		if !regexp.MustCompile(`(?m)^readonly ` + regexp.QuoteMeta(disposition.ShellName) + `=`).MatchString(region) {
			t.Errorf("field %q claims to render as %q, which the region does not assign", field, disposition.ShellName)
		}
	}
}

// The defect this package exists for was not that the script held the wrong
// values -- it held the right ones -- but that it held its own copies, so
// nothing made them follow the manifest. A manifest value appearing anywhere
// outside the generated region is that defect coming back.
func TestNoManifestValueIsRestatedOutsideTheRegion(t *testing.T) {
	t.Parallel()
	manifest := loadRepositoryManifest(t)
	raw, err := os.ReadFile(scriptPath())
	if err != nil {
		t.Fatal(err)
	}
	script := string(raw)
	begin := strings.Index(script, regionBegin)
	end := strings.Index(script, regionEnd)
	if begin < 0 || end < begin {
		t.Fatal("build script has no generated region")
	}
	outside := script[:begin] + script[end+len(regionEnd):]

	values := map[string]string{
		"derivative_version":    manifest.DerivativeVersion,
		"upstream.commit":       manifest.Upstream.Commit,
		"upstream.repository":   manifest.Upstream.Repository,
		"build.container_image": manifest.Build.ContainerImage,
		"build.go_version":      manifest.Build.GoVersion,
		"build.binary_sha256":   manifest.Build.BinarySHA256,
		"build.target_os":       manifest.Build.TargetOS,
		"build.target_arch":     manifest.Build.TargetArch,
	}
	for index, patch := range manifest.Patches {
		values[fmt.Sprintf("patches[%d].sha256", index)] = patch.SHA256
		values[fmt.Sprintf("patches[%d].path", index)] = patch.Path
	}
	for index, overlay := range manifest.Overlays {
		values[fmt.Sprintf("overlays[%d].sha256", index)] = overlay.SHA256
		values[fmt.Sprintf("overlays[%d].path", index)] = overlay.Path
	}
	for field, value := range values {
		// target_os and target_arch are short enough to occur as ordinary words;
		// only a quoted literal would be a restatement.
		needle := value
		if field == "build.target_os" || field == "build.target_arch" {
			needle = `"` + value + `"`
		}
		if strings.Contains(outside, needle) {
			t.Errorf("%s (%q) is restated outside the generated region; the build would stop following the manifest", field, value)
		}
	}
}

// Load rejecting a manifest is the first gate the build passes through, so the
// rejections have to be real rather than assumed.
func TestValidateRejectsWhatTheBuildCouldNotActOn(t *testing.T) {
	t.Parallel()
	base := loadRepositoryManifest(t)
	for _, testCase := range []struct {
		name    string
		mutate  func(*Manifest)
		message string
	}{
		{"unknown schema", func(m *Manifest) { m.SchemaVersion = 2 }, "schema_version"},
		{"foreign artifact", func(m *Manifest) { m.Artifact = "incus" }, "artifact"},
		{"floating container tag", func(m *Manifest) { m.Build.ContainerImage = "docker.io/library/golang:1.26" }, "pinned by digest"},
		{"repository with .git", func(m *Manifest) { m.Upstream.Repository += ".git" }, "without a .git suffix"},
		{"short commit", func(m *Manifest) { m.Upstream.Commit = "1546384" }, "not a full commit id"},
		{"patch outside its tree", func(m *Manifest) { m.Patches[0].Path = "third_party/other/x.patch" }, "must live under"},
		{"patch traversal", func(m *Manifest) { m.Patches[0].Path = "third_party/garm/patches/../../../etc/passwd" }, "clean in-tree path"},
		{"patch digest is not one", func(m *Manifest) { m.Patches[4].SHA256 = "not-a-digest" }, "is not a sha256"},
		{"patch with no stated reason", func(m *Manifest) { m.Patches[4].Purpose = "  " }, "cannot be re-approved"},
		{"overlay outside the overlay root", func(m *Manifest) { m.Overlays[0].Path = "internal/x.go" }, "must live under"},
		{"network during build", func(m *Manifest) { m.Build.NetworkDuringTestAndBuild = "default" }, "network_during_test_and_build"},
		{"module mode drifts", func(m *Manifest) { m.Build.ModuleMode = "mod" }, "module_mode"},
		{"one rebuild proves nothing", func(m *Manifest) { m.Build.ReproducibleRebuilds = 1 }, "cannot demonstrate reproducibility"},
		{"tag list smuggled into one tag", func(m *Manifest) { m.Build.Tags = []string{"netgo,osusergo"} }, "must be one tag"},
		{"glibc is not a version", func(m *Manifest) { m.Build.MaximumRequiredGLIBC = "latest" }, "maximum_required_glibc"},
		{"go version is a series", func(m *Manifest) { m.Build.GoVersion = "go1.26" }, "go_version"},
		{"binary digest is not one", func(m *Manifest) { m.Build.BinarySHA256 = "" }, "is not a sha256"},
		{"shell metacharacter in a rendered value", func(m *Manifest) { m.DerivativeVersion = `v1"; rm -rf /; #` }, "would not survive"},
	} {
		mutated := base
		mutated.Patches = slices.Clone(base.Patches)
		mutated.Overlays = slices.Clone(base.Overlays)
		mutated.Build.Tags = slices.Clone(base.Build.Tags)
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
	raw, err := os.ReadFile(manifestPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(strings.NewReader(string(raw) + "---\nschema_version: 1\n")); err == nil ||
		!strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("second document accepted: %v", err)
	}
}

// Every declared overlay has to land somewhere inside the upstream tree, and the
// place is derived from the path rather than declared beside it.
func TestOverlayInstallTargetIsDerivedFromItsPath(t *testing.T) {
	t.Parallel()
	manifest := loadRepositoryManifest(t)
	for _, overlay := range manifest.Overlays {
		target := overlay.InstallTarget()
		if target == overlay.Path || strings.HasPrefix(target, "/") || strings.Contains(target, "..") {
			t.Fatalf("overlay %q resolves to install target %q", overlay.Path, target)
		}
	}
}
