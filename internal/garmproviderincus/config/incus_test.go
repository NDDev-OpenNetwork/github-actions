// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const testFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func testCredentialFiles(t *testing.T) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	cert := filepath.Join(dir, "client.crt")
	key := filepath.Join(dir, "client.key")
	server := filepath.Join(dir, "server.crt")
	for path, mode := range map[string]os.FileMode{cert: 0o644, key: 0o600, server: 0o644} {
		require.NoError(t, os.WriteFile(path, []byte("test fixture\n"), mode))
	}
	return cert, key, server
}

func testConfig(t *testing.T) Incus {
	t.Helper()
	cert, key, server := testCredentialFiles(t)
	platform := filepath.Join(filepath.Dir(cert), "platform.yaml")
	require.NoError(t, os.WriteFile(platform, []byte("schema_version: 1\n"), 0o644))
	diagnostics := filepath.Join(filepath.Dir(cert), "diagnostics")
	require.NoError(t, os.Mkdir(diagnostics, 0o700))
	return Incus{
		URL:               ExpectedIncusURL,
		ProjectName:       ExpectedProjectName,
		ClientCertificate: cert,
		ClientKey:         key,
		TLSServerCert:     server,
		SecureBoot:        true,
		InstanceType:      IncusImageVirtualMachine,
		WorkerImages: map[string]WorkerImage{
			"nddev-linux-standard": {
				Alias:       "nddev-ubuntu-24.04-amd64-current",
				Fingerprint: testFingerprint,
				Variant:     "standard",
				RunnerUID:   1001,
				RunnerGID:   1002,
			},
		},
		PlatformConfigFile:        platform,
		JournalFile:               filepath.Join(filepath.Dir(cert), "provider-journal.json"),
		JournalLockFile:           filepath.Join(filepath.Dir(cert), "provider-journal.lock"),
		QueueIntentFile:           filepath.Join(filepath.Dir(cert), "queue-intents.json"),
		AdmissionLeaseSeconds:     300,
		DiagnosticsDirectory:      diagnostics,
		DiagnosticsRetentionHours: 168,
		DiagnosticsMaxBundleBytes: 16 * 1024 * 1024,
		DiagnosticsMaxTotalBytes:  1024 * 1024 * 1024,
	}
}

func TestIncusRemoteValidation(t *testing.T) {
	valid := IncusImageRemote{
		Address:  "https://cloud-images.ubuntu.com/releases",
		Public:   true,
		Protocol: SimpleStreams,
	}
	require.NoError(t, valid.Validate())

	missingAddress := valid
	missingAddress.Address = ""
	require.EqualError(t, missingAddress.Validate(), "missing address")

	badScheme := valid
	badScheme.Address = "ftp://example.invalid"
	require.EqualError(t, badScheme.Validate(), "address must be http or https")
}

func TestIncusConfigAcceptsOnlyHardenedContract(t *testing.T) {
	cfg := testConfig(t)
	require.NoError(t, cfg.Validate())
}

func TestIncusConfigRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Incus)
		message string
	}{
		{"unix socket", func(c *Incus) { c.UnixSocket = "/var/lib/incus/unix.socket" }, "unix_socket_path is forbidden"},
		{"missing URL", func(c *Incus) { c.URL = "" }, "url must be specified"},
		{"public URL", func(c *Incus) { c.URL = "https://192.0.2.1:8443" }, "must be https://127.0.0.1:8443 or a cluster member inside"},
		{"URL query", func(c *Incus) { c.URL = ExpectedIncusURL + "?unsafe=true" }, "must be a bare https endpoint"},
		{"wrong port", func(c *Incus) { c.URL = "https://172.16.0.9:9443" }, "must use port 8443"},
		{"missing client key", func(c *Incus) { c.ClientKey = "" }, "client_certificate and client_key are mandatory"},
		{"missing server certificate", func(c *Incus) { c.TLSServerCert = "" }, "tls_server_certificate is mandatory"},
		{"remote image", func(c *Incus) { c.ImageRemotes = map[string]IncusImageRemote{"images": {}} }, "image_remotes are forbidden"},
		{"wrong project", func(c *Incus) { c.ProjectName = "default" }, "project_name must be gha-fleet"},
		{"default profile", func(c *Incus) { c.IncludeDefaultProfile = true }, "include_default_profile must be false"},
		{"secure boot disabled", func(c *Incus) { c.SecureBoot = false }, "secure_boot must be true"},
		{"container", func(c *Incus) { c.InstanceType = IncusImageContainer }, "instance_type must be virtual-machine"},
		{"missing worker images", func(c *Incus) { c.WorkerImages = nil }, "worker_images must pin at least one pool image"},
		{"blank flavor", func(c *Incus) { c.WorkerImages[" "] = c.WorkerImages["nddev-linux-standard"] }, "worker_images key must be an exact non-empty flavor"},
		{"remote-style alias", func(c *Incus) {
			image := c.WorkerImages["nddev-linux-standard"]
			image.Alias = "images:ubuntu/24.04"
			c.WorkerImages["nddev-linux-standard"] = image
		}, "alias must name one local Incus alias"},
		{"uppercase fingerprint", func(c *Incus) {
			image := c.WorkerImages["nddev-linux-standard"]
			image.Fingerprint = "A" + testFingerprint[1:]
			c.WorkerImages["nddev-linux-standard"] = image
		}, "fingerprint must be a lowercase SHA-256 digest"},
		{"unknown image variant", func(c *Incus) {
			image := c.WorkerImages["nddev-linux-standard"]
			image.Variant = "dockerish"
			c.WorkerImages["nddev-linux-standard"] = image
		}, "variant must be standard or integration"},
		{"missing runner UID", func(c *Incus) {
			image := c.WorkerImages["nddev-linux-standard"]
			image.RunnerUID = 0
			c.WorkerImages["nddev-linux-standard"] = image
		}, "runner_uid must be in 1..65535"},
		{"invalid runner GID", func(c *Incus) {
			image := c.WorkerImages["nddev-linux-standard"]
			image.RunnerGID = 65536
			c.WorkerImages["nddev-linux-standard"] = image
		}, "runner_gid must be in 1..65535"},
		{"conflicting alias pins", func(c *Incus) {
			c.WorkerImages["nddev-linux-integration"] = WorkerImage{
				Alias:       c.WorkerImages["nddev-linux-standard"].Alias,
				Fingerprint: "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
				Variant:     "integration",
				RunnerUID:   1001,
				RunnerGID:   1002,
			}
		}, "pinned to conflicting fingerprints"},
		{"missing platform policy", func(c *Incus) { c.PlatformConfigFile = "/missing/platform.yaml" }, "invalid platform_config_file"},
		{"same journal paths", func(c *Incus) { c.JournalLockFile = c.JournalFile }, "must differ"},
		{"queue intent equals journal", func(c *Incus) { c.QueueIntentFile = c.JournalFile }, "must differ"},
		{"missing queue intent parent", func(c *Incus) { c.QueueIntentFile = "/missing/queue-intents.json" }, "invalid queue_intent_file"},
		{"wrong lease", func(c *Incus) { c.AdmissionLeaseSeconds = 60 }, "must be exactly 300"},
		{"missing diagnostics directory", func(c *Incus) { c.DiagnosticsDirectory = "/missing/diagnostics" }, "invalid diagnostics_directory"},
		{"wrong diagnostics retention", func(c *Incus) { c.DiagnosticsRetentionHours = 24 }, "must be exactly 168"},
		{"wrong bundle limit", func(c *Incus) { c.DiagnosticsMaxBundleBytes = 1 }, "must be exactly 16777216"},
		{"wrong total limit", func(c *Incus) { c.DiagnosticsMaxTotalBytes = 1 }, "must be exactly 1073741824"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig(t)
			test.mutate(&cfg)
			require.ErrorContains(t, cfg.Validate(), test.message)
		})
	}
}

