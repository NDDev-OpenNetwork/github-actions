package imagemanifest

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRepositoryManifestIsValidAndPinned(t *testing.T) {
	t.Parallel()

	manifest, err := Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if manifest.Source.ReleaseID != "20260801" {
		t.Fatalf("release ID = %q", manifest.Source.ReleaseID)
	}
	if manifest.Runner.Version != "v2.336.0" {
		t.Fatalf("runner version = %q", manifest.Runner.Version)
	}
	if manifest.CompilerCache.Name != "sccache" || manifest.CompilerCache.Version != "v0.17.0" ||
		manifest.CompilerCache.ArchiveSHA256 != "67c4a96dd237c1f518f6b36083f270f9976d516f1e57fce891755ea782e50006" ||
		manifest.CompilerCache.BinarySHA256 != "066c5a84c85044c8f48b3ab571ac114293ea717c3d36985db022af8206e21e63" {
		t.Fatalf("compiler cache is not exactly pinned: %#v", manifest.CompilerCache)
	}
	if manifest.GoCacheSeed.Commit != "b4a49eecb836b2c97f15afe2c749d34f552f2ae4" ||
		manifest.GoCacheSeed.ArchiveSHA256 != "e2e2325f28743b645cd5fce8f520ffa5cbeed2662eada73e22d34f6a5c7472a3" ||
		len(manifest.GoCacheSeed.Packages) != 1 || manifest.GoCacheSeed.Packages[0] != "./cmd/gha-fleet" {
		t.Fatalf("Go cache seed is not exactly pinned: %#v", manifest.GoCacheSeed)
	}
	if manifest.Guest.BuilderDiskGiB != 22 {
		t.Fatalf("builder disk = %d GiB, want 22", manifest.Guest.BuilderDiskGiB)
	}
	assertBakedToolchains(t, manifest)
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
	if fingerprint != "sha256:98ab78a52fdccdd6df1622e5383f27a67c507cfdad789880c0cf10b1b7f31377" {
		t.Fatalf("standard manifest fingerprint drifted: %q", fingerprint)
	}
}

