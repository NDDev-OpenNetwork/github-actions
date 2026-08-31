package repositorycontract

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEveryNetworkBootstrapSurfaceIsClassified(t *testing.T) {
	t.Parallel()
	root := toolCacheRepositoryRoot(t)
	want := []string{
		"actions/package-cache/package-cache.sh",
		"actions/tool-cache/tool-cache.sh",
		"internal/garmproviderincus/provider/admission.go",
		"internal/garmproviderincus/provider/cache_delivery.go",
		"internal/garmproviderincus/provider/incus.go",
		"internal/garmproviderincus/provider/specs.go",
		"internal/imagebuild/assets/container-provision.sh",
		"internal/imagebuild/assets/provision.sh",
		"internal/imagebuild/assets/smoke-integration.sh",
		"internal/imagebuild/assets/smoke.sh",
		"scripts/build-garm-nddev.sh",
		"scripts/configure-sccache.sh",
		"scripts/install-benchmark-toolchain.sh",
	}
	var got []string
	for _, directory := range []string{"actions", "scripts", "internal/imagebuild/assets", "internal/garmproviderincus/provider"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".md") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			text := string(raw)
			if strings.Contains(text, "https://") || strings.Contains(text, "curl ") {
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				got = append(got, filepath.ToSlash(relative))
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(got)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("network bootstrap inventory drifted\n got: %q\nwant: %q", got, want)
	}
}

func TestExternalDownloadFallbackMarkersRemainBounded(t *testing.T) {
	t.Parallel()
	root := toolCacheRepositoryRoot(t)
	checks := map[string][]string{
		"actions/tool-cache/tool-cache.sh": {
			"--retry 2", "verify_candidate", "nddev_tool_cache_event=",
		},
		"scripts/install-benchmark-toolchain.sh": {
			"--retry 2", "toolchain SHA-256 mismatch", "rustc 1.98.0",
		},
		"internal/garmproviderincus/provider/incus.go": {
			"--retry 2", "validateRunnerTool",
		},
		"internal/imagebuild/artifacts.go": {
			"attempt <= 3", "http.StatusTooManyRequests", "expectedSHA",
		},
	}
	for relative, markers := range checks {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		for _, marker := range markers {
			if !strings.Contains(string(raw), marker) {
				t.Errorf("%s lacks %q", relative, marker)
			}
		}
	}
}

func TestGARMAndProviderRunnerWrappersShareTheAttemptBudget(t *testing.T) {
	t.Parallel()
	root := toolCacheRepositoryRoot(t)
	paths := []string{
		"internal/garmproviderincus/provider/specs.go",
		"third_party/garm/patches/0027-bound-upstream-download-attempts.patch",
	}
	for _, relative := range paths {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, `--retry 2 --retry-delay 5 --retry-connrefused`) {
			t.Errorf("%s does not pin the three-total-attempt runner wrapper", relative)
		}
		if relative == paths[0] && strings.Contains(text, `--retry 5 --retry-delay 5 --retry-connrefused`) {
			t.Errorf("%s retains the incompatible six-total-attempt runner wrapper", relative)
		}
	}
}
