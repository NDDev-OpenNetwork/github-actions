package imagebuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadRetriesOnlyTransientResponses(t *testing.T) {
	t.Parallel()
	payload := []byte("verified artifact")
	digestBytes := sha256.Sum256(payload)
	expected := hex.EncodeToString(digestBytes[:])
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(writer, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()

	destination := filepath.Join(t.TempDir(), "artifact")
	digest, err := download(context.Background(), server.Client(), server.URL, destination, expected, 1024)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	if attempts != 3 || digest != expected {
		t.Fatalf("attempts=%d digest=%q", attempts, digest)
	}
}

func TestDownloadDoesNotRetryPermanentResponse(t *testing.T) {
	t.Parallel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		attempts++
		http.Error(writer, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := download(
		context.Background(), server.Client(), server.URL,
		filepath.Join(t.TempDir(), "artifact"), strings.Repeat("0", 64), 1024,
	)
	if err == nil {
		t.Fatal("expected permanent download failure")
	}
	if attempts != 1 {
		t.Fatalf("permanent failure retried %d times", attempts)
	}
}

func TestParseChecksums(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path := filepath.Join(directory, "SHA256SUMS")
	content := "0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe *disk.img\n" +
		"4881b54323d62bb2a791a48c5bfa841492e55cf7a27af18b047edc904d595051 *metadata.tar.xz\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write checksums: %v", err)
	}
	checksums, err := parseChecksums(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if checksums["disk.img"] != "0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe" {
		t.Fatalf("unexpected disk checksum %q", checksums["disk.img"])
	}
}

func TestAllowedDownloadHostsAreNarrow(t *testing.T) {
	t.Parallel()
	for _, host := range []string{"cloud-images.ubuntu.com", "github.com", "release-assets.githubusercontent.com"} {
		if !allowedDownloadHost(host) {
			t.Fatalf("expected %s to be allowed", host)
		}
	}
	for _, host := range []string{"github.com.example.org", "usercontent.com", "example.com"} {
		if allowedDownloadHost(host) {
			t.Fatalf("expected %s to be rejected", host)
		}
	}
}

func TestCleanupRefusesUnexpectedDirectory(t *testing.T) {
	t.Parallel()
	if err := (Artifacts{Directory: "/var/tmp/not-ours"}).Cleanup(); err == nil {
		t.Fatal("expected cleanup refusal")
	}
}
