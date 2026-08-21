package diagnosticexport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/workerdiagnostics"
)

func diagnosticFixture(t *testing.T) (Config, string) {
	return diagnosticFixtureForPool(t, "nddev-linux-standard")
}

func diagnosticFixtureForPool(t *testing.T, pool string) (Config, string) {
	return diagnosticFixtureForIdentity(t, pool, pool)
}

func diagnosticFixtureForIdentity(t *testing.T, pool, scaleSet string) (Config, string) {
	return diagnosticFixtureForScope(t, pool, scaleSet, "pool-test", validConfig().Repositories[1])
}

func diagnosticFixtureForScope(t *testing.T, pool, scaleSet, poolID, repository string) (Config, string) {
	return diagnosticFixtureForTrust(t, pool, scaleSet, poolID, repository, "trusted")
}

func diagnosticFixtureForTrust(t *testing.T, pool, scaleSet, poolID, repository, trust string) (Config, string) {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "diagnostics")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	config := validConfig()
	config.SourceDirectory = directory
	config.StateDirectory = filepath.Join(t.TempDir(), "state")
	capturedAt := time.Date(2026, 8, 8, 15, 16, 17, 123456789, time.UTC)
	store := workerdiagnostics.Store{
		Directory:      directory,
		Retention:      7 * 24 * time.Hour,
		MaxBundleBytes: config.MaxBundleBytes,
		MaxTotalBytes:  1024 * 1024 * 1024,
		MaxArtifacts:   workerdiagnostics.DefaultMaxArtifacts,
		Now:            func() time.Time { return capturedAt },
		Random:         strings.NewReader("abcdef"),
	}
	result, err := store.Write(context.Background(), workerdiagnostics.Instance{
		Name:             "runner-export-test",
		Trust:            trust,
		ControllerID:     "controller-test",
		PoolID:           poolID,
		PoolName:         pool,
		ScaleSet:         scaleSet,
		Repository:       repository,
		ImageFingerprint: strings.Repeat("a", 64),
		RunnerVersion:    "v2.336.0",
		ProviderVersion:  "v0.1.5-nddev.3",
		ProviderCommit:   strings.Repeat("b", 40),
		State:            "Running",
	}, []workerdiagnostics.Artifact{
		{Path: "incus/qemu.log", Source: "Incus qemu", Content: []byte("boot ok\n")},
		{Path: "runner/Worker.log", Source: "runner", Content: []byte("job ok\n")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return config, filepath.Base(result.Path)
}

func TestReadBundleSeparatesTrustDomains(t *testing.T) {
	config, name := diagnosticFixtureForTrust(
		t, "nddev-linux-release", "nddev-linux-release", "pool-test",
		validConfig().Repositories[1], "release",
	)
	config.Pools = []string{"nddev-linux-integration", "nddev-linux-release", "nddev-linux-standard"}
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bundle.ObjectKey, "/trust/release/") || strings.Contains(bundle.ObjectKey, "/trust/trusted/") {
		t.Fatalf("release object key = %q", bundle.ObjectKey)
	}
}

func TestReadBundleRejectsUnknownTrustDomain(t *testing.T) {
	config, name := diagnosticFixtureForTrust(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test",
		validConfig().Repositories[1], "unreviewed",
	)
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("unknown trust error = %v", err)
	}
}

func TestLegacyBundleWithoutTrustRemainsTrusted(t *testing.T) {
	config := validConfig()
	instance := workerdiagnostics.Instance{
		PoolID:     "pool-test",
		PoolName:   "nddev-linux-standard",
		ScaleSet:   "nddev-linux-standard",
		Repository: config.Repositories[1],
	}
	key, err := objectKey(config, instance, time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(key, "/trust/trusted/") {
		t.Fatalf("legacy object key = %q", key)
	}
}

func TestReadBundleAcceptsUnassignedWarmScopeAndSeparatesObjectKey(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t,
		"nddev-linux-standard",
		"nddev-linux-standard",
		"warm/nddev-linux-standard",
		"",
	)
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	wanted := "diagnostics/v1/unassigned-warm/trust/trusted/platform/linux/amd64/" +
		"pool/nddev-linux-standard/2026/08/08/sha256/"
	if !strings.HasPrefix(bundle.ObjectKey, wanted) || strings.Contains(bundle.ObjectKey, "/repository/") {
		t.Fatalf("unassigned warm object key = %q", bundle.ObjectKey)
	}
}

