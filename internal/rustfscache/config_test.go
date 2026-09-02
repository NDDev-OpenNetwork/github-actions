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

func TestAccessKeysAreRepositoryScopedOutsideLegacyBucket(t *testing.T) {
	left := testConfig(t, t.TempDir())
	right := left
	right.Bucket = "example-estate-cache"
	right.Identities = append([]Identity(nil), left.Identities...)
	for index := range right.Identities {
		right.Identities[index].Prefix = strings.Replace(right.Identities[index].Prefix, "example-org/example-actions/", "example-org/example-estate/", 1)
		right.Identities[index].Policy = strings.Replace(right.Identities[index].Policy, "gha-cache-github-actions-", "gha-cache-example-estate-", 1)
	}
	legacy := credentialSkeletons(left)
	scoped := credentialSkeletons(right)
	if legacy[0].accessKey == scoped[0].accessKey {
		t.Fatal("second repository reused legacy role-only access key")
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
		"bucket":   func(config *Config) { config.Bucket = "other" },
		"quota":    func(config *Config) { config.QuotaBytes = 1024 },
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
	config.ClaimJournalFile = DefaultClaimJournalFile
	config.ClaimJournalLockFile = DefaultClaimJournalLockFile
	if err := config.ValidateProductionPaths(); err == nil || !strings.Contains(err.Error(), DefaultCredentialsDirectory) {
		t.Fatalf("ValidateProductionPaths error = %v", err)
	}
}

func TestProductionPathsRejectLocatorDrift(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Config){
		"CA":            func(config *Config) { config.CAFile += ".other" },
		"root access":   func(config *Config) { config.RootAccessKeyFile += ".other" },
		"root secret":   func(config *Config) { config.RootSecretKeyFile += ".other" },
		"managed":       func(config *Config) { config.CredentialsDirectory += ".other" },
		"claim journal": func(config *Config) { config.ClaimJournalFile += ".other" },
		"claim lock":    func(config *Config) { config.ClaimJournalLockFile += ".other" },
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
			config.ClaimJournalFile = DefaultClaimJournalFile
			config.ClaimJournalLockFile = DefaultClaimJournalLockFile
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
		ClaimEndpoint:        "https://198.51.100.1:9443/api/v1/cache/claim",
		ClaimJournalFile:     filepath.Join(directory, "cache-claims.json"),
		ClaimJournalLockFile: filepath.Join(directory, "cache-claims.lock"),
		Identities: []Identity{
			{Role: "trusted-writer", Policy: "gha-cache-github-actions-trusted", Prefix: "example-org/example-actions/trust/trusted", Mode: "read-write", RetentionDays: 30,
				ActionsCachePrefix: "cache/example-org/example-actions"},
			{Role: "untrusted-writer", Policy: "gha-cache-github-actions-untrusted", Prefix: "example-org/example-actions/trust/untrusted", Mode: "read-write", RetentionDays: 7},
			{Role: "promoter", Policy: "gha-cache-github-actions-promoter", Prefix: "example-org/example-actions/trust/promoted", Mode: "read-write", RetentionDays: 90},
			{Role: "release-reader", Policy: "gha-cache-github-actions-release", Prefix: "example-org/example-actions/trust/promoted", Mode: "read-only", RetentionDays: 90},
		},
	}
}

// The trusted writer may carry runs-on/cache's cache/<owner>/<repo> prefix,
// and only it: an untrusted build keeps GitHub's cache so nothing it writes
// is restored by a trusted build, and the prefix is exactly the layout the
// action hard-codes, never a hand-picked one.
func TestOnlyTheTrustedWriterMayCarryTheActionsCachePrefix(t *testing.T) {
	config, err := Load(filepath.Join("..", "..", "config", "rustfs-cache-identities.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	config.Endpoint = "https://192.0.2.1:9002"
	index := func(role string) int {
		for i, identity := range config.Identities {
			if identity.Role == role {
				return i
			}
		}
		t.Fatalf("role %s is missing", role)
		return -1
	}
	trusted, untrusted := index("trusted-writer"), index("untrusted-writer")

	accepted := config
	accepted.Identities = append([]Identity(nil), config.Identities...)
	accepted.Identities[trusted].ActionsCachePrefix = "cache/example-org/example-actions"
	if err := accepted.Validate(); err != nil {
		t.Fatalf("the trusted writer's actions cache prefix must validate: %v", err)
	}

	wrongPrefix := config
	wrongPrefix.Identities = append([]Identity(nil), config.Identities...)
	wrongPrefix.Identities[trusted].ActionsCachePrefix = "cache/example-org/other"
	if err := wrongPrefix.Validate(); err == nil || !strings.Contains(err.Error(), "actions cache prefix must be") {
		t.Fatalf("a prefix outside the repository must be refused, got %v", err)
	}

	onUntrusted := config
	onUntrusted.Identities = append([]Identity(nil), config.Identities...)
	onUntrusted.Identities[untrusted].ActionsCachePrefix = "cache/example-org/example-actions"
	if err := onUntrusted.Validate(); err == nil || !strings.Contains(err.Error(), "only the trusted writer may") {
		t.Fatalf("the untrusted writer must never carry the actions cache prefix, got %v", err)
	}
}