func TestContainerManifestPinsCanonicalSignedRootfs(t *testing.T) {
	t.Parallel()
	manifest, err := Load(filepath.Join("..", "..", "config", "golden-image-container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Image.EffectiveType() != "container" || manifest.Source.DiskFile != "" || manifest.Source.DiskSHA256 != "" ||
		manifest.Source.RootfsFile != "ubuntu-24.04-server-cloudimg-amd64-root.tar.xz" ||
		manifest.Source.RootfsSHA256 != "915b4be62933475c3fb5f5031aa2e159294db95fb32aaa9e8b317aadcb6c065d" {
		t.Fatalf("container source is not exactly pinned: %#v", manifest)
	}
	fingerprint, err := manifest.Fingerprint()
	if err != nil || !strings.HasPrefix(fingerprint, "sha256:") {
		t.Fatalf("container fingerprint=%q err=%v", fingerprint, err)
	}
}

// assertBakedToolchains keeps both worker images pinned to the exact toolchain
// versions the representative benchmark installers short-circuit on. A drift
// here silently reintroduces a per-job toolchain download.
func assertBakedToolchains(t *testing.T, manifest Manifest) {
	t.Helper()
	wanted := map[string]struct{ version, archiveSHA256 string }{
		"bun":       {"1.3.14", "951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f"},
		"codeql":    {"2.26.4", "48e1ab8b874d57bd6fd7c90fefee75addc5a45e9bd063982df9beb45a62dd5d3"},
		"gh":        {"2.97.0", "a2c9b8497e1f85b1ad0dfcb78b5a622e098801b8e461e459e88e1ee12f018112"},
		"go":        {"1.26.7", "ffb5f8de10c62550dfddab66b36b57030721e0a44a3218e9e1181d7b59f121ca"},
		"node22":    {"22.23.2", "d60acfe00a2932254bb0ad20e01b0d74397a0875595de719654b214f4b03f307"},
		"node24":    {"24.19.0", "14b342e71204f811bde6153be8e04b62aef63c236fef92b55f9c83154b409647"},
		"node25":    {"25.9.0", "1d8db7d6e291d167e8c467ae4094be175e1a0b3969c7ae1f8955b9f7824f7b2e"},
		"node26":    {"26.8.1", "3e301118d7df53d563b7e96c1617545f26e2f76f9724be668d6cab65c15dda5d"},
		"pnpm":      {"11.22.0", "57a97e6f23a3faffc03153a4ef8c770a0552612b8640aebe39bfdd5754d0ebdc"},
		"python314": {"3.14.7", "76d5ddab6d2dd89a39c06220f6efeda486a48ed481eae97bfc596c74ac3623db"},
		"rustup":    {"1.29.0", "4acc9acc76d5079515b46346a485974457b5a79893cfb01112423c89aeb5aa10"},
		"uv":        {"0.11.30", "04bc7d180d6138bf6dc08387acf507a823f397a98fea55da36b0ccc7fbce3b68"},
		"yarn":      {"4.18.0", "606e7e2dfc8bcc24e1b3a70a1043288a271ad2cc71cf42248fadc25f5938a497"},
	}
	if len(manifest.Toolchains) != len(wanted) {
		t.Fatalf("toolchains = %d, want %d", len(manifest.Toolchains), len(wanted))
	}
	for _, toolchain := range manifest.Toolchains {
		expected, known := wanted[toolchain.Name]
		if !known {
			t.Fatalf("unexpected baked toolchain %q", toolchain.Name)
		}
		if toolchain.Version != expected.version || toolchain.ArchiveSHA256 != expected.archiveSHA256 {
			t.Fatalf("toolchain %q is not exactly pinned: %#v", toolchain.Name, toolchain)
		}
	}
}

func TestIntegrationManifestPinsDockerToolchain(t *testing.T) {
	t.Parallel()

	manifest, err := Load(filepath.Join("..", "..", "config", "golden-image-integration.yaml"))
	if err != nil {
		t.Fatalf("load integration manifest: %v", err)
	}
	if !manifest.Guest.DockerCapable() || manifest.Guest.BuilderDiskGiB != 30 {
		t.Fatalf("unexpected integration guest: %#v", manifest.Guest)
	}
	for _, pkg := range []string{"busybox-static", "docker-buildx", "docker-compose-v2", "docker.io", "pigz"} {
		if manifest.Guest.PackageVersions[pkg] == "" {
			t.Fatalf("Docker toolchain package %q is not version-pinned", pkg)
		}
	}
	if manifest.Guest.DockerActionBaseRef != "nddev/gha-action-base:busybox-1-1.36.1-6ubuntu3.1" {
		t.Fatalf("unexpected action base reference %q", manifest.Guest.DockerActionBaseRef)
	}
	if ca := manifest.Guest.RegistryMirrorCA; ca == nil || ca.Path != "/etc/gha-fleet/trust/rustfs-ca.pem" ||
		ca.Subject != "CN=NDDev-GHA-Cache-CA" || len(ca.SHA256) != 64 {
		t.Fatalf("integration image does not pin the registry mirror CA: %#v", manifest.Guest.RegistryMirrorCA)
	}
	assertBakedToolchains(t, manifest)
}

// dockerd reads its trust once, at start, from the image's own store, so the
// CA that signs the members' registry mirror is part of the docker-capable
// image and nothing else: pinned by digest and subject, read from the fleet's
// trust directory, refused everywhere it does not belong.
func TestRegistryMirrorCAIsPinnedToTheDockerCapableImage(t *testing.T) {
	t.Parallel()
	integration, err := Load(filepath.Join("..", "..", "config", "golden-image-container-integration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	standard, err := Load(filepath.Join("..", "..", "config", "golden-image-container.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if standard.Guest.RegistryMirrorCA != nil {
		t.Fatalf("the standard image must not carry a registry mirror CA: %#v", standard.Guest.RegistryMirrorCA)
	}
	pinned := *integration.Guest.RegistryMirrorCA
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"missing on the docker image", func(m *Manifest) { m.Guest.RegistryMirrorCA = nil }, "guest.registry_mirror_ca"},
		{"outside the trust directory", func(m *Manifest) { c := pinned; c.Path = "/tmp/ca.pem"; m.Guest.RegistryMirrorCA = &c }, "guest.registry_mirror_ca.path"},
		{"traversal", func(m *Manifest) {
			c := pinned
			c.Path = "/etc/gha-fleet/trust/../secrets/x.pem"
			m.Guest.RegistryMirrorCA = &c
		}, "guest.registry_mirror_ca.path"},
		{"unpinned digest", func(m *Manifest) { c := pinned; c.SHA256 = "latest"; m.Guest.RegistryMirrorCA = &c }, "guest.registry_mirror_ca.sha256"},
		{"loose subject", func(m *Manifest) { c := pinned; c.Subject = "NDDev-GHA-Cache-CA"; m.Guest.RegistryMirrorCA = &c }, "guest.registry_mirror_ca.subject"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := integration
			test.mutate(&manifest)
			err := manifest.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation failure, got %v", test.want, err)
			}
		})
	}
	t.Run("present on the standard image", func(t *testing.T) {
		t.Parallel()
		manifest := standard
		c := pinned
		manifest.Guest.RegistryMirrorCA = &c
		err := manifest.Validate()
		if err == nil || !strings.Contains(err.Error(), "guest.registry_mirror_ca") {
			t.Fatalf("expected the standard image to refuse the mirror CA, got %v", err)
		}
	})
}

