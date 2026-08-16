package imagebuild

import (
	"os"
	"path/filepath"
	"testing"
)

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
