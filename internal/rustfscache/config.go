package rustfscache

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion               = 1
	DefaultConfigPath           = "/etc/gha-fleet/rustfs-cache-identities.yaml"
	DefaultCredentialsDirectory = "/etc/garm/cache"
	DefaultCAFile               = "/etc/gha-fleet/trust/rustfs-ca.pem"
	DefaultRootAccessKeyFile    = "/etc/gha-fleet/secrets/rustfs-access-key"
	DefaultRootSecretKeyFile    = "/etc/gha-fleet/secrets/rustfs-secret-key"
	DefaultClaimJournalFile     = "/var/lib/gha-cache-broker/claims.json"
	DefaultClaimJournalLockFile = "/var/lib/gha-cache-broker/claims.lock"
	MaximumConfigBytes          = 64 * 1024
)

var (
	namePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
	bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{2,255}$`)
)

type Config struct {
	SchemaVersion        int        `yaml:"schema_version" json:"schema_version"`
	Endpoint             string     `yaml:"endpoint" json:"endpoint"`
	Region               string     `yaml:"region" json:"region"`
	CAFile               string     `yaml:"ca_file" json:"ca_file"`
	RootAccessKeyFile    string     `yaml:"root_access_key_file" json:"root_access_key_file"`
	RootSecretKeyFile    string     `yaml:"root_secret_key_file" json:"root_secret_key_file"`
	CredentialsDirectory string     `yaml:"credentials_directory" json:"credentials_directory"`
	Bucket               string     `yaml:"bucket" json:"bucket"`
	QuotaBytes           int64      `yaml:"quota_bytes" json:"quota_bytes"`
	ClaimEndpoint        string     `yaml:"claim_endpoint" json:"claim_endpoint"`
	ClaimJournalFile     string     `yaml:"claim_journal_file" json:"claim_journal_file"`
	ClaimJournalLockFile string     `yaml:"claim_journal_lock_file" json:"claim_journal_lock_file"`
	Identities           []Identity `yaml:"identities" json:"identities"`
}

type Identity struct {
	Role          string `yaml:"role" json:"role"`
	Policy        string `yaml:"policy" json:"policy"`
	Prefix        string `yaml:"prefix" json:"prefix"`
	Mode          string `yaml:"mode" json:"mode"`
	RetentionDays int    `yaml:"retention_days" json:"retention_days"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read RustFS cache identity config: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaximumConfigBytes {
		return Config{}, fmt.Errorf("RustFS cache identity config must contain 1..%d bytes", MaximumConfigBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode RustFS cache identity config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, fmt.Errorf("RustFS cache identity config must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode trailing RustFS cache identity config: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if err := ValidateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Region != "us-east-1" {
		return fmt.Errorf("region must be us-east-1")
	}
	paths := []struct {
		label string
		value string
	}{
		{"ca_file", c.CAFile},
		{"root_access_key_file", c.RootAccessKeyFile},
		{"root_secret_key_file", c.RootSecretKeyFile},
		{"credentials_directory", c.CredentialsDirectory},
	}
	for _, path := range paths {
		if !filepath.IsAbs(path.value) || filepath.Clean(path.value) != path.value || path.value == "/" {
			return fmt.Errorf("%s must be an absolute clean non-root path", path.label)
		}
	}
	if !bucketPattern.MatchString(c.Bucket) || !strings.HasSuffix(c.Bucket, "-cache") {
		return fmt.Errorf("bucket must be a valid name ending in -cache")
	}
	if c.QuotaBytes < 2*1024*1024*1024 || c.QuotaBytes > 1024*1024*1024*1024 {
		return fmt.Errorf("quota_bytes must be between 2 GiB and 1 TiB")
	}
	if err := validateClaimEndpoint(c.ClaimEndpoint); err != nil {
		return err
	}
	for label, value := range map[string]string{"claim_journal_file": c.ClaimJournalFile, "claim_journal_lock_file": c.ClaimJournalLockFile} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
			return fmt.Errorf("%s must be an absolute clean non-root path", label)
		}
	}
	if len(c.Identities) != 4 {
		return fmt.Errorf("identities must contain exactly four trust roles")
	}
	if len(c.Identities) == 0 {
		return fmt.Errorf("identities must contain exactly four trust roles")
	}
	repository, _, err := cachenamespace.ParsePrefixRoot(c.Identities[0].Prefix)
	if err != nil {
		return fmt.Errorf("derive cache repository identity: %w", err)
	}
	policyNamespace := "gha-cache-" + strings.TrimSuffix(c.Bucket, "-cache")
	wanted := map[string]struct {
		mode      string
		policy    string
		prefix    string
		retention int
	}{
		"trusted-writer":   {"read-write", policyNamespace + "-trusted", mustPrefix(repository, cachenamespace.Trusted), 30},
		"untrusted-writer": {"read-write", policyNamespace + "-untrusted", mustPrefix(repository, cachenamespace.Untrusted), 7},
		"promoter":         {"read-write", policyNamespace + "-promoter", mustPrefix(repository, cachenamespace.Promoted), 90},
		"release-reader":   {"read-only", policyNamespace + "-release", mustPrefix(repository, cachenamespace.Promoted), 90},
	}
	seenRoles := make(map[string]struct{}, len(c.Identities))
	seenPolicies := make(map[string]struct{}, len(c.Identities))
	for _, identity := range c.Identities {
		expected, exists := wanted[identity.Role]
		if !exists {
			return fmt.Errorf("identity role %q is unsupported", identity.Role)
		}
		if _, duplicate := seenRoles[identity.Role]; duplicate {
			return fmt.Errorf("identity role %q is duplicated", identity.Role)
		}
		seenRoles[identity.Role] = struct{}{}
		if !namePattern.MatchString(identity.Policy) || !strings.HasPrefix(identity.Policy, policyNamespace+"-") {
			return fmt.Errorf("identity %s policy is outside the managed namespace", identity.Role)
		}
		if _, duplicate := seenPolicies[identity.Policy]; duplicate {
			return fmt.Errorf("identity policy %q is duplicated", identity.Policy)
		}
		seenPolicies[identity.Policy] = struct{}{}
		if !prefixPattern.MatchString(identity.Prefix) || strings.Contains(identity.Prefix, "//") ||
			strings.HasSuffix(identity.Prefix, "/") || filepath.Clean(identity.Prefix) != identity.Prefix {
			return fmt.Errorf("identity %s prefix is not normalized", identity.Role)
		}
		if identity.Mode != expected.mode || identity.Policy != expected.policy || identity.Prefix != expected.prefix ||
			identity.RetentionDays != expected.retention {
			return fmt.Errorf("identity %s trust contract drifted", identity.Role)
		}
	}
	return nil
}

