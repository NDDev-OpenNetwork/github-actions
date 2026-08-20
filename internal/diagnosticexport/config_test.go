package diagnosticexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validConfig() Config {
	return Config{
		SchemaVersion:         4,
		DeploymentStage:       "canary",
		SourceDirectory:       "/run/gha-diagnostic-exporter-source",
		SourceOwner:           "garm",
		StateDirectory:        "/var/lib/gha-diagnostic-exporter",
		Endpoint:              "https://198.51.100.1:9002",
		Region:                "us-east-1",
		Bucket:                "gha-diagnostics-canary",
		Prefix:                "diagnostics/v1",
		Repositories:          []string{"example-guild/example-project", "example-user/github-actions"},
		AccountScopes:         []string{"example-media", "example-user"},
		Pools:                 []string{"nddev-linux-integration", "nddev-linux-standard"},
		Trusts:                []string{"release", "trusted"},
		Platform:              "linux",
		Architecture:          "amd64",
		PathStyle:             true,
		CAFile:                "/run/credentials/gha-diagnostic-exporter.service/rustfs-ca.pem",
		AccessKeyFile:         "/run/credentials/gha-diagnostic-exporter.service/rustfs-access-key",
		SecretKeyFile:         "/run/credentials/gha-diagnostic-exporter.service/rustfs-secret-key",
		RequestTimeoutSeconds: 20,
		SourceRetentionHours:  168,
		MaxBundleBytes:        16 * 1024 * 1024,
		MaxDecompressedBytes:  20 * 1024 * 1024,
	}
}

func TestConfigValidate(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*Config)
		want string
	}{
		{"legacy schema", func(c *Config) { c.SchemaVersion = 3 }, "must be 4"},
		{"unsorted trusts", func(c *Config) { c.Trusts = []string{"trusted", "release"} }, "strictly sorted"},
		{"missing trusts", func(c *Config) { c.Trusts = nil }, "between 1 and 16"},
		{"production stage", func(c *Config) { c.DeploymentStage = "production" }, "must remain canary"},
		{"plaintext endpoint", func(c *Config) { c.Endpoint = "http://198.51.100.1:9002" }, "HTTPS"},
		{"virtual host", func(c *Config) { c.PathStyle = false }, "must be true"},
		{"credential outside mount", func(c *Config) { c.SecretKeyFile = "/etc/secret" }, "credential path"},
		{"wrong source owner", func(c *Config) { c.SourceOwner = "root" }, "must be garm"},
		{"source state overlap", func(c *Config) { c.StateDirectory = c.SourceDirectory + "/state" }, "must not overlap"},
		{"oversize decompression", func(c *Config) { c.MaxDecompressedBytes = 65 * 1024 * 1024 }, "64 MiB"},
		{"dot prefix", func(c *Config) { c.Prefix = "." }, "normalized relative"},
		{"dot repository owner", func(c *Config) { c.Repositories = []string{"../repository"} }, "bounded exact identity"},
		{"unsorted repositories", func(c *Config) { c.Repositories = []string{"z/repo", "a/repo"} }, "strictly sorted"},
		{"unknown account syntax", func(c *Config) { c.AccountScopes = []string{"../account"} }, "bounded exact identity"},
		{"missing pools", func(c *Config) { c.Pools = nil }, "between 1 and 16"},
		{"dot pool", func(c *Config) { c.Pools = []string{".."} }, "bounded identity"},
		{"unsorted pools", func(c *Config) {
			c.Pools = []string{"nddev-linux-standard", "nddev-linux-integration"}
		}, "strictly sorted and unique"},
		{"duplicate pools", func(c *Config) {
			c.Pools = []string{"nddev-linux-standard", "nddev-linux-standard"}
		}, "strictly sorted and unique"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.edit(&config)
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestConfigValidationErrorsAreDeterministic(t *testing.T) {
	config := validConfig()
	config.Pools = nil
	config.Trusts = nil
	first := config.Validate().Error()
	for range 20 {
		if got := config.Validate().Error(); got != first {
			t.Fatalf("Validate() error changed between calls:\n%s\n%s", first, got)
		}
	}
}

func TestConfigAllowsOnlyExactReviewedPools(t *testing.T) {
	config := validConfig()
	for _, pool := range config.Pools {
		if !config.AllowsPool(pool) {
			t.Errorf("reviewed pool %q was rejected", pool)
		}
	}
	for _, pool := range []string{"", "..", "nddev-linux-release", "nddev-linux-standard-extra"} {
		if config.AllowsPool(pool) {
			t.Errorf("unreviewed pool %q was accepted", pool)
		}
	}
	config.Pools = []string{"nddev-linux-standard", "nddev-linux-integration"}
	if config.AllowsPool("nddev-linux-standard") {
		t.Fatal("an unsorted allowlist was accepted")
	}
	config.Pools = []string{"nddev-linux-standard", "nddev-linux-standard"}
	if config.AllowsPool("nddev-linux-standard") {
		t.Fatal("a duplicate allowlist was accepted")
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	directory := t.TempDir()
	filename := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(filename, []byte("schema_version: 1\nunknown: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(filename); err == nil || !strings.Contains(err.Error(), "field unknown not found") {
		t.Fatalf("LoadConfig() error = %v", err)
	}
}
