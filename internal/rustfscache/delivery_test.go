package rustfscache

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadDeliveryEnforcesIdentityAndFilesystemContract(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.Chmod(directory, 0o750))
	config := testConfig(t, directory)
	config.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(config.CAFile, testCacheCA(t), 0o644))
	require.NoError(t, os.Chmod(config.CAFile, 0o644))
	role := "trusted-writer"
	accessKeyPath := filepath.Join(directory, "rustfs-"+role+"-access-key")
	require.NoError(t, os.WriteFile(accessKeyPath, []byte(accessKeyForRole(role)+"\n"), 0o640))
	secretPath := filepath.Join(directory, "rustfs-"+role+"-secret-key")
	require.NoError(t, os.WriteFile(secretPath, []byte("0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_-\n"), 0o640))
	// WriteFile only requests a mode; the process umask masks it. The delivery
	// contract requires exactly 0640, so under the worker's umask 077 these
	// credentials would land as 0600 and be rejected for the wrong reason.
	require.NoError(t, os.Chmod(accessKeyPath, 0o640))
	require.NoError(t, os.Chmod(secretPath, 0o640))

	delivery, err := LoadDelivery(config, role, os.Geteuid(), os.Getegid(), os.Geteuid(), os.Getegid())
	require.NoError(t, err)
	require.Equal(t, role, delivery.Role)
	require.Equal(t, accessKeyForRole(role), delivery.AccessKey)
	require.Len(t, delivery.SecretKey, 64)
	require.NotEmpty(t, delivery.CAPEM)
	delivery.Clear()
	require.Nil(t, delivery.SecretKey)
	require.Nil(t, delivery.CAPEM)

	_, err = LoadDelivery(config, "promoter", os.Geteuid(), os.Getegid(), os.Geteuid(), os.Getegid())
	require.ErrorContains(t, err, "not deliverable")
	require.NoError(t, os.Chmod(secretPath, 0o600))
	_, err = LoadDelivery(config, role, os.Geteuid(), os.Getegid(), os.Geteuid(), os.Getegid())
	require.ErrorContains(t, err, "singly linked regular file")
}

func testCacheCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "NDDev cache test CA"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
