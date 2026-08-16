package garmderivative

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

var (
	hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// A shell-safe scalar. Every rendered value is written into a double-quoted
	// assignment in a generated shell region, so a value carrying a quote, a
	// backslash, a dollar or a newline would stop being a value and start being
	// code. Nothing legitimate in this manifest needs those characters.
	shellSafe    = regexp.MustCompile(`^[A-Za-z0-9@:/._+-]+$`)
	goRelease    = regexp.MustCompile(`^go1\.[0-9]+\.[0-9]+$`)
	glibcVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)
)

// Validate rejects a manifest the build could not act on. It checks shape, not
// policy: that a digest is a digest and a path stays inside the tree it names.
// Which exact digest is correct is a contract assertion and lives in
// internal/deploycontract, which is where a change to one is reviewed.
func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: %d is not a schema this reader speaks", m.SchemaVersion)
	}
	if m.Artifact != "garm" {
		return fmt.Errorf("artifact: %q, want \"garm\"", m.Artifact)
	}
	if err := validateScalar("derivative_version", m.DerivativeVersion); err != nil {
		return err
	}
	if !strings.HasPrefix(m.DerivativeVersion, "v") {
		return fmt.Errorf("derivative_version: %q must be a v-prefixed version", m.DerivativeVersion)
	}

	if err := validateScalar("upstream.repository", m.Upstream.Repository); err != nil {
		return err
	}
	if !strings.HasPrefix(m.Upstream.Repository, "https://") {
		return fmt.Errorf("upstream.repository: %q must be an https URL", m.Upstream.Repository)
	}
	// The build fetches a commit, so a repository URL carrying a .git suffix and
	// one not carrying it would fetch identically and compare unequally. Pin the
	// canonical form so the manifest and the script cannot differ on a detail
	// that changes nothing.
	if strings.HasSuffix(m.Upstream.Repository, ".git") {
		return fmt.Errorf("upstream.repository: %q must be the canonical URL without a .git suffix", m.Upstream.Repository)
	}
	if err := validateScalar("upstream.release", m.Upstream.Release); err != nil {
		return err
	}
	if !hex40.MatchString(m.Upstream.Commit) {
		return fmt.Errorf("upstream.commit: %q is not a full commit id", m.Upstream.Commit)
	}
	if !hex64.MatchString(m.Upstream.ReleaseAssetSHA256) {
		return fmt.Errorf("upstream.release_asset_sha256: %q is not a sha256", m.Upstream.ReleaseAssetSHA256)
	}

	if len(m.Patches) == 0 {
		return fmt.Errorf("patches: a derivative with no patch is not a derivative")
	}
	for index, patch := range m.Patches {
		if err := validateSource(fmt.Sprintf("patches[%d]", index), patch, "third_party/garm/patches/"); err != nil {
			return err
		}
		if path.Ext(patch.Path) != ".patch" {
			return fmt.Errorf("patches[%d].path: %q must name a .patch file", index, patch.Path)
		}
	}
	if len(m.Overlays) == 0 {
		return fmt.Errorf("overlays: none declared")
	}
	for index, overlay := range m.Overlays {
		if err := validateSource(fmt.Sprintf("overlays[%d]", index), overlay, OverlayPrefix); err != nil {
			return err
		}
	}

	return m.Build.validate()
}

