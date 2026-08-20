// Package providerrelease is the typed reading of config/provider-derivative.yaml,
// the one statement of which Incus provider this tree builds and admits.
//
// It exists because the provider version was written down four times and bound
// to nothing, so two production hosts ran different binaries that both reported
// v0.1.5-nddev.30 -- one built from ad8efaa, the other from cae2d18, which is a
// tenancy enforcement change. Issue #263 records the observation.
//
// The GARM derivative already had this shape. This is the same one, applied to
// the other in-tree derivative.
package providerrelease

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultManifestPath is where the manifest lives relative to the repository root.
const DefaultManifestPath = "config/provider-derivative.yaml"

const maxManifestBytes = 64 * 1024

// Manifest is config/provider-derivative.yaml.
type Manifest struct {
	SchemaVersion     int      `json:"schema_version" yaml:"schema_version"`
	Artifact          string   `json:"artifact" yaml:"artifact"`
	DerivativeVersion string   `json:"derivative_version" yaml:"derivative_version"`
	InterfaceVersion  string   `json:"interface_version" yaml:"interface_version"`
	Upstream          Upstream `json:"upstream" yaml:"upstream"`
	Runtime           Runtime  `json:"runtime" yaml:"runtime"`
	Build             Build    `json:"build" yaml:"build"`
}

type Upstream struct {
	Repository         string `json:"repository" yaml:"repository"`
	Release            string `json:"release" yaml:"release"`
	Commit             string `json:"commit" yaml:"commit"`
	ReleaseAssetSHA256 string `json:"release_asset_sha256" yaml:"release_asset_sha256"`
}

type Runtime struct {
	IncusSDKVersion string `json:"incus_sdk_version" yaml:"incus_sdk_version"`
}

type Build struct {
	SourceCommit         string `json:"source_commit" yaml:"source_commit"`
	BinarySHA256         string `json:"binary_sha256" yaml:"binary_sha256"`
	GoVersion            string `json:"go_version" yaml:"go_version"`
	CGOEnabled           bool   `json:"cgo_enabled" yaml:"cgo_enabled"`
	TargetOS             string `json:"target_os" yaml:"target_os"`
	TargetArch           string `json:"target_arch" yaml:"target_arch"`
	Trimpath             bool   `json:"trimpath" yaml:"trimpath"`
	EmptyBuildID         bool   `json:"empty_build_id" yaml:"empty_build_id"`
	VCSMetadata          string `json:"vcs_metadata" yaml:"vcs_metadata"`
	ReproducibleRebuilds int    `json:"reproducible_rebuilds" yaml:"reproducible_rebuilds"`
}

var (
	hex40       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hex64       = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nddevSemver = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+-nddev\.[0-9]+$`)
	plainSemver = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
)

func Load(path string) (Manifest, error) {
	file, err := os.Open(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("open provider release manifest: %w", err)
	}
	defer file.Close()

	manifest, err := Decode(file)
	if err != nil {
		return Manifest{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return manifest, nil
}

func Decode(reader io.Reader) (Manifest, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestBytes+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("read provider release manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("provider release manifest exceeds %d bytes", maxManifestBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("parse YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, fmt.Errorf("multiple YAML documents are not allowed")
		}
		return Manifest{}, fmt.Errorf("parse trailing YAML: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (m Manifest) Validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("schema_version: %d is not a schema this reader speaks", m.SchemaVersion)
	}
	if m.Artifact != "garm-provider-incus" {
		return fmt.Errorf("artifact: %q, want \"garm-provider-incus\"", m.Artifact)
	}
	// A derivative version has to be distinguishable from the upstream release it
	// derives from, or a host config could name upstream's v0.1.5 and be accepted
	// by a binary that is several NDDev revisions past it.
	if !nddevSemver.MatchString(m.DerivativeVersion) {
		return fmt.Errorf("derivative_version: %q must look like vMAJOR.MINOR.PATCH-nddev.N", m.DerivativeVersion)
	}
	if !plainSemver.MatchString(m.InterfaceVersion) {
		return fmt.Errorf("interface_version: %q must be a plain vMAJOR.MINOR.PATCH", m.InterfaceVersion)
	}
	if !strings.HasPrefix(m.Upstream.Repository, "https://") || strings.HasSuffix(m.Upstream.Repository, ".git") {
		return fmt.Errorf("upstream.repository: %q must be the canonical https URL without a .git suffix", m.Upstream.Repository)
	}
	if !plainSemver.MatchString(m.Upstream.Release) {
		return fmt.Errorf("upstream.release: %q must be a plain vMAJOR.MINOR.PATCH", m.Upstream.Release)
	}
	if !strings.HasPrefix(m.DerivativeVersion, m.Upstream.Release+"-nddev.") {
		return fmt.Errorf("derivative_version %q does not derive from upstream.release %q", m.DerivativeVersion, m.Upstream.Release)
	}
	if !hex40.MatchString(m.Upstream.Commit) {
		return fmt.Errorf("upstream.commit: %q is not a full commit id", m.Upstream.Commit)
	}
	if !hex64.MatchString(m.Upstream.ReleaseAssetSHA256) {
		return fmt.Errorf("upstream.release_asset_sha256: %q is not a sha256", m.Upstream.ReleaseAssetSHA256)
	}
	if !plainSemver.MatchString(m.Runtime.IncusSDKVersion) {
		return fmt.Errorf("runtime.incus_sdk_version: %q must be a plain vMAJOR.MINOR.PATCH", m.Runtime.IncusSDKVersion)
	}
	if !hex40.MatchString(m.Build.SourceCommit) {
		return fmt.Errorf("build.source_commit: %q is not a full commit id", m.Build.SourceCommit)
	}
	if !hex64.MatchString(m.Build.BinarySHA256) {
		return fmt.Errorf("build.binary_sha256: %q is not a sha256", m.Build.BinarySHA256)
	}
	if m.Build.GoVersion != "go1.26.6" || m.Build.CGOEnabled || m.Build.TargetOS != "linux" ||
		m.Build.TargetArch != "amd64" || !m.Build.Trimpath || !m.Build.EmptyBuildID ||
		m.Build.VCSMetadata != "disabled" || m.Build.ReproducibleRebuilds < 2 {
		return fmt.Errorf("build: release requires go1.26.6, CGO disabled, linux/amd64, trimpath, empty build ID, disabled implicit VCS metadata and two rebuilds")
	}
	return nil
}

// DevelopmentVersion is what a binary reports when it was built without the
// release stamp. It is deliberately not a version any host config can name, so
// an unstamped binary refuses every real platform policy instead of claiming to
// be the release it was built beside.
const DevelopmentVersion = "v0.0.0-unknown"

// IsRelease reports whether a version string is a stamped release rather than
// the development sentinel.
func IsRelease(version string) bool { return nddevSemver.MatchString(version) }
