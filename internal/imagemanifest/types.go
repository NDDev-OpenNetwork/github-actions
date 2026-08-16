package imagemanifest

// Manifest pins every network-fetched input used to build a worker image.
// It intentionally contains no credentials or mutable aliases as sources.
type Manifest struct {
	SchemaVersion int         `json:"schema_version" yaml:"schema_version"`
	Image         Image       `json:"image" yaml:"image"`
	Source        Source      `json:"source" yaml:"source"`
	Runner        Runner      `json:"runner" yaml:"runner"`
	CompilerCache Tool        `json:"compiler_cache" yaml:"compiler_cache"`
	Toolchains    []Toolchain `json:"toolchains" yaml:"toolchains"`
	Guest         Guest       `json:"guest" yaml:"guest"`
}

type Image struct {
	Alias         string `json:"alias" yaml:"alias"`
	CurrentAlias  string `json:"current_alias" yaml:"current_alias"`
	PreviousAlias string `json:"previous_alias" yaml:"previous_alias"`
	SourceAlias   string `json:"source_alias" yaml:"source_alias"`
	OS            string `json:"os" yaml:"os"`
	Release       string `json:"release" yaml:"release"`
	Architecture  string `json:"architecture" yaml:"architecture"`
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
}

// BakedToolchains are the toolchains every managed worker image must pin. The
// representative benchmark installers short-circuit when the pinned version is
// already on PATH, and actions/setup-go resolves a pre-seeded runner tool cache,
// so a complete set turns per-job toolchain installation into a no-op.
func BakedToolchains() []string { return []string{"bun", "go", "rust", "uv"} }

type Guest struct {
	BuilderDiskGiB      int               `json:"builder_disk_gib" yaml:"builder_disk_gib"`
	Packages            []string          `json:"packages" yaml:"packages"`
	PackageVersions     map[string]string `json:"package_versions,omitempty" yaml:"package_versions,omitempty"`
	Variant             string            `json:"variant,omitempty" yaml:"variant,omitempty"`
	DockerActionBaseRef string            `json:"docker_action_base_ref,omitempty" yaml:"docker_action_base_ref,omitempty"`
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