func (b Build) validate() error {
	if err := validateScalar("build.container_image", b.ContainerImage); err != nil {
		return err
	}
	// Only a digest pins an image. A tag is a moving target and would make the
	// build reproducible only until somebody pushed over it.
	if !strings.Contains(b.ContainerImage, "@sha256:") {
		return fmt.Errorf("build.container_image: %q must be pinned by digest", b.ContainerImage)
	}
	if err := validateScalar("build.go_version", b.GoVersion); err != nil {
		return err
	}
	// `go env GOVERSION` inside the container reports an exact release, and the
	// build compares against this string, so a series like go1.26 would never
	// match and the assertion would be unfalsifiable rather than satisfied.
	if !goRelease.MatchString(b.GoVersion) {
		return fmt.Errorf("build.go_version: %q must be an exact go1.MINOR.PATCH release", b.GoVersion)
	}
	if b.TargetOS == "" || b.TargetArch == "" {
		return fmt.Errorf("build: target_os and target_arch must both be named")
	}
	if err := validateScalar("build.target_os", b.TargetOS); err != nil {
		return err
	}
	if err := validateScalar("build.target_arch", b.TargetArch); err != nil {
		return err
	}
	// The derivative is built and tested with no network so that a dependency
	// cannot be substituted between review and build. Any other value would have
	// to explain itself, and none does yet.
	if b.NetworkDuringTestAndBuild != "none" {
		return fmt.Errorf("build.network_during_test_and_build: %q, want \"none\"", b.NetworkDuringTestAndBuild)
	}
	if b.ModuleMode != "vendor" {
		return fmt.Errorf("build.module_mode: %q, want \"vendor\"", b.ModuleMode)
	}
	if len(b.Tags) == 0 {
		return fmt.Errorf("build.tags: none declared")
	}
	for index, tag := range b.Tags {
		// The rendered value joins the list with commas, so a comma inside a tag
		// would silently become two tags. Checked before the general scalar rule
		// so the error says what is actually wrong.
		if strings.Contains(tag, ",") {
			return fmt.Errorf("build.tags[%d]: %q must be one tag, not a list", index, tag)
		}
		if err := validateScalar(fmt.Sprintf("build.tags[%d]", index), tag); err != nil {
			return err
		}
	}
	// One build proves nothing about reproducibility; the comparison needs at
	// least two, and the script compares each against the first.
	if b.ReproducibleRebuilds < 2 {
		return fmt.Errorf("build.reproducible_rebuilds: %d cannot demonstrate reproducibility", b.ReproducibleRebuilds)
	}
	if err := validateScalar("build.maximum_required_glibc", b.MaximumRequiredGLIBC); err != nil {
		return err
	}
	if !glibcVersion.MatchString(b.MaximumRequiredGLIBC) {
		return fmt.Errorf("build.maximum_required_glibc: %q must be MAJOR.MINOR", b.MaximumRequiredGLIBC)
	}
	if !hex64.MatchString(b.BinarySHA256) {
		return fmt.Errorf("build.binary_sha256: %q is not a sha256", b.BinarySHA256)
	}
	return nil
}

func validateSource(field string, source Source, wantPrefix string) error {
	if !strings.HasPrefix(source.Path, wantPrefix) {
		return fmt.Errorf("%s.path: %q must live under %s", field, source.Path, wantPrefix)
	}
	if path.Clean(source.Path) != source.Path || strings.Contains(source.Path, "..") {
		return fmt.Errorf("%s.path: %q must be a clean in-tree path", field, source.Path)
	}
	if err := validateScalar(field+".path", source.Path); err != nil {
		return err
	}
	if !hex64.MatchString(source.SHA256) {
		return fmt.Errorf("%s.sha256: %q is not a sha256", field, source.SHA256)
	}
	if strings.TrimSpace(source.Purpose) == "" {
		return fmt.Errorf("%s.purpose: a digest with no stated reason cannot be re-approved", field)
	}
	return nil
}

func validateScalar(field, value string) error {
	if value == "" {
		return fmt.Errorf("%s: must not be empty", field)
	}
	if !shellSafe.MatchString(value) {
		return fmt.Errorf("%s: %q carries a character that would not survive being written into a shell assignment", field, value)
	}
	return nil
}

// InstallTarget is where an overlay replaces a file in the checked-out upstream
// tree. Validate has already held the prefix, so this is a strip and not a guess.
func (s Source) InstallTarget() string {
	return strings.TrimPrefix(s.Path, OverlayPrefix)
}
