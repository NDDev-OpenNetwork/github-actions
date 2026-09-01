package cachebroker

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	"gopkg.in/yaml.v3"
)

var brokerBucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

const (
	SchemaVersion     = 1
	MaximumConfigSize = 256 * 1024
	DefaultConfigPath = "/etc/gha-fleet/cache-broker.yaml"
)

type Config struct {
	SchemaVersion    int    `yaml:"schema_version" json:"schema_version"`
	ListenAddress    string `yaml:"listen_address" json:"listen_address"`
	Endpoint         string `yaml:"endpoint" json:"endpoint"`
	Region           string `yaml:"region" json:"region"`
	Bucket           string `yaml:"bucket" json:"bucket"`
	CAFile           string `yaml:"ca_file" json:"ca_file"`
	JournalFile      string `yaml:"journal_file" json:"journal_file"`
	JournalLock      string `yaml:"journal_lock_file" json:"journal_lock_file"`
	QueueJournalFile string `yaml:"queue_journal_file,omitempty" json:"queue_journal_file,omitempty"`
	QueueJournalLock string `yaml:"queue_journal_lock_file,omitempty" json:"queue_journal_lock_file,omitempty"`
	// ProviderJournalFile is the provider's execution ledger, read without its
	// lock. It answers one question this process cannot otherwise ask: which
	// runners still hold a lease. A running queue intent whose runner holds
	// none has finished, and nothing else in the system reclaims it.
	ProviderJournalFile string `yaml:"provider_journal_file,omitempty" json:"provider_journal_file,omitempty"`
	// BuildcacheRegistry is the member-local zot origin that holds BuildKit
	// layer caches (buildcache/<owner>/<repo>/<class>). The address is the
	// same on every member's bridge, so one configured origin serves the
	// whole fleet. Empty disables buildcache delivery.
	BuildcacheRegistry string       `yaml:"buildcache_registry,omitempty" json:"buildcache_registry,omitempty"`
	Repositories       []Repository `yaml:"repositories" json:"repositories"`
}

type Repository struct {
	Name   string     `yaml:"name" json:"name"`
	Bucket string     `yaml:"bucket,omitempty" json:"bucket,omitempty"`
	Roles  []Identity `yaml:"roles" json:"roles"`
}