func TestReadBundleAcceptsReviewedWholeAccountScope(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", "example-user",
	)
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	wanted := "diagnostics/v1/account/example-user/trust/trusted/platform/linux/amd64/"
	if !strings.HasPrefix(bundle.ObjectKey, wanted) {
		t.Fatalf("whole-account object key = %q", bundle.ObjectKey)
	}
}

func TestReadBundleAcceptsRepositoryOwnedByReviewedAccount(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", "example-user/another-repository",
	)
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	wanted := "diagnostics/v1/account/example-user/another-repository/trust/trusted/platform/linux/amd64/"
	if !strings.HasPrefix(bundle.ObjectKey, wanted) {
		t.Fatalf("account repository object key = %q", bundle.ObjectKey)
	}
}

func TestReadBundleRejectsRepositoryOwnedByUnknownAccount(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", "unknown-account/another-repository",
	)
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("unknown account repository error = %v", err)
	}
}

func TestReadBundleRejectsMalformedRepositoryInReviewedAccountScope(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", "example-user/repository/extra",
	)
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("malformed account repository error = %v", err)
	}
}

func TestReadBundleSeparatesReviewedTenantScopes(t *testing.T) {
	for _, test := range []struct {
		identity string
		prefix   string
	}{
		{"example-media", "diagnostics/v1/account/example-media/"},
		{"example-guild/example-project", "diagnostics/v1/repository/example-guild/example-project/"},
	} {
		config, name := diagnosticFixtureForScope(
			t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", test.identity,
		)
		bundle, err := ReadBundle(context.Background(), config, name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(bundle.ObjectKey, test.prefix) {
			t.Fatalf("tenant %q object key = %q", test.identity, bundle.ObjectKey)
		}
	}
}

func TestReadBundleRejectsUnknownTenantScope(t *testing.T) {
	config, name := diagnosticFixtureForScope(
		t, "nddev-linux-standard", "nddev-linux-standard", "pool-test", "unknown-account",
	)
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("unknown tenant error = %v", err)
	}
}

func TestReadBundleRejectsAmbiguousRepositoryAndWarmIdentities(t *testing.T) {
	for _, test := range []struct {
		name       string
		poolID     string
		repository string
	}{
		{name: "empty repository without warm pool", poolID: "pool-test", repository: ""},
		{name: "repository with warm pool", poolID: "warm/nddev-linux-standard", repository: validConfig().Repositories[1]},
		{name: "wrong warm pool", poolID: "warm/nddev-linux-integration", repository: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, name := diagnosticFixtureForScope(
				t,
				"nddev-linux-standard",
				"nddev-linux-standard",
				test.poolID,
				test.repository,
			)
			if _, err := ReadBundle(context.Background(), config, name); err == nil ||
				!strings.Contains(err.Error(), "outside the configured repository or pool") {
				t.Fatalf("ambiguous identity error = %v", err)
			}
		})
	}
}

func TestReadBundleAcceptsEachReviewedPoolAndNamespacesObjectKey(t *testing.T) {
	for _, pool := range []string{"nddev-linux-integration", "nddev-linux-standard"} {
		t.Run(pool, func(t *testing.T) {
			config, name := diagnosticFixtureForPool(t, pool)
			bundle, err := ReadBundle(context.Background(), config, name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(bundle.ObjectKey, "/pool/"+pool+"/") {
				t.Fatalf("object key %q is outside pool %q", bundle.ObjectKey, pool)
			}
		})
	}
}

func TestReadBundleRejectsPoolOutsideReviewedAllowlist(t *testing.T) {
	config, name := diagnosticFixtureForPool(t, "nddev-linux-release")
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("unreviewed pool error = %v", err)
	}
}

