package imagemanifest

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
)

var (
	aliasPattern          = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{2,63}$`)
	filePattern           = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)
	packagePattern        = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,99}$`)
	packageVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+:~_-]{0,127}$`)
	imageRefPattern       = regexp.MustCompile(`^nddev/gha-action-base:busybox-[0-9][a-z0-9.-]{2,95}$`)
	sha256Pattern         = regexp.MustCompile(`^[0-9a-f]{64}$`)
	fingerprintPattern    = regexp.MustCompile(`^[0-9A-F]{40}$`)
	releaseIDPattern      = regexp.MustCompile(`^[0-9]{8}$`)
	versionPattern        = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	plainVersionPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	commitPattern         = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// toolchainAssets maps each baked toolchain to the exact release asset it must
// pin. The archive name and URL path are rendered from the pinned version, so a
// manifest cannot silently point at a different platform, channel or vendor.
var toolchainAssets = map[string]struct {
	Host    string
	Archive func(version string) string
	Path    func(version string) string
}{
	"bun": {
		Host:    "github.com",
		Archive: func(string) string { return "bun-linux-x64.zip" },
		Path: func(version string) string {
			return "/oven-sh/bun/releases/download/bun-v" + version + "/bun-linux-x64.zip"
		},
	},
	"gh": {
		Host: "github.com",
		Archive: func(version string) string {
			return "gh_" + version + "_linux_amd64.tar.gz"
		},
		Path: func(version string) string {
			return "/cli/cli/releases/download/v" + version + "/gh_" + version + "_linux_amd64.tar.gz"
		},
	},
	"go": {
		Host:    "go.dev",
		Archive: func(version string) string { return "go" + version + ".linux-amd64.tar.gz" },
		Path:    func(version string) string { return "/dl/go" + version + ".linux-amd64.tar.gz" },
	},
	"node22": {
		Host:    "nodejs.org",
		Archive: func(version string) string { return "node-v" + version + "-linux-x64.tar.xz" },
		Path: func(version string) string {
			return "/dist/v" + version + "/node-v" + version + "-linux-x64.tar.xz"
		},
	},
	"node24": {
		Host:    "nodejs.org",
		Archive: func(version string) string { return "node-v" + version + "-linux-x64.tar.xz" },
		Path: func(version string) string {
			return "/dist/v" + version + "/node-v" + version + "-linux-x64.tar.xz"
		},
	},
	"node25": {
		Host:    "nodejs.org",
		Archive: func(version string) string { return "node-v" + version + "-linux-x64.tar.xz" },
		Path: func(version string) string {
			return "/dist/v" + version + "/node-v" + version + "-linux-x64.tar.xz"
		},
	},
	"pnpm": {
		Host:    "registry.npmjs.org",
		Archive: func(version string) string { return "pnpm-" + version + ".tgz" },
		Path:    func(version string) string { return "/pnpm/-/pnpm-" + version + ".tgz" },
	},
	"rust": {
		Host: "static.rust-lang.org",
		Archive: func(version string) string {
			return "rust-" + version + "-x86_64-unknown-linux-gnu.tar.xz"
		},
		Path: func(version string) string {
			return "/dist/rust-" + version + "-x86_64-unknown-linux-gnu.tar.xz"
		},
	},
	"uv": {
		Host:    "github.com",
		Archive: func(string) string { return "uv-x86_64-unknown-linux-gnu.tar.gz" },
		Path: func(version string) string {
			return "/astral-sh/uv/releases/download/" + version + "/uv-x86_64-unknown-linux-gnu.tar.gz"
		},
	},
	"yarn": {
		Host:    "registry.npmjs.org",
		Archive: func(version string) string { return "yarn-cli-dist-" + version + ".tgz" },
		Path:    func(version string) string { return "/@yarnpkg/cli-dist/-/cli-dist-" + version + ".tgz" },
	},
}

type Issue struct {
	Path    string
	Message string
}

type ValidationError struct{ Issues []Issue }

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Path+": "+issue.Message)
	}
	return "invalid image manifest: " + strings.Join(parts, "; ")
}

func (m Manifest) Validate() error {
	issues := make([]Issue, 0)
	add := func(field, message string) { issues = append(issues, Issue{field, message}) }
	if m.SchemaVersion != 1 {
		add("schema_version", "must be 1")
	}
	for field, value := range map[string]string{
		"image.alias": m.Image.Alias, "image.current_alias": m.Image.CurrentAlias,
		"image.previous_alias": m.Image.PreviousAlias, "image.source_alias": m.Image.SourceAlias,
	} {
		if !aliasPattern.MatchString(value) || strings.Contains(value, "..") || strings.HasSuffix(value, "/") {
			add(field, "must be a bounded Incus alias without traversal")
		}
	}
	if m.Image.Alias == m.Image.CurrentAlias || m.Image.Alias == m.Image.PreviousAlias || m.Image.CurrentAlias == m.Image.PreviousAlias {
		add("image", "immutable, current, and previous aliases must be distinct")
	}
	if m.Image.OS != "ubuntu" {
		add("image.os", "must be ubuntu")
	}
	imageType := m.Image.EffectiveType()
	if imageType != "virtual-machine" && imageType != "container" {
		add("image.type", "must be virtual-machine or container")
	}
	if m.Image.Release != "24.04" {
		add("image.release", "must be 24.04")
	}
	if m.Image.Architecture != "x86_64" {
		add("image.architecture", "must be x86_64")
	}
	if !releaseIDPattern.MatchString(m.Source.ReleaseID) {
		add("source.release_id", "must be an exact YYYYMMDD release identifier")
	}
	validateSourceURL(add, m.Source.BaseURL, m.Source.ReleaseID)
	for field, value := range map[string]string{
		"source.checksums_file":  m.Source.ChecksumsFile,
		"source.signature_file":  m.Source.SignatureFile,
		"source.metadata_file":   m.Source.MetadataFile,
		"runner.archive":         m.Runner.Archive,
		"compiler_cache.archive": m.CompilerCache.Archive,
	} {
		if !filePattern.MatchString(value) || path.Base(value) != value {
			add(field, "must be a plain filename")
		}
	}
	if m.Source.ChecksumsFile != "SHA256SUMS" || m.Source.SignatureFile != "SHA256SUMS.gpg" {
		add("source", "must use Canonical SHA256SUMS and SHA256SUMS.gpg")
	}
	validateSHA(add, "source.metadata_sha256", m.Source.MetadataSHA256)
	if imageType == "container" {
		if m.Source.DiskFile != "" || m.Source.DiskSHA256 != "" {
			add("source.disk_file", "must be empty for a container image")
		}
		if m.Source.RootfsFile != "ubuntu-24.04-server-cloudimg-amd64-root.tar.xz" {
			add("source.rootfs_file", "must be the pinned Canonical container rootfs")
		}
		if !filePattern.MatchString(m.Source.RootfsFile) || path.Base(m.Source.RootfsFile) != m.Source.RootfsFile {
			add("source.rootfs_file", "must be a plain filename")
		}
		validateSHA(add, "source.rootfs_sha256", m.Source.RootfsSHA256)
	} else {
		if !filePattern.MatchString(m.Source.DiskFile) || path.Base(m.Source.DiskFile) != m.Source.DiskFile {
			add("source.disk_file", "must be a plain filename")
		}
		validateSHA(add, "source.disk_sha256", m.Source.DiskSHA256)
		if m.Source.RootfsFile != "" || m.Source.RootfsSHA256 != "" {
			add("source.rootfs_file", "must be empty for a virtual-machine image")
		}
	}
	if m.Source.KeyringPath != "/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg" {
		add("source.keyring_path", "must use the Ubuntu cloud image keyring")
	}
	if !fingerprintPattern.MatchString(m.Source.SignerFingerprint) {
		add("source.signer_fingerprint", "must be a 40-character uppercase OpenPGP fingerprint")
	}
	if m.Source.SignerFingerprint != "D2EB44626FDDC30B513D5BB71A5D6C4C7DB87C81" {
		add("source.signer_fingerprint", "must match the pinned UEC image signing key")
	}
	if !versionPattern.MatchString(m.Runner.Version) {
		add("runner.version", "must be an exact vMAJOR.MINOR.PATCH version")
	}
	validateRunnerURL(add, m.Runner)
	validateSHA(add, "runner.sha256", m.Runner.SHA256)
	validateCompilerCache(add, m.CompilerCache)
	validateToolchains(add, m.Toolchains)
	if m.Guest.BuilderDiskGiB < 12 || m.Guest.BuilderDiskGiB > 24 {
		add("guest.builder_disk_gib", "must be between 12 and 24 GiB")
	}
	variant := m.Guest.EffectiveVariant()
	if variant != "standard" && variant != "integration" {
		add("guest.variant", "must be standard or integration")
	}
	if variant == "integration" {
		if !imageRefPattern.MatchString(m.Guest.DockerActionBaseRef) || strings.Contains(m.Guest.DockerActionBaseRef, "..") {
			add("guest.docker_action_base_ref", "must be a bounded immutable local image reference")
		}
		busyboxVersion := m.Guest.PackageVersions["busybox-static"]
		tagVersion := strings.NewReplacer(":", "-", "+", "-", "~", "-").Replace(busyboxVersion)
		if busyboxVersion != "" && m.Guest.DockerActionBaseRef != "nddev/gha-action-base:busybox-"+tagVersion {
			add("guest.docker_action_base_ref", "must encode the pinned busybox-static Debian version")
		}
	} else if m.Guest.DockerActionBaseRef != "" {
		add("guest.docker_action_base_ref", "must be empty for the standard image")
	}
	validatePackages(add, m.Guest.Packages, m.Guest.PackageVersions, variant)

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Path < issues[j].Path
	})
	return &ValidationError{Issues: issues}
}

func validateCompilerCache(add func(string, string), tool Tool) {
	if tool.Name != "sccache" {
		add("compiler_cache.name", "must be sccache")
	}
	if !versionPattern.MatchString(tool.Version) {
		add("compiler_cache.version", "must be an exact vMAJOR.MINOR.PATCH version")
	}
	if !commitPattern.MatchString(tool.SourceCommit) {
		add("compiler_cache.source_commit", "must be a full lowercase commit SHA")
	}
	validateSHA(add, "compiler_cache.archive_sha256", tool.ArchiveSHA256)
	validateSHA(add, "compiler_cache.binary_sha256", tool.BinarySHA256)
	parsed, err := url.Parse(tool.DownloadURL)
	wantedArchive := "sccache-" + tool.Version + "-x86_64-unknown-linux-musl.tar.gz"
	wantedPath := "/mozilla/sccache/releases/download/" + tool.Version + "/" + wantedArchive
	if tool.Archive != wantedArchive {
		add("compiler_cache.archive", "must match the pinned portable Linux sccache asset")
	}
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "github.com" || parsed.RawQuery != "" ||
		parsed.Fragment != "" || parsed.Path != wantedPath {
		add("compiler_cache.download_url", "must exactly match the pinned mozilla/sccache release asset")
	}
	wantedBinary := "sccache-" + tool.Version + "-x86_64-unknown-linux-musl/sccache"
	if tool.BinaryPath != wantedBinary || path.Clean(tool.BinaryPath) != tool.BinaryPath || strings.HasPrefix(tool.BinaryPath, "/") {
		add("compiler_cache.binary_path", "must be the exact relative sccache executable path")
	}
}

func validateToolchains(add func(string, string), toolchains []Toolchain) {
	seen := make(map[string]struct{}, len(toolchains))
	for index, toolchain := range toolchains {
		field := fmt.Sprintf("toolchains[%d]", index)
		asset, supported := toolchainAssets[toolchain.Name]
		if !supported {
			add(field+".name", "must be one of "+strings.Join(BakedToolchains(), ", "))
			continue
		}
		if _, duplicate := seen[toolchain.Name]; duplicate {
			add(field+".name", "must be unique")
		}
		seen[toolchain.Name] = struct{}{}
		if !plainVersionPattern.MatchString(toolchain.Version) {
			add(field+".version", "must be an exact MAJOR.MINOR.PATCH version")
			continue
		}
		validateSHA(add, field+".archive_sha256", toolchain.ArchiveSHA256)
		wantedArchive := asset.Archive(toolchain.Version)
		if toolchain.Archive != wantedArchive || path.Base(toolchain.Archive) != toolchain.Archive {
			add(field+".archive", "must be the pinned "+toolchain.Name+" release asset "+wantedArchive)
		}
		parsed, err := url.Parse(toolchain.DownloadURL)
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() != asset.Host ||
			parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != asset.Path(toolchain.Version) {
			add(field+".download_url", "must exactly match the pinned "+toolchain.Name+" release asset on "+asset.Host)
		}
	}
	for _, required := range BakedToolchains() {
		if _, ok := seen[required]; !ok {
			add("toolchains", "must pin "+required)
		}
	}
}

func validateSourceURL(add func(string, string), raw, releaseID string) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "cloud-images.ubuntu.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		add("source.base_url", "must be an HTTPS cloud-images.ubuntu.com URL")
		return
	}
	wantedSuffix := "/releases/noble/release-" + releaseID
	if !strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), wantedSuffix) {
		add("source.base_url", "must end in "+wantedSuffix)
	}
}

func validateRunnerURL(add func(string, string), runner Runner) {
	parsed, err := url.Parse(runner.DownloadURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() != "github.com" || parsed.RawQuery != "" || parsed.Fragment != "" {
		add("runner.download_url", "must be an HTTPS github.com release URL")
		return
	}
	wanted := "/actions/runner/releases/download/" + runner.Version + "/" + runner.Archive
	if parsed.Path != wanted {
		add("runner.download_url", "must exactly match the pinned actions/runner release asset")
	}
}

func validateSHA(add func(string, string), field, value string) {
	if !sha256Pattern.MatchString(value) {
		add(field, "must be a lowercase SHA-256 digest")
	}
}

func validatePackages(add func(string, string), packages []string, versions map[string]string, variant string) {
	required := []string{"build-essential", "ca-certificates", "curl", "git", "git-lfs", "jq", "rsync", "sudo", "tar", "unzip", "xz-utils", "zip", "zstd"}
	if variant == "integration" {
		required = append(required, "busybox-static", "docker-buildx", "docker-compose-v2", "docker.io", "pigz")
	}
	seen := make(map[string]struct{}, len(packages))
	for index, pkg := range packages {
		field := fmt.Sprintf("guest.packages[%d]", index)
		if !packagePattern.MatchString(pkg) {
			add(field, "must be a plain Debian package name")
		}
		if _, duplicate := seen[pkg]; duplicate {
			add(field, "must be unique")
		}
		seen[pkg] = struct{}{}
		if pkg == "openssh-server" || pkg == "incus" || pkg == "lxd" || pkg == "docker-ce" || pkg == "docker-ce-cli" || pkg == "containerd.io" {
			add(field, "is forbidden in managed worker images")
		}
		if variant != "integration" && (pkg == "docker.io" || pkg == "docker-buildx" || pkg == "docker-compose-v2") {
			add(field, "is forbidden in the standard worker image")
		}
	}
	for _, pkg := range required {
		if _, ok := seen[pkg]; !ok {
			add("guest.packages", "must include "+pkg)
		}
	}
	for pkg, version := range versions {
		if _, ok := seen[pkg]; !ok {
			add("guest.package_versions."+pkg, "must refer to a listed package")
		}
		if !packageVersionPattern.MatchString(version) {
			add("guest.package_versions."+pkg, "must be a bounded exact Debian version")
		}
	}
	if variant == "integration" {
		for _, pkg := range []string{"busybox-static", "docker-buildx", "docker-compose-v2", "docker.io", "pigz"} {
			if versions[pkg] == "" {
				add("guest.package_versions", "must pin "+pkg)
			}
		}
	} else if len(versions) != 0 {
		add("guest.package_versions", "must be empty for the standard image")
	}
}