func validateClaimEndpoint(value string) error {
	endpoint, err := url.ParseRequestURI(value)
	if err != nil {
		return fmt.Errorf("claim_endpoint must be an HTTPS /api/v1/cache/claim URL using a literal IP and explicit port")
	}
	host, port, splitErr := net.SplitHostPort(endpoint.Host)
	address := net.ParseIP(host)
	if endpoint.Scheme != "https" || splitErr != nil || address == nil || port == "" || address.IsUnspecified() || address.IsMulticast() || endpoint.Path != "/api/v1/cache/claim" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return fmt.Errorf("claim_endpoint must be an HTTPS /api/v1/cache/claim URL using a literal IP and explicit port")
	}
	return nil
}

// ValidateEndpoint accepts the deployment address while refusing DNS,
// credentials, alternate ports and non-origin URL components. The exact IP is
// estate data and must not be compiled into this public package.
func ValidateEndpoint(value string) error {
	endpoint, err := url.ParseRequestURI(value)
	if err != nil {
		return fmt.Errorf("endpoint must be an HTTPS origin using a literal unicast IP and port 9002")
	}
	host, port, splitErr := net.SplitHostPort(endpoint.Host)
	address := net.ParseIP(host)
	if endpoint.Scheme != "https" || splitErr != nil || address == nil || port != "9002" ||
		address.IsUnspecified() || address.IsMulticast() || endpoint.Path != "" ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.User != nil {
		return fmt.Errorf("endpoint must be an HTTPS origin using a literal unicast IP and port 9002")
	}
	return nil
}

func mustPrefix(repository string, class cachenamespace.TrustClass) string {
	prefix, err := cachenamespace.PrefixRootFor(repository, class)
	if err != nil {
		panic(err)
	}
	return prefix
}

// Repository returns the exact repository authorized by all configured cache
// identities. Validate proves every role shares this root.
func (c Config) Repository() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	repository, _, err := cachenamespace.ParsePrefixRoot(c.Identities[0].Prefix)
	return repository, err
}

// ValidateProductionPaths pins mutable credential material to the reviewed host
// boundary. Validate intentionally permits a temporary directory so the
// filesystem and reconciliation contracts can be exercised without root.
func (c Config) ValidateProductionPaths() error {
	paths := []struct {
		label    string
		actual   string
		expected string
	}{
		{"ca_file", c.CAFile, DefaultCAFile},
		{"root_access_key_file", c.RootAccessKeyFile, DefaultRootAccessKeyFile},
		{"root_secret_key_file", c.RootSecretKeyFile, DefaultRootSecretKeyFile},
		{"claim_journal_file", c.ClaimJournalFile, DefaultClaimJournalFile},
		{"claim_journal_lock_file", c.ClaimJournalLockFile, DefaultClaimJournalLockFile},
	}
	for _, path := range paths {
		if path.actual != path.expected {
			return fmt.Errorf("%s must be %s", path.label, path.expected)
		}
	}
	if c.CredentialsDirectory != DefaultCredentialsDirectory && !strings.HasPrefix(c.CredentialsDirectory, DefaultCredentialsDirectory+"/") {
		return fmt.Errorf("credentials_directory must be %s or one private child", DefaultCredentialsDirectory)
	}
	return nil
}

func (c Config) SortedIdentities() []Identity {
	identities := append([]Identity(nil), c.Identities...)
	sort.Slice(identities, func(i, j int) bool { return identities[i].Role < identities[j].Role })
	return identities
}