func TestReadBundleRejectsPoolAndScaleSetMismatch(t *testing.T) {
	config, name := diagnosticFixtureForIdentity(t, "nddev-linux-standard", "nddev-linux-integration")
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "outside the configured repository or pool") {
		t.Fatalf("pool/Scale Set mismatch error = %v", err)
	}
}

func TestReadBundleVerifiesIdentityContentAndObjectKey(t *testing.T) {
	config, name := diagnosticFixture(t)
	names, err := ListBundles(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != name {
		t.Fatalf("ListBundles() = %#v", names)
	}
	bundle, err := ReadBundle(context.Background(), config, name)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Content) == 0 || len(bundle.SHA256) != 64 || bundle.Manifest.Instance.Repository != config.Repositories[1] {
		t.Fatalf("unexpected verified bundle: %#v", bundle)
	}
	wanted := "diagnostics/v1/repository/example-user/github-actions/trust/trusted/platform/linux/amd64/" +
		"pool/nddev-linux-standard/2026/08/08/sha256/"
	if !strings.HasPrefix(bundle.ObjectKey, wanted) || !strings.HasSuffix(bundle.ObjectKey, bundle.SHA256+".tar.gz") {
		t.Fatalf("object key = %q", bundle.ObjectKey)
	}
}

func TestReadBundleRejectsChangedBytesAndOpenMode(t *testing.T) {
	config, name := diagnosticFixture(t)
	filename := filepath.Join(config.SourceDirectory, name)
	content, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)/2] ^= 0xff
	if err := os.WriteFile(filename, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundle(context.Background(), config, name); err == nil {
		t.Fatal("tampered bundle was accepted")
	}
	if err := os.Chmod(filename, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundle(context.Background(), config, name); err == nil || !strings.Contains(err.Error(), "mode is unsafe") {
		t.Fatalf("open-mode error = %v", err)
	}
}

func TestReadBundleRejectsTrailingCompressedContent(t *testing.T) {
	config, name := diagnosticFixture(t)
	filename := filepath.Join(config.SourceDirectory, name)
	file, err := os.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("unexpected trailing bytes")); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBundle(context.Background(), config, name); err == nil ||
		!strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing bytes error = %v", err)
	}
}

func TestListBundlesRejectsSymlinkedSpool(t *testing.T) {
	config, _ := diagnosticFixture(t)
	link := filepath.Join(t.TempDir(), "diagnostics-link")
	if err := os.Symlink(config.SourceDirectory, link); err != nil {
		t.Fatal(err)
	}
	config.SourceDirectory = link
	if _, err := ListBundles(config); err == nil || !strings.Contains(err.Error(), "must not traverse symlinks") {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestSafeArchivePathRejectsTraversalAndDegenerateNames(t *testing.T) {
	for _, value := range []string{"", ".", "../secret", "a/../secret", "/absolute", "line\nbreak", `windows\path`} {
		if safeArchivePath(value) {
			t.Errorf("safeArchivePath(%q) = true", value)
		}
	}
	for _, value := range []string{"manifest.json", "incus/qemu.log", "runner/Worker.log"} {
		if !safeArchivePath(value) {
			t.Errorf("safeArchivePath(%q) = false", value)
		}
	}
}

func TestListBundlesDoesNotRejectLargeBacklogAboveFormerBoundary(t *testing.T) {
	config, original := diagnosticFixture(t)
	content, err := os.ReadFile(filepath.Join(config.SourceDirectory, original))
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 10000; index++ {
		filename := filepath.Join(config.SourceDirectory, fmt.Sprintf(
			"runner-diagnostics-v1-backlog-%08dT000000.000000000Z-%012x.tar.gz", index, index,
		))
		if err := os.WriteFile(filename, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	batch, total, _, err := ListBundleBatch(context.Background(), config, maxBundlesPerRun)
	if err != nil || total != 10001 || len(batch) != maxBundlesPerRun {
		t.Fatalf("large directory batch=%d total=%d err=%v", len(batch), total, err)
	}
}
