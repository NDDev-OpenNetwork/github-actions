package rustfscache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadCanonicalConfig(t *testing.T) {
	t.Parallel()

	config, err := Load(filepath.Join("..", "..", "config", "rustfs-cache-identities.yaml"))
	if err != nil {
		t.Fatalf("Load canonical config: %v", err)
	}
	if err := config.ValidateProductionPaths(); err != nil {
		t.Fatalf("ValidateProductionPaths: %v", err)
	}
}

func TestValidateAcceptsDeploymentIdentityAndLiteralEndpoint(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config", "rustfs-cache-identities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config.Endpoint = "https://192.0.2.1:9002"
	for index := range config.Identities {
		config.Identities[index].Prefix = strings.Replace(
			config.Identities[index].Prefix,
			"example-org/example-actions/",
			"example-tenant/example-repository/",
			1,
		)
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	repository, err := config.Repository()
	if err != nil {
		t.Fatal(err)
	}
	if repository != "example-tenant/example-repository" {
		t.Fatalf("repository = %q", repository)
	}
}

func TestConfigRejectsTrustContractDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Config){
		"endpoint": func(config *Config) { config.Endpoint = "https://rustfs.invalid:9002" },
		"region":   func(config *Config) { config.Region = "eu-west-1" },
		"bucket":   func(config *Config) { config.Bucket = "other-cache" },
		"quota":    func(config *Config) { config.QuotaBytes-- },
		"prefix": func(config *Config) {
			config.Identities[0].Prefix = "example-org/example-actions/trust/other"
		},
		"mode": func(config *Config) { config.Identities[0].Mode = "read-only" },
		"retention": func(config *Config) {
			config.Identities[0].RetentionDays++
		},
		"duplicate role": func(config *Config) {
			config.Identities[1].Role = config.Identities[0].Role
		},
		"duplicate policy": func(config *Config) {
			config.Identities[1].Policy = config.Identities[0].Policy
		},
		"renamed policy": func(config *Config) {
			config.Identities[0].Policy += "-renamed"
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := testConfig(t, t.TempDir())
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("Validate unexpectedly accepted drift")
			}
		})
	}
}

func TestProductionPathsRejectTemporaryCredentialDirectory(t *testing.T) {
	t.Parallel()

	config := testConfig(t, t.TempDir())
	if err := config.Validate(); err != nil {
		t.Fatalf("Validate test config: %v", err)
	}
	config.CAFile = DefaultCAFile
	config.RootAccessKeyFile = DefaultRootAccessKeyFile
	config.RootSecretKeyFile = DefaultRootSecretKeyFile
	if err := config.ValidateProductionPaths(); err == nil || !strings.Contains(err.Error(), DefaultCredentialsDirectory) {
		t.Fatalf("ValidateProductionPaths error = %v", err)
	}
}

func TestProductionPathsRejectLocatorDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Config){
		"CA":          func(config *Config) { config.CAFile += ".other" },
		"root access": func(config *Config) { config.RootAccessKeyFile += ".other" },
		"root secret": func(config *Config) { config.RootSecretKeyFile += ".other" },
		"managed":     func(config *Config) { config.CredentialsDirectory += ".other" },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := testConfig(t, t.TempDir())
			config.CAFile = DefaultCAFile
			config.RootAccessKeyFile = DefaultRootAccessKeyFile
			config.RootSecretKeyFile = DefaultRootSecretKeyFile
			config.CredentialsDirectory = DefaultCredentialsDirectory
			mutate(&config)
			if err := config.ValidateProductionPaths(); err == nil {
				t.Fatal("ValidateProductionPaths accepted locator drift")
			}
		})
	}
}

func TestLoadRejectsUnknownFieldAndMultipleDocuments(t *testing.T) {
	t.Parallel()

	canonical, err := os.ReadFile(filepath.Join("..", "..", "config", "rustfs-cache-identities.yaml"))
	if err != nil {
		t.Fatalf("read canonical config: %v", err)
	}
	tests := map[string][]byte{
		"unknown field":      append(append([]byte(nil), canonical...), []byte("unknown_field: true\n")...),
		"multiple documents": append(append([]byte(nil), canonical...), []byte("---\nschema_version: 1\n")...),
	}
	for name, content := range tests {
		name, content := name, content
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load unexpectedly accepted malformed config")
			}
		})
	}
}

func testConfig(t *testing.T, directory string) Config {
	t.Helper()
	return Config{
		SchemaVersion:        SchemaVersion,
		Endpoint:             "https://198.51.100.1:9002",
		Region:               "us-east-1",
		CAFile:               filepath.Join(directory, "ca.pem"),
		RootAccessKeyFile:    filepath.Join(directory, "root-access-key"),
		RootSecretKeyFile:    filepath.Join(directory, "root-secret-key"),
		CredentialsDirectory: directory,
		Bucket:               "github-actions-cache",
		QuotaBytes:           64 * 1024 * 1024 * 1024,
		Identities: []Identity{
			{Role: "trusted-writer", Policy: "gha-cache-github-actions-trusted", Prefix: "example-org/example-actions/trust/trusted", Mode: "read-write", RetentionDays: 30},
			{Role: "untrusted-writer", Policy: "gha-cache-github-actions-untrusted", Prefix: "example-org/example-actions/trust/untrusted", Mode: "read-write", RetentionDays: 7},
			{Role: "promoter", Policy: "gha-cache-github-actions-promoter", Prefix: "example-org/example-actions/trust/promoted", Mode: "read-write", RetentionDays: 90},
			{Role: "release-reader", Policy: "gha-cache-github-actions-release", Prefix: "example-org/example-actions/trust/promoted", Mode: "read-only", RetentionDays: 90},
		},
	}
}