func TestValidationRequiresEveryBakedToolchain(t *testing.T) {
	t.Parallel()

	base, err := Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	for _, testCase := range []struct {
		name    string
		mutate  func(*Manifest)
		message string
	}{
		{"missing", func(m *Manifest) { m.Toolchains = m.Toolchains[1:] }, "must pin bun"},
		{"none", func(m *Manifest) { m.Toolchains = nil }, "must pin go"},
		{"unknown vendor", func(m *Manifest) { m.Toolchains[0].Name = "zig" }, "toolchains[0].name"},
		{"duplicate", func(m *Manifest) { m.Toolchains[1] = m.Toolchains[0] }, "must be unique"},
		{"v-prefixed version", func(m *Manifest) { m.Toolchains[1].Version = "v1.26.6" }, "MAJOR.MINOR.PATCH"},
		{"foreign host", func(m *Manifest) {
			m.Toolchains[1].DownloadURL = "https://example.com/dl/go1.26.6.linux-amd64.tar.gz"
		}, "toolchains[1].download_url"},
		{"version drift in URL", func(m *Manifest) {
			m.Toolchains[1].DownloadURL = "https://go.dev/dl/go1.26.4.linux-amd64.tar.gz"
		}, "toolchains[1].download_url"},
		{"archive drift", func(m *Manifest) { m.Toolchains[1].Archive = "go1.26.4.linux-amd64.tar.gz" }, "toolchains[1].archive"},
		{"path traversal archive", func(m *Manifest) { m.Toolchains[3].Archive = "../uv-x86_64-unknown-linux-gnu.tar.gz" }, "toolchains[3].archive"},
		{"unpinned digest", func(m *Manifest) { m.Toolchains[2].ArchiveSHA256 = "" }, "toolchains[2].archive_sha256"},
	} {
		mutated := base
		mutated.Toolchains = append([]Toolchain(nil), base.Toolchains...)
		testCase.mutate(&mutated)
		err := mutated.Validate()
		if err == nil || !strings.Contains(err.Error(), testCase.message) {
			t.Errorf("%s: expected %q, got %v", testCase.name, testCase.message, err)
		}
	}
}

func TestDecodeRejectsUnknownAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	if _, err := Decode(strings.NewReader("schema_version: 1\nunknown: true\n")); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
	if _, err := Decode(strings.NewReader("schema_version: 1\n---\nschema_version: 1\n")); err == nil || !strings.Contains(err.Error(), "multiple YAML documents") {
		t.Fatalf("expected multiple-document error, got %v", err)
	}
}

