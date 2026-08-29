package imagemanifest

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// The promised surface is what a job can invoke with no setup step. The baked
// toolchains live in the runner tool cache, which the setup-* actions put on
// PATH, so naming them here would promise something a plain `run:` step cannot
// call -- the exact confusion this field exists to end.
func TestProvidesDoesNotPromiseToolCacheToolchains(t *testing.T) {
	toolCacheOnly := map[string]struct{}{
		"bun": {}, "cargo": {}, "corepack": {}, "dart": {}, "deno": {},
		"flutter": {}, "gh": {}, "go": {}, "gofmt": {}, "node": {}, "npm": {},
		"npx": {}, "pnpm": {}, "rustc": {}, "uv": {}, "yarn": {},
	}
	for _, path := range goldenManifestPaths(t) {
		manifest := loadGolden(t, path)
		for _, provided := range manifest.Guest.Provides {
			if _, found := toolCacheOnly[provided]; found {
				t.Errorf("%s promises %q, which lives in the runner tool cache and is not on a job's PATH",
					filepath.Base(path), provided)
			}
		}
	}
}

// Every promised command must be traceable to something the image installs:
// an apt package from this manifest, or one of the few binaries provision.sh
// puts in /usr/local/bin. Otherwise the list drifts into wishful thinking and
// only the smoke, which runs late, would catch it.
func TestEveryPromisedCommandComesFromThisManifest(t *testing.T) {
	fromPackage := map[string]string{
		"g++": "build-essential", "gcc": "build-essential", "make": "build-essential",
		"busybox": "busybox-static", "cmake": "cmake", "ctest": "cmake",
		"curl": "curl", "docker": "docker.io", "git": "git", "git-lfs": "git-lfs",
		"jq": "jq", "mvn": "maven", "ninja": "ninja-build",
		"java": "openjdk-21-jdk-headless", "javac": "openjdk-21-jdk-headless",
		"pigz": "pigz", "pkg-config": "pkg-config", "pip3": "python3-pip",
		"rg": "ripgrep", "rsync": "rsync", "shellcheck": "shellcheck",
		"sudo": "sudo", "tar": "tar",
		"unzip": "unzip", "xz": "xz-utils", "zip": "zip", "zstd": "zstd",
		"Xvfb": "xvfb",
	}
	// Installed outside apt: the sccache binary, and the python/pip aliases
	// Ubuntu deliberately does not ship.
	fromProvisioning := map[string]struct{}{
		"sccache": {}, "python": {}, "python3": {}, "pip": {},
	}
	// A pinned PATH binary is its own justification: it is installed by name.
	for _, path := range goldenManifestPaths(t) {
		for _, binary := range loadGolden(t, path).Guest.PathBinaries {
			fromProvisioning[binary.Name] = struct{}{}
		}
	}
	for _, path := range goldenManifestPaths(t) {
		manifest := loadGolden(t, path)
		packages := make(map[string]struct{}, len(manifest.Guest.Packages))
		for _, name := range manifest.Guest.Packages {
			packages[name] = struct{}{}
		}
		for _, provided := range manifest.Guest.Provides {
			if _, direct := fromProvisioning[provided]; direct {
				continue
			}
			source, known := fromPackage[provided]
			if !known {
				t.Errorf("%s promises %q with no package or provisioning step behind it",
					filepath.Base(path), provided)
				continue
			}
			if _, installed := packages[source]; !installed {
				t.Errorf("%s promises %q but does not install %s", filepath.Base(path), provided, source)
			}
		}
	}
}

func goldenManifestPaths(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "golden-image*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden manifests found: %v", err)
	}
	return paths
}

func loadGolden(t *testing.T, path string) Manifest {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	manifest, err := Load(path)
	if err != nil {
		t.Fatalf("load %s: %v", path, err)
	}
	return manifest
}

// A tool on PATH is part of what every worker promises, so a version that
// drifts between the standard and integration images would make the same
// command mean two things depending on where a job landed.
func TestVariantsPinTheSamePathBinaries(t *testing.T) {
	var reference []Tool
	var referenceName string
	for _, path := range goldenManifestPaths(t) {
		manifest := loadGolden(t, path)
		if reference == nil {
			reference, referenceName = manifest.Guest.PathBinaries, filepath.Base(path)
			continue
		}
		if !reflect.DeepEqual(reference, manifest.Guest.PathBinaries) {
			t.Errorf("%s pins different path binaries from %s", filepath.Base(path), referenceName)
		}
	}
	if len(reference) == 0 {
		t.Fatal("no manifest pins a path binary; actionlint is the reason this mechanism exists")
	}
}
