package imageplan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incuspolicy"
)

type Plan struct {
	ManifestFingerprint string                    `json:"manifest_fingerprint"`
	IncusVersion        string                    `json:"incus_version"`
	Project             string                    `json:"project"`
	Profile             string                    `json:"profile"`
	PublicHostAddress   string                    `json:"public_host_address"`
	BuilderName         string                    `json:"builder_name"`
	SmokeName           string                    `json:"smoke_name"`
	InstanceConfig      map[string]string         `json:"instance_config"`
	BuilderDiskGiB      int                       `json:"builder_disk_gib"`
	SmokeRootDiskGiB    int                       `json:"smoke_root_disk_gib"`
	Image               imagemanifest.Image       `json:"image"`
	Source              imagemanifest.Source      `json:"source"`
	Runner              imagemanifest.Runner      `json:"runner"`
	CompilerCache       imagemanifest.Tool        `json:"compiler_cache"`
	Toolchains          []imagemanifest.Toolchain `json:"toolchains"`
	Packages            []string                  `json:"packages"`
	PackageInstallSpecs []string                  `json:"package_install_specs"`
	Variant             string                    `json:"variant"`
	DockerActionBaseRef string                    `json:"docker_action_base_ref,omitempty"`
}

func Build(cfg config.Config, manifest imagemanifest.Manifest, profile string) (Plan, error) {
	if err := cfg.Validate(); err != nil {
		return Plan{}, err
	}
	if err := manifest.Validate(); err != nil {
		return Plan{}, err
	}
	if cfg.ControlPlane.RunnerVersion != manifest.Runner.Version {
		return Plan{}, fmt.Errorf("platform runner %q does not match image runner %q", cfg.ControlPlane.RunnerVersion, manifest.Runner.Version)
	}
	pool, ok := cfg.Pool(profile)
	if !ok {
		return Plan{}, fmt.Errorf("image profile %q is not a configured pool", profile)
	}
	backend, ok := cfg.Backend(pool.Backend)
	if !ok {
		return Plan{}, fmt.Errorf("image profile %q references unknown backend %q", profile, pool.Backend)
	}
	wantedType := "virtual-machine"
	if backend.Implementation == "incus-container" {
		wantedType = "container"
	}
	if manifest.Image.EffectiveType() != wantedType {
		return Plan{}, fmt.Errorf("image type %q does not match backend %q", manifest.Image.EffectiveType(), backend.Implementation)
	}
	if pool.Capabilities.Docker != manifest.Guest.DockerCapable() {
		return Plan{}, fmt.Errorf(
			"image variant %q Docker capability does not match profile %q",
			manifest.Guest.EffectiveVariant(),
			profile,
		)
	}
	if pool.Capabilities.NetworkPolicy != "public-internet" {
		return Plan{}, fmt.Errorf("image profile %q must use the reconciled public-internet pilot bridge", profile)
	}
	// The pool's runtime ceiling is deliberately not asserted here. An image
	// plan describes one builder VM and one smoke VM; how many workers the
	// scheduler may later run from that image cannot change a single byte of
	// it, and MaxRunning reaches no field of Plan. The pilot pinned it to 1,
	// which made raising a pool's concurrency fail as an image-build error.
	if manifest.Guest.BuilderDiskGiB >= pool.Resources.DiskGiB {
		return Plan{}, fmt.Errorf("image builder disk must be smaller than the %d-GiB runtime profile", pool.Resources.DiskGiB)
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		return Plan{}, fmt.Errorf("fingerprint image manifest: %w", err)
	}
	short := strings.TrimPrefix(fingerprint, "sha256:")[:12]
	// Canonical toolchain order keeps guest environment, image properties and
	// publish arguments byte-stable regardless of manifest declaration order.
	toolchains := append([]imagemanifest.Toolchain(nil), manifest.Toolchains...)
	sort.Slice(toolchains, func(i, j int) bool { return toolchains[i].Name < toolchains[j].Name })
	installSpecs := make([]string, 0, len(manifest.Guest.Packages))
	for _, pkg := range manifest.Guest.Packages {
		spec := pkg
		if version := manifest.Guest.PackageVersions[pkg]; version != "" {
			spec += "=" + version
		}
		installSpecs = append(installSpecs, spec)
	}
	instanceConfig := incuspolicy.VMInstanceConfig()
	if manifest.Image.EffectiveType() == "container" {
		instanceConfig = map[string]string{
			"security.privileged":                  "false",
			"security.idmap.isolated":              "true",
			"security.nesting":                     fmt.Sprintf("%t", pool.Capabilities.Docker),
			"security.syscalls.intercept.mknod":    "false",
			"security.syscalls.intercept.setxattr": "false",
		}
	}
	return Plan{
		ManifestFingerprint: fingerprint,
		IncusVersion:        cfg.Incus.Version,
		Project:             cfg.Incus.Project,
		Profile:             profile,
		PublicHostAddress:   cfg.Incus.PublicHostAddress,
		BuilderName:         "gha-image-builder-" + short,
		SmokeName:           "gha-image-smoke-" + short,
		InstanceConfig:      instanceConfig,
		BuilderDiskGiB:      manifest.Guest.BuilderDiskGiB,
		SmokeRootDiskGiB:    pool.Resources.DiskGiB,
		Image:               manifest.Image,
		Source:              manifest.Source,
		Runner:              manifest.Runner,
		CompilerCache:       manifest.CompilerCache,
		Toolchains:          toolchains,
		Packages:            append([]string(nil), manifest.Guest.Packages...),
		PackageInstallSpecs: installSpecs,
		Variant:             manifest.Guest.EffectiveVariant(),
		DockerActionBaseRef: manifest.Guest.DockerActionBaseRef,
	}, nil
}