func TestValidationRejectsMutableOrUntrustedInputs(t *testing.T) {
	t.Parallel()

	base, err := Load(filepath.Join("..", "..", "config", "golden-image.yaml"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"floating source", func(m *Manifest) { m.Source.BaseURL = "https://cloud-images.ubuntu.com/releases/noble/release" }, "source.base_url"},
		{"wrong signer", func(m *Manifest) { m.Source.SignerFingerprint = strings.Repeat("A", 40) }, "pinned UEC image signing key"},
		{"runner redirect", func(m *Manifest) { m.Runner.DownloadURL = "https://example.com/runner.tar.gz" }, "runner.download_url"},
		{"compiler cache redirect", func(m *Manifest) { m.CompilerCache.DownloadURL = "https://example.com/sccache.tar.gz" }, "compiler_cache.download_url"},
		{"compiler cache binary traversal", func(m *Manifest) { m.CompilerCache.BinaryPath = "../sccache" }, "compiler_cache.binary_path"},
		{"Go seed redirect", func(m *Manifest) { m.GoCacheSeed.DownloadURL = "https://example.com/source.tar.gz" }, "go_cache_seed.download_url"},
		{"Go seed mutable commit", func(m *Manifest) { m.GoCacheSeed.Commit = "main" }, "go_cache_seed.commit"},
		{"Go seed package expansion", func(m *Manifest) { m.GoCacheSeed.Packages = []string{"./..."} }, "go_cache_seed.packages"},
		{"Incus alias over 64 bytes", func(m *Manifest) { m.Image.Alias = "a" + strings.Repeat("b", 64) }, "image.alias"},
		{"unsafe archive", func(m *Manifest) { m.Runner.Archive = "../runner.tar.gz" }, "runner.archive"},
		{"docker daemon", func(m *Manifest) { m.Guest.Packages = append(m.Guest.Packages, "docker.io") }, "forbidden"},
		{"SSH server", func(m *Manifest) { m.Guest.Packages = append(m.Guest.Packages, "openssh-server") }, "forbidden"},
		{"oversized builder disk", func(m *Manifest) { m.Guest.BuilderDiskGiB = 50 }, "guest.builder_disk_gib"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := base
			manifest.Guest.Packages = append([]string(nil), base.Guest.Packages...)
			test.mutate(&manifest)
			err := manifest.Validate()
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation failure, got %v", test.want, err)
			}
		})
	}
}

func TestIntegrationValidationRejectsUnpinnedOrRemoteDockerInputs(t *testing.T) {
	t.Parallel()

	base, err := Load(filepath.Join("..", "..", "config", "golden-image-integration.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Manifest)
		want   string
	}{
		{"missing engine pin", func(m *Manifest) { delete(m.Guest.PackageVersions, "docker.io") }, "must pin docker.io"},
		{"remote action base", func(m *Manifest) { m.Guest.DockerActionBaseRef = "docker.io/library/busybox:latest" }, "docker_action_base_ref"},
		{"Docker CE package", func(m *Manifest) { m.Guest.Packages = append(m.Guest.Packages, "docker-ce") }, "forbidden"},
		{"unknown variant", func(m *Manifest) { m.Guest.Variant = "privileged" }, "guest.variant"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manifest := base
			manifest.Guest.Packages = append([]string(nil), base.Guest.Packages...)
			manifest.Guest.PackageVersions = make(map[string]string, len(base.Guest.PackageVersions))
			for pkg, version := range base.Guest.PackageVersions {
				manifest.Guest.PackageVersions[pkg] = version
			}
			test.mutate(&manifest)
			err := manifest.Validate()
			var validationErr *ValidationError
			if !errors.As(err, &validationErr) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q validation failure, got %v", test.want, err)
			}
		})
	}
}

// TestRustupPinsEveryChannelTheEstateRequests is the guard the feature needs.
// Baking rustup is only worth anything if the channels the estate actually pins
// are installed beside it: all seven public setup-systems name 1.98.0 in
// rust-toolchain.toml, and their MSRV job asks for 1.89. Dropping either one
// puts those runs back to downloading a toolchain, and nothing else in the tree
// would notice.
func TestRustupPinsEveryChannelTheEstateRequests(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"../../config/golden-image.yaml",
		"../../config/golden-image-container.yaml",
		"../../config/golden-image-integration.yaml",
		"../../config/golden-image-container-integration.yaml",
	} {
		manifest, err := Load(path)
		if err != nil {
			t.Fatalf("load %s: %v", path, err)
		}
		var rustup *Toolchain
		for index := range manifest.Toolchains {
			if manifest.Toolchains[index].Name == "rustup" {
				rustup = &manifest.Toolchains[index]
			}
		}
		if rustup == nil {
			t.Fatalf("%s pins no rustup", path)
		}
		if !slices.Contains(rustup.Channels, "1.98.0") || !slices.Contains(rustup.Channels, "1.89") {
			t.Fatalf("%s rustup channels = %v, want both 1.98.0 and the 1.89 MSRV", path, rustup.Channels)
		}
		if rustup.DefaultChannel != "1.98.0" {
			t.Fatalf("%s rustup default channel = %q, want 1.98.0", path, rustup.DefaultChannel)
		}
	}
}
