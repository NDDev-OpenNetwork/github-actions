package imagebuild

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/imagemanifest"
)

func writeTestCA(t *testing.T, directory, commonName string, isCA bool) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	raw := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	path := filepath.Join(directory, commonName+".pem")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return path, hex.EncodeToString(digest[:])
}

// The build ships the certificate the manifest pins and no other: the file
// on the build host must match the digest and the subject, and must be a CA.
func TestCopyRegistryMirrorCAProvesThePin(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	path, digest := writeTestCA(t, directory, "Test-Cache-CA", true)
	destination := filepath.Join(directory, registryMirrorCAArtifact)
	pin := imagemanifest.RegistryMirrorCA{Path: path, SHA256: digest, Subject: "CN=Test-Cache-CA"}
	if err := copyRegistryMirrorCA(pin, destination); err != nil {
		t.Fatalf("a pinned CA must copy: %v", err)
	}
	copied, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if copiedDigest := sha256.Sum256(copied); hex.EncodeToString(copiedDigest[:]) != digest {
		t.Fatal("the copy is not the pinned bytes")
	}

	leafPath, leafDigest := writeTestCA(t, directory, "Not-A-CA", false)
	tests := []struct {
		name string
		pin  imagemanifest.RegistryMirrorCA
		want string
	}{
		{"digest drift", imagemanifest.RegistryMirrorCA{Path: path, SHA256: strings.Repeat("0", 64), Subject: "CN=Test-Cache-CA"}, "manifest pins"},
		{"subject drift", imagemanifest.RegistryMirrorCA{Path: path, SHA256: digest, Subject: "CN=Other"}, "subject"},
		{"not a CA", imagemanifest.RegistryMirrorCA{Path: leafPath, SHA256: leafDigest, Subject: "CN=Not-A-CA"}, "not a CA"},
		{"missing file", imagemanifest.RegistryMirrorCA{Path: filepath.Join(directory, "absent.pem"), SHA256: digest, Subject: "CN=Test-Cache-CA"}, "inspect"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := copyRegistryMirrorCA(test.pin, filepath.Join(t.TempDir(), registryMirrorCAArtifact))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected %q, got %v", test.want, err)
			}
		})
	}
}