type Identity struct {
	Role          string `yaml:"role" json:"role"`
	Mode          string `yaml:"mode" json:"mode"`
	Prefix        string `yaml:"prefix" json:"prefix"`
	AccessKeyFile string `yaml:"access_key_file" json:"access_key_file"`
	SecretKeyFile string `yaml:"secret_key_file" json:"secret_key_file"`
	// BuildcacheUsernameFile and BuildcachePasswordFile, when both set, add a
	// registry credential for the role's buildcache namespace to the same
	// delivery. Both or neither; delivery also requires buildcache_registry.
	BuildcacheUsernameFile string `yaml:"buildcache_username_file,omitempty" json:"buildcache_username_file,omitempty"`
	BuildcachePasswordFile string `yaml:"buildcache_password_file,omitempty" json:"buildcache_password_file,omitempty"`
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read cache broker config: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaximumConfigSize {
		return Config{}, fmt.Errorf("cache broker config must contain 1..%d bytes", MaximumConfigSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode cache broker config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, errors.New("cache broker config must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode trailing cache broker config: %w", err)
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
	host, port, err := net.SplitHostPort(c.ListenAddress)
	if err != nil || net.ParseIP(host) == nil || port == "" || net.ParseIP(host).IsUnspecified() {
		return errors.New("listen_address must use one literal non-wildcard IP and explicit port")
	}
	if err := rustfscache.ValidateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Region != "us-east-1" || c.Bucket != "github-actions-cache" {
		return errors.New("region/bucket must be us-east-1/github-actions-cache")
	}
	for label, value := range map[string]string{"ca_file": c.CAFile, "journal_file": c.JournalFile, "journal_lock_file": c.JournalLock} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
			return fmt.Errorf("%s must be an absolute clean non-root path", label)
		}
	}
	if (c.QueueJournalFile == "") != (c.QueueJournalLock == "") {
		return errors.New("queue_journal_file and queue_journal_lock_file must be configured together")
	}
	if c.QueueJournalFile != "" {
		for label, value := range map[string]string{"queue_journal_file": c.QueueJournalFile, "queue_journal_lock_file": c.QueueJournalLock} {
			if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
				return fmt.Errorf("%s must be an absolute clean non-root path", label)
			}
		}
		if filepath.Dir(c.QueueJournalFile) != filepath.Dir(c.QueueJournalLock) || c.QueueJournalFile == c.QueueJournalLock {
			return errors.New("queue journal and lock must be distinct siblings")
		}
	}
	if c.ProviderJournalFile != "" {
		if !filepath.IsAbs(c.ProviderJournalFile) || filepath.Clean(c.ProviderJournalFile) != c.ProviderJournalFile || c.ProviderJournalFile == "/" {
			return errors.New("provider_journal_file must be an absolute clean non-root path")
		}
		if c.QueueJournalFile == "" {
			return errors.New("provider_journal_file reclaims running queue intents and needs the queue journal")
		}
		if c.ProviderJournalFile == c.QueueJournalFile || c.ProviderJournalFile == c.QueueJournalLock ||
			c.ProviderJournalFile == c.JournalFile || c.ProviderJournalFile == c.JournalLock {
			return errors.New("provider_journal_file must be distinct from every journal this process writes")
		}
	}
	if c.BuildcacheRegistry != "" {
		parsed, err := url.Parse(c.BuildcacheRegistry)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" ||
			parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("buildcache_registry must be a bare HTTP(S) origin")
		}
	}
	if len(c.Repositories) == 0 {
		return errors.New("repositories must not be empty")
	}
	seenRepositories := map[string]struct{}{}
	previous := ""
	for _, repository := range c.Repositories {
		if _, _, err := splitRepository(repository.Name); err != nil {
			return err
		}
		if repository.Name <= previous {
			return errors.New("repositories must be sorted and unique")
		}
		if repository.Bucket != "" && (!brokerBucketPattern.MatchString(repository.Bucket) || !strings.HasSuffix(repository.Bucket, "-cache")) {
			return fmt.Errorf("repository %q bucket must end in -cache", repository.Name)
		}
		previous = repository.Name
		if _, duplicate := seenRepositories[repository.Name]; duplicate {
			return fmt.Errorf("repository %q is duplicated", repository.Name)
		}
		seenRepositories[repository.Name] = struct{}{}
		if len(repository.Roles) != 3 {
			return fmt.Errorf("repository %q must define trusted-writer, untrusted-writer and release-reader", repository.Name)
		}
		seenRoles := map[string]struct{}{}
		for _, identity := range repository.Roles {
			if _, duplicate := seenRoles[identity.Role]; duplicate {
				return fmt.Errorf("repository %q role %q is duplicated", repository.Name, identity.Role)
			}
			seenRoles[identity.Role] = struct{}{}
			class, mode, ok := roleContract(identity.Role)
			if !ok || identity.Mode != mode {
				return fmt.Errorf("repository %q role %q has invalid mode", repository.Name, identity.Role)
			}
			wanted, _ := cachenamespace.PrefixRootFor(repository.Name, class)
			if identity.Prefix != wanted {
				return fmt.Errorf("repository %q role %q prefix must be %q", repository.Name, identity.Role, wanted)
			}
			for _, value := range []string{identity.AccessKeyFile, identity.SecretKeyFile} {
				if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
					return fmt.Errorf("repository %q role %q credential path is invalid", repository.Name, identity.Role)
				}
			}
			if (identity.BuildcacheUsernameFile == "") != (identity.BuildcachePasswordFile == "") {
				return fmt.Errorf("repository %q role %q buildcache credential files must be configured together", repository.Name, identity.Role)
			}
			if identity.BuildcacheUsernameFile != "" {
				if c.BuildcacheRegistry == "" {
					return fmt.Errorf("repository %q role %q carries buildcache credentials without buildcache_registry", repository.Name, identity.Role)
				}
				for _, value := range []string{identity.BuildcacheUsernameFile, identity.BuildcachePasswordFile} {
					if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
						return fmt.Errorf("repository %q role %q buildcache credential path is invalid", repository.Name, identity.Role)
					}
				}
			}
		}
		for _, role := range []string{"release-reader", "trusted-writer", "untrusted-writer"} {
			if _, exists := seenRoles[role]; !exists {
				return fmt.Errorf("repository %q omits role %q", repository.Name, role)
			}
		}
	}
	return nil
}

func (c Config) Identity(repository, role string) (Identity, bool) {
	index := sort.Search(len(c.Repositories), func(i int) bool { return c.Repositories[i].Name >= repository })
	if index == len(c.Repositories) || c.Repositories[index].Name != repository {
		return Identity{}, false
	}
	for _, identity := range c.Repositories[index].Roles {
		if identity.Role == role {
			return identity, true
		}
	}
	return Identity{}, false
}

func (c Config) Delivery(repository, role string) (Repository, Identity, bool) {
	index := sort.Search(len(c.Repositories), func(i int) bool { return c.Repositories[i].Name >= repository })
	if index == len(c.Repositories) || c.Repositories[index].Name != repository {
		return Repository{}, Identity{}, false
	}
	selected := c.Repositories[index]
	if selected.Bucket == "" {
		selected.Bucket = c.Bucket
	}
	for _, identity := range selected.Roles {
		if identity.Role == role {
			return selected, identity, true
		}
	}
	return Repository{}, Identity{}, false
}

func roleContract(role string) (cachenamespace.TrustClass, string, bool) {
	switch role {
	case "trusted-writer":
		return cachenamespace.Trusted, "read-write", true
	case "untrusted-writer":
		return cachenamespace.Untrusted, "read-write", true
	case "release-reader":
		return cachenamespace.Promoted, "read-only", true
	default:
		return "", "", false
	}
}

func splitRepository(value string) (string, string, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(value, "\\ ") {
		return "", "", fmt.Errorf("repository %q must be owner/name", value)
	}
	return parts[0], parts[1], nil
}

func ValidateClaimEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "/api/v1/cache/claim" {
		return errors.New("claim endpoint must be an uncredentialed HTTPS /api/v1/cache/claim URL")
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || net.ParseIP(host) == nil || port == "" {
		return errors.New("claim endpoint must use a literal IP and explicit port")
	}
	return nil
}
