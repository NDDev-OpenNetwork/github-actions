package diagnosticexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadCredentialIsBoundedPrivateAndWhitespaceStrict(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(filename, []byte("opaque-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := readCredential(filename, 64)
	if err != nil || value != "opaque-credential" {
		t.Fatalf("readCredential() = %q, %v", value, err)
	}
	if err := os.Chmod(filename, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := readCredential(filename, 64); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("open credential error = %v", err)
	}
}

func TestHexDigest(t *testing.T) {
	value := strings.Repeat("ab", 32)
	decoded, err := hexDigest(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("hexDigest() = %x, %v", decoded, err)
	}
	if _, err := hexDigest("not-a-digest"); err == nil {
		t.Fatal("invalid digest was accepted")
	}
}