func TestIncusConfigRequiresAbsoluteRegularFilesAndPrivateKeyMode(t *testing.T) {
	cfg := testConfig(t)
	cfg.ClientCertificate = "relative.crt"
	require.ErrorContains(t, cfg.Validate(), "path must be absolute")

	cfg = testConfig(t)
	require.NoError(t, os.Chmod(cfg.ClientKey, 0o640))
	require.NoError(t, cfg.Validate())

	cfg = testConfig(t)
	require.NoError(t, os.Chmod(cfg.ClientKey, 0o644))
	require.ErrorContains(t, cfg.Validate(), "private key permissions")

	cfg = testConfig(t)
	target := cfg.ClientCertificate
	symlink := filepath.Join(t.TempDir(), "client.crt")
	require.NoError(t, os.Symlink(target, symlink))
	cfg.ClientCertificate = symlink
	require.ErrorContains(t, cfg.Validate(), "not a symlink")

	cfg = testConfig(t)
	realParent := filepath.Dir(cfg.ClientCertificate)
	symlinkParent := filepath.Join(t.TempDir(), "credentials")
	require.NoError(t, os.Symlink(realParent, symlinkParent))
	cfg.ClientCertificate = filepath.Join(symlinkParent, filepath.Base(cfg.ClientCertificate))
	require.ErrorContains(t, cfg.Validate(), "parent directory must not traverse symlinks")
}

func TestNewConfigRejectsUnknownKeys(t *testing.T) {
	cfg := testConfig(t)
	contents := fmt.Sprintf(`
url = %q
project_name = %q
client_certificate = %q
client_key = %q
tls_server_certificate = %q
secure_boot = true
instance_type = %q
typo_that_must_not_be_ignored = true

[worker_images.nddev-linux-standard]
alias = %q
fingerprint = %q
variant = %q
`, cfg.URL, cfg.ProjectName, cfg.ClientCertificate, cfg.ClientKey, cfg.TLSServerCert,
		cfg.InstanceType,
		cfg.WorkerImages["nddev-linux-standard"].Alias,
		cfg.WorkerImages["nddev-linux-standard"].Fingerprint,
		cfg.WorkerImages["nddev-linux-standard"].Variant)
	path := filepath.Join(t.TempDir(), "provider.toml")
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o600))

	_, err := NewConfig(path)
	require.ErrorContains(t, err, "unknown keys")
}

// A cluster member's endpoint is not loopback and must still be accepted, or
// the provider cannot reach a fleet that spans more than one host. The
// boundary widens to the fleet's private network and stops there.
func TestIncusConfigAcceptsAClusterMemberInsideThePrivateNetwork(t *testing.T) {
	cfg := testConfig(t)
	cfg.URL = "https://172.16.0.9:8443"
	require.NoError(t, cfg.Validate())

	for _, outside := range []string{
		"https://10.109.255.255:8443",
		"https://172.16.16.1:8443",
		"https://8.8.8.8:8443",
	} {
		cfg.URL = outside
		require.ErrorContains(t, cfg.Validate(), "cluster member inside", "accepted %s", outside)
	}
}

// The private network a fleet sits on is deployment data. Compiling one
// estate's subnet in meant a fleet on another network could not validate its
// own cluster endpoint without a patched binary -- which is exactly what
// happened when this repository was anonymised for publication and the
// rewritten constant started refusing every endpoint on the live fleet.
func TestIncusEndpointAcceptsTheNetworkTheDeploymentDeclares(t *testing.T) {
	t.Parallel()
	// A member on a network that is not the default.
	if err := validateIncusEndpoint("10.110.0.9:8443", "10.110.0.0/20"); err != nil {
		t.Fatalf("a declared network was refused: %v", err)
	}
	// The same address with no declaration falls back to the example estate's
	// network and is correctly outside it.
	if err := validateIncusEndpoint("10.110.0.9:8443", ""); err == nil {
		t.Fatal("an address outside the default network was accepted")
	}
	// Loopback stays valid whatever is declared, and the boundary still holds.
	if err := validateIncusEndpoint("127.0.0.1:8443", "10.110.0.0/20"); err != nil {
		t.Fatalf("loopback was refused: %v", err)
	}
	if err := validateIncusEndpoint("203.0.113.9:8443", "10.110.0.0/20"); err == nil {
		t.Fatal("a public address was accepted")
	}
	// A declaration that is not private is refused rather than trusted.
	if err := validateIncusEndpoint("203.0.113.9:8443", "203.0.113.0/24"); err == nil {
		t.Fatal("a public range was accepted as the fleet private network")
	}
}
