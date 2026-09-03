package imagemanifest

// Manifest pins every network-fetched input used to build a worker image.
// It intentionally contains no credentials or mutable aliases as sources.
type Manifest struct {
	SchemaVersion int           `json:"schema_version" yaml:"schema_version"`
	Image         Image         `json:"image" yaml:"image"`
	Source        Source        `json:"source" yaml:"source"`
	Runner        Runner        `json:"runner" yaml:"runner"`
	CompilerCache Tool          `json:"compiler_cache" yaml:"compiler_cache"`
	GoCacheSeed   GoCacheSeed   `json:"go_cache_seed" yaml:"go_cache_seed"`
	Toolchains    []Toolchain   `json:"toolchains" yaml:"toolchains"`
	BrowserSmoke  *BrowserSmoke `json:"browser_smoke,omitempty" yaml:"browser_smoke,omitempty"`
	Guest         Guest         `json:"guest" yaml:"guest"`
}

// GoCacheSeed pins public source bytes compiled while the immutable worker
// image is built. The source tree is removed after warming the runner-owned Go
// module and build caches; its archive digest and commit remain attested.
type GoCacheSeed struct {
	Repository    string   `json:"repository" yaml:"repository"`
	Commit        string   `json:"commit" yaml:"commit"`
	Archive       string   `json:"archive" yaml:"archive"`
	DownloadURL   string   `json:"download_url" yaml:"download_url"`
	ArchiveSHA256 string   `json:"archive_sha256" yaml:"archive_sha256"`
	Packages      []string `json:"packages" yaml:"packages"`
}

type Image struct {
	Type          string `json:"type,omitempty" yaml:"type,omitempty"`
	Alias         string `json:"alias" yaml:"alias"`
	CurrentAlias  string `json:"current_alias" yaml:"current_alias"`
	PreviousAlias string `json:"previous_alias" yaml:"previous_alias"`
	SourceAlias   string `json:"source_alias" yaml:"source_alias"`
	OS            string `json:"os" yaml:"os"`
	Release       string `json:"release" yaml:"release"`
	Architecture  string `json:"architecture" yaml:"architecture"`
}

func (i Image) EffectiveType() string {
	if i.Type == "" {
		return "virtual-machine"
	}
	return i.Type
}

type Source struct {
	ReleaseID         string `json:"release_id" yaml:"release_id"`
	BaseURL           string `json:"base_url" yaml:"base_url"`
	ChecksumsFile     string `json:"checksums_file" yaml:"checksums_file"`
	SignatureFile     string `json:"signature_file" yaml:"signature_file"`
	MetadataFile      string `json:"metadata_file" yaml:"metadata_file"`
	MetadataSHA256    string `json:"metadata_sha256" yaml:"metadata_sha256"`
	DiskFile          string `json:"disk_file" yaml:"disk_file"`
	DiskSHA256        string `json:"disk_sha256" yaml:"disk_sha256"`
	RootfsFile        string `json:"rootfs_file,omitempty" yaml:"rootfs_file,omitempty"`
	RootfsSHA256      string `json:"rootfs_sha256,omitempty" yaml:"rootfs_sha256,omitempty"`
	KeyringPath       string `json:"keyring_path" yaml:"keyring_path"`
	SignerFingerprint string `json:"signer_fingerprint" yaml:"signer_fingerprint"`
}

type Runner struct {
	Version     string `json:"version" yaml:"version"`
	Archive     string `json:"archive" yaml:"archive"`
	DownloadURL string `json:"download_url" yaml:"download_url"`
	SHA256      string `json:"sha256" yaml:"sha256"`
}

// Tool pins a release artifact and its extracted executable independently.
// SourceCommit is provenance metadata; the release archive and installed
// executable remain enforced by their own SHA-256 digests.
type Tool struct {
	Name          string `json:"name" yaml:"name"`
	Version       string `json:"version" yaml:"version"`
	SourceCommit  string `json:"source_commit" yaml:"source_commit"`
	Archive       string `json:"archive" yaml:"archive"`
	DownloadURL   string `json:"download_url" yaml:"download_url"`
	ArchiveSHA256 string `json:"archive_sha256" yaml:"archive_sha256"`
	BinaryPath    string `json:"binary_path" yaml:"binary_path"`
	BinarySHA256  string `json:"binary_sha256" yaml:"binary_sha256"`
}

// Toolchain pins one language toolchain that the image bakes so no job repeats
// its download and install. A multi-file toolchain cannot be described by a
// single executable digest the way Tool describes sccache, so the enforced pair
// is the release archive digest, which fully determines the extracted tree, and
// the exact version every installed executable must report after installation.
type Toolchain struct {
	Name          string `json:"name" yaml:"name"`
	Version       string `json:"version" yaml:"version"`
	Archive       string `json:"archive" yaml:"archive"`
	DownloadURL   string `json:"download_url" yaml:"download_url"`
	ArchiveSHA256 string `json:"archive_sha256" yaml:"archive_sha256"`
	// Channels are the extra toolchains a version manager installs beside
	// itself, and DefaultChannel is the one it selects. Only rustup uses them:
	// its own version says nothing about which Rust a job gets, and the estate
	// pins those in rust-toolchain.toml rather than in the image.
	Channels       []string `json:"channels,omitempty" yaml:"channels,omitempty"`
	DefaultChannel string   `json:"default_channel,omitempty" yaml:"default_channel,omitempty"`
}

