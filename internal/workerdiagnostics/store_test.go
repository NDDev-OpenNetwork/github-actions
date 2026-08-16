package workerdiagnostics

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedTime = time.Date(2026, 8, 8, 11, 12, 13, 123456789, time.UTC)

func testStore(t *testing.T) Store {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "diagnostics")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return Store{
		Directory:      directory,
		Retention:      7 * 24 * time.Hour,
		MaxBundleBytes: 16 * 1024 * 1024,
		MaxTotalBytes:  1024 * 1024 * 1024,
		Now:            func() time.Time { return fixedTime },
		Random:         bytes.NewReader([]byte{0, 1, 2, 3, 4, 5}),
	}
}

func testInstance() Instance {
	return Instance{
		Name:             "runner-test-instance",
		ControllerID:     "controller-test",
		PoolID:           "pool-test",
		PoolName:         "nddev-linux-standard",
		ScaleSet:         "nddev-linux-standard",
		Repository:       "example-user/github-actions",
		ImageFingerprint: strings.Repeat("a", 64),
		RunnerVersion:    "v2.336.0",
		ProviderVersion:  "v0.1.5-nddev.3",
		ProviderCommit:   "test-commit",
		State:            "Running",
	}
}

func TestStoreWritesPrivateAtomicRedactedBundle(t *testing.T) {
	store := testStore(t)
	result, err := store.Write(context.Background(), testInstance(), []Artifact{
		{
			Path:    "runner/Worker.log",
			Source:  "/home/runner/actions-runner/_diag/Worker.log",
			Content: []byte("Authorization: Bearer opaque-secret-value\npassword=hunter2\nworker ok\n"),
		},
		{
			Path:    "console.log",
			Source:  "incus console",
			Content: []byte("boot ok\n"),
		},
	}, []string{"token=collection-secret endpoint failed"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ArtifactCount != 2 || result.CollectionFailures != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if filepath.Dir(result.Path) != store.Directory {
		t.Fatalf("bundle escaped directory: %q", result.Path)
	}
	info, err := os.Lstat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode = %v", info.Mode())
	}

	files := readBundle(t, result.Path)
	if strings.Contains(string(files["runner/Worker.log"]), "opaque-secret-value") ||
		strings.Contains(string(files["runner/Worker.log"]), "hunter2") {
		t.Fatal("bundle retained a known credential")
	}
	if !strings.Contains(string(files["runner/Worker.log"]), "worker ok") {
		t.Fatal("bundle lost non-secret diagnostics")
	}
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.Instance.Name != "runner-test-instance" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if len(manifest.Artifacts) != 2 || manifest.Artifacts[0].Path != "console.log" ||
		manifest.Artifacts[1].Path != "runner/Worker.log" {
		t.Fatalf("artifacts are not stable and sorted: %#v", manifest.Artifacts)
	}
	if strings.Contains(strings.Join(manifest.CollectionErrors, " "), "collection-secret") {
		t.Fatal("manifest retained a credential from a collection error")
	}
	entries, err := os.ReadDir(store.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || strings.HasPrefix(entries[0].Name(), ".runner-diagnostics-") {
		t.Fatalf("temporary bundle was leaked: %#v", entries)
	}
}

func TestStoreTruncatesArtifactsAndBundle(t *testing.T) {
	store := testStore(t)
	store.MaxBundleBytes = 1024
	content := bytes.Repeat([]byte("x"), 4096)
	result, err := store.Write(context.Background(), testInstance(), []Artifact{
		{Path: "one.log", Source: "one", Content: content},
		{Path: "two.log", Source: "two", Content: content},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.UncompressedBytes != 1024 {
		t.Fatalf("uncompressed bytes = %d", result.UncompressedBytes)
	}
	files := readBundle(t, result.Path)
	var manifest Manifest
	if err := json.Unmarshal(files["manifest.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Artifacts) != 1 || !manifest.Artifacts[0].Truncated || manifest.Artifacts[0].Bytes != 1024 {
		t.Fatalf("unexpected truncation manifest: %#v", manifest.Artifacts)
	}
}

func TestStoreRejectsUnsafePathsAndDirectories(t *testing.T) {
	store := testStore(t)
	for _, artifactPath := range []string{"../secret", "/absolute", "safe/../secret", `safe\secret`} {
		_, err := store.Write(context.Background(), testInstance(), []Artifact{{Path: artifactPath}}, nil)
		if err == nil || !strings.Contains(err.Error(), "unsafe diagnostic artifact path") {
			t.Fatalf("path %q error = %v", artifactPath, err)
		}
	}

	openDirectory := filepath.Join(t.TempDir(), "open")
	if err := os.Mkdir(openDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	// Mkdir only requests a mode; the process umask masks it. Under the
	// worker's umask 077 this directory would land as 0700 and stop exercising
	// the group-or-other-access rejection it exists to prove.
	if err := os.Chmod(openDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	store.Directory = openDirectory
	if _, err := store.Write(context.Background(), testInstance(), nil, nil); err == nil ||
		!strings.Contains(err.Error(), "must not grant group or other access") {
		t.Fatalf("open directory error = %v", err)
	}

	realDirectory := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(realDirectory, symlink); err != nil {
		t.Fatal(err)
	}
	store.Directory = symlink
	if _, err := store.Write(context.Background(), testInstance(), nil, nil); err == nil ||
		!strings.Contains(err.Error(), "must not traverse symlinks") {
		t.Fatalf("symlink directory error = %v", err)
	}
}

func TestStorePrunesOnlyMatchingExpiredRegularBundles(t *testing.T) {
	store := testStore(t)
	oldName := "runner-diagnostics-v1-old-runner-20260701T000000.000000000Z-000000000001.tar.gz"
	oldPath := filepath.Join(store.Directory, oldName)
	if err := os.WriteFile(oldPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := fixedTime.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(store.Directory, "do-not-delete")
	if err := os.WriteFile(unrelated, []byte("retained"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(store.Directory, "runner-diagnostics-v1-link-20260701T000000.000000000Z-000000000002.tar.gz")
	if err := os.Symlink(unrelated, symlink); err != nil {
		t.Fatal(err)
	}

	result, err := store.Write(context.Background(), testInstance(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired bundle was not removed: %v", err)
	}
	for _, retained := range []string{unrelated, symlink, result.Path} {
		if _, err := os.Lstat(retained); err != nil {
			t.Fatalf("retained path %q: %v", retained, err)
		}
	}
}

func TestStoreHonorsCancelledContext(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := store.Write(ctx, testInstance(), []Artifact{{Path: "console.log", Content: []byte("x")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "diagnostic collection interrupted") {
		t.Fatalf("cancelled context error = %v", err)
	}
	entries, readErr := os.ReadDir(store.Directory)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("cancelled capture wrote files: %#v", entries)
	}
}

func TestInspectReturnsOnlyMatchingRegularBundleMetadata(t *testing.T) {
	store := testStore(t)
	result, err := store.Write(context.Background(), testInstance(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(store.Directory, "unrelated")
	if err := os.WriteFile(unrelated, []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(store.Directory, "runner-diagnostics-v1-link-20260808T000000.000000000Z-000000000003.tar.gz")
	if err := os.Symlink(result.Path, symlink); err != nil {
		t.Fatal(err)
	}

	stats, err := Inspect(store.Directory, fixedTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Bundles != 1 || stats.Bytes != info.Size() || stats.OldestAgeSeconds != 60 || stats.NewestAgeSeconds != 60 {
		t.Fatalf("unexpected spool stats: %#v", stats)
	}
}

func TestRedactKnownCredentialForms(t *testing.T) {
	input := strings.Join([]string{
		"ghp_" + strings.Repeat("1", 36),
		"github_pat_" + strings.Repeat("2", 30),
		"AKIA1234567890123456",
		"eyJ1234567890.abcdefghijk.abcdefghijk",
		"client_secret='secret-value'",
		"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----",
	}, "\n")
	output := string(Redact([]byte(input)))
	for _, secret := range []string{"ghp_", "github_pat_", "AKIA", "eyJ", "secret-value", "BEGIN PRIVATE KEY"} {
		if strings.Contains(output, secret) {
			t.Fatalf("redaction retained %q in %q", secret, output)
		}
	}
}

func readBundle(t *testing.T, filename string) map[string][]byte {
	t.Helper()
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decompressor, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decompressor.Close()
	archive := tar.NewReader(decompressor)
	result := make(map[string][]byte)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
		result[header.Name] = content
	}
	return result
}
