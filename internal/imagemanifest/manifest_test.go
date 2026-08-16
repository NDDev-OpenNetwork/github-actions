package imagemanifest

import (
	"errors"
	"path/filepath"
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
	if manifest.Guest.BuilderDiskGiB != 20 {
		t.Fatalf("builder disk = %d GiB, want 20", manifest.Guest.BuilderDiskGiB)
	}
	assertBakedToolchains(t, manifest)
	fingerprint, err := manifest.Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	if !strings.HasPrefix(fingerprint, "sha256:") || len(fingerprint) != len("sha256:")+64 {
		t.Fatalf("unexpected fingerprint %q", fingerprint)
	}
	if fingerprint != "sha256:59412e761e56be796b11f66163eefc3cb59de3bb766e12c98828d622527c4e50" {
		t.Fatalf("standard manifest fingerprint drifted: %q", fingerprint)
	}
}

// assertBakedToolchains keeps both worker images pinned to the exact toolchain
// versions the representative benchmark installers short-circuit on. A drift
// here silently reintroduces a per-job toolchain download.
func assertBakedToolchains(t *testing.T, manifest Manifest) {
	t.Helper()
	wanted := map[string]struct{ version, archiveSHA256 string }{
		"bun":  {"1.3.14", "951ee2aee855f08595aeec6225226a298d3fea83a3dcd6465c09cbccdf7e848f"},
		"go":   {"1.26.5", "5c2c3b16caefa1d968a94c1daca04a7ca301a496d9b086e17ad77bb81393f053"},
		"node": {"24.19.0", "14b342e71204f811bde6153be8e04b62aef63c236fef92b55f9c83154b409647"},
		"rust": {"1.97.1", "88f28fa9af20594179f85d6df67078dfd6fa93e2f6da5e1e9b0ac4997988ca4f"},
		"uv":   {"0.11.30", "04bc7d180d6138bf6dc08387acf507a823f397a98fea55da36b0ccc7fbce3b68"},
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
	if !manifest.Guest.DockerCapable() || manifest.Guest.BuilderDiskGiB != 24 {
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
	assertBakedToolchains(t, manifest)
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
		{"v-prefixed version", func(m *Manifest) { m.Toolchains[1].Version = "v1.26.5" }, "MAJOR.MINOR.PATCH"},
		{"foreign host", func(m *Manifest) {
			m.Toolchains[1].DownloadURL = "https://example.com/dl/go1.26.5.linux-amd64.tar.gz"
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