// BrowserSmoke pins browser bytes used only to launch-test the image. The
// archive is injected into the disposable smoke instance and deleted with it;
// consumer jobs install the browser version from their own lockfile.
type BrowserSmoke struct {
	Version       string `json:"version,omitempty" yaml:"version,omitempty"`
	UpstreamRef   string `json:"upstream_ref,omitempty" yaml:"upstream_ref,omitempty"`
	Archive       string `json:"archive,omitempty" yaml:"archive,omitempty"`
	DownloadURL   string `json:"download_url,omitempty" yaml:"download_url,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty" yaml:"archive_sha256,omitempty"`
	BinaryPath    string `json:"binary_path,omitempty" yaml:"binary_path,omitempty"`
}

// BakedToolchains are the toolchains every managed worker image must pin. The
// representative benchmark installers short-circuit when the pinned version is
// already on PATH, and actions/setup-go resolves a pre-seeded runner tool cache,
// so a complete set turns per-job toolchain installation into a no-op.
func BakedToolchains() []string {
	// rustup owns the whole Rust surface. A standalone rust tarball was baked
	// beside it once, and the rustup shims overwrote its /usr/local/bin
	// binaries during provisioning, so the "system" rust the recipe promised
	// was unreachable in the shipped image. The default channel serves jobs
	// that call cargo directly, and actions-rust-lang/setup-rust-toolchain
	// resolves rust-toolchain.toml channels through rustup either way.
	return []string{"bun", "codeql", "gh", "go", "node22", "node24", "node25", "node26", "pnpm", "python314", "rustup", "uv", "yarn"}
}

// OptionalToolchains may be pinned by an image that needs them and omitted by
// one that does not. Flutter is optional rather than baked because its SDK is
// 1.55 GB compressed: the standard container image carries it for the mobile
// job classes, and the integration image has no reason to pay for it.
func OptionalToolchains() []string {
	return []string{"flutter"}
}

type Guest struct {
	BuilderDiskGiB int      `json:"builder_disk_gib" yaml:"builder_disk_gib"`
	Packages       []string `json:"packages" yaml:"packages"`
	// PathBinaries are single-binary tools the image puts on PATH, pinned by
	// archive and binary digest exactly as the compiler cache is. The
	// toolchains cannot serve this: they land in the runner tool cache, which
	// only the setup-* actions add to PATH, so a plain run: step cannot call
	// them. actionlint is the reason this exists -- two repositories opened
	// their command with it and their CI did not start for weeks.
	PathBinaries []Tool `json:"path_binaries,omitempty" yaml:"path_binaries,omitempty"`
	// Provides is the command surface the image guarantees a job can invoke
	// without installing anything. Nothing stated it before, so consumers
	// guessed: two repositories opened their command with actionlint, which the
	// image does not carry, and their CI did not start for weeks; another
	// apt-installs cmake and ninja-build on every job, both of which it has
	// carried all along. The smoke proves every name here resolves inside the
	// built image, so the image cannot ship promising a tool it lacks.
	Provides            []string          `json:"provides" yaml:"provides"`
	PackageVersions     map[string]string `json:"package_versions,omitempty" yaml:"package_versions,omitempty"`
	Variant             string            `json:"variant,omitempty" yaml:"variant,omitempty"`
	DockerActionBaseRef string            `json:"docker_action_base_ref,omitempty" yaml:"docker_action_base_ref,omitempty"`
	Browser             string            `json:"browser,omitempty" yaml:"browser,omitempty"`
	// RegistryMirrorCA is the fleet CA that signs every member's registry
	// mirror (zot on 192.0.2.1:5001, the docker.io pull-through cache the
	// daemon.json this image bakes has named since the image was born).
	// dockerd with the containerd image store loads its trust once, from the
	// system store, when it starts: a CA delivered later by a claim, into
	// certs.d, or into the store without a restart never reaches it, which
	// is why the mirror went unused for a month while every pull logged an
	// unknown authority and fell through to docker.io. The build reads the
	// certificate from the host it runs on, proves the pinned digest and
	// subject, and installs it into the image's trust store before Docker
	// first starts.
	RegistryMirrorCA *RegistryMirrorCA `json:"registry_mirror_ca,omitempty" yaml:"registry_mirror_ca,omitempty"`
}

// RegistryMirrorCA pins the certificate the docker-capable image trusts for
// the members' registry mirror. Path names the file on the build host; the
// estate installs it on every host as the cache trust anchor, and the pinned
// digest is what makes the bytes the image ships reviewable.
type RegistryMirrorCA struct {
	Path    string `json:"path" yaml:"path"`
	SHA256  string `json:"sha256" yaml:"sha256"`
	Subject string `json:"subject" yaml:"subject"`
}

func (g Guest) EffectiveVariant() string {
	if g.Variant == "" {
		return "standard"
	}
	return g.Variant
}

func (g Guest) DockerCapable() bool {
	return g.EffectiveVariant() == "integration"
}

func (g Guest) BrowserCapable() bool { return g.Browser == "chromium" }
