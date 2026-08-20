package diagnosticexport

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

const (
	maxConfigBytes     = 64 * 1024
	maxConfiguredPools = 16
)

var (
	bucketPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	identityPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	regionPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Config is deliberately narrow while RustFS remains a canary backend. It
// contains no secret values; systemd credential paths point at the per-unit
// credential mount.
type Config struct {
	SchemaVersion         int      `yaml:"schema_version" json:"schema_version"`
	DeploymentStage       string   `yaml:"deployment_stage" json:"deployment_stage"`
	SourceDirectory       string   `yaml:"source_directory" json:"source_directory"`
	SourceOwner           string   `yaml:"source_owner" json:"source_owner"`
	StateDirectory        string   `yaml:"state_directory" json:"state_directory"`
	Endpoint              string   `yaml:"endpoint" json:"endpoint"`
	Region                string   `yaml:"region" json:"region"`
	Bucket                string   `yaml:"bucket" json:"bucket"`
	Prefix                string   `yaml:"prefix" json:"prefix"`
	Repositories          []string `yaml:"repositories" json:"repositories"`
	AccountScopes         []string `yaml:"account_scopes" json:"account_scopes"`
	Pools                 []string `yaml:"pools" json:"pools"`
	Trusts                []string `yaml:"trusts" json:"trusts"`
	Platform              string   `yaml:"platform" json:"platform"`
	Architecture          string   `yaml:"architecture" json:"architecture"`
	PathStyle             bool     `yaml:"path_style" json:"path_style"`
	CAFile                string   `yaml:"ca_file" json:"ca_file"`
	AccessKeyFile         string   `yaml:"access_key_file" json:"access_key_file"`
	SecretKeyFile         string   `yaml:"secret_key_file" json:"secret_key_file"`
	RequestTimeoutSeconds int      `yaml:"request_timeout_seconds" json:"request_timeout_seconds"`
	SourceRetentionHours  int      `yaml:"source_retention_hours" json:"source_retention_hours"`
	MaxBundleBytes        int64    `yaml:"max_bundle_bytes" json:"max_bundle_bytes"`
	MaxDecompressedBytes  int64    `yaml:"max_decompressed_bytes" json:"max_decompressed_bytes"`
}

func LoadConfig(filename string) (Config, error) {
	if !filepath.IsAbs(filename) {
		return Config{}, errors.New("diagnostic exporter config path must be absolute")
	}
	fd, err := unix.Open(filename, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Config{}, fmt.Errorf("open diagnostic exporter config: %w", err)
	}
	file := os.NewFile(uintptr(fd), filename)
	if file == nil {
		_ = unix.Close(fd)
		return Config{}, errors.New("open diagnostic exporter config: invalid file descriptor")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return Config{}, fmt.Errorf("stat diagnostic exporter config: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) ||
		stat.Mode&0o007 != 0 || stat.Size < 1 || stat.Size > maxConfigBytes {
		return Config{}, errors.New("diagnostic exporter config ownership, type, link count, mode or size is unsafe")
	}
	content, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil {
		return Config{}, fmt.Errorf("read diagnostic exporter config: %w", err)
	}
	if int64(len(content)) != stat.Size {
		return Config{}, errors.New("diagnostic exporter config changed while reading")
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return Config{}, fmt.Errorf("restat diagnostic exporter config: %w", err)
	}
	if stat.Dev != after.Dev || stat.Ino != after.Ino || stat.Size != after.Size ||
		stat.Mode != after.Mode || stat.Uid != after.Uid || stat.Gid != after.Gid ||
		stat.Mtim != after.Mtim || stat.Ctim != after.Ctim || after.Nlink != 1 {
		return Config{}, errors.New("diagnostic exporter config changed while reading")
	}
	return ParseConfig(content)
}

// ParseConfig validates committed deployment configuration without applying
// the runtime ownership checks enforced by LoadConfig.
func ParseConfig(content []byte) (Config, error) {
	if len(content) == 0 || len(content) > maxConfigBytes {
		return Config{}, fmt.Errorf("diagnostic exporter config must contain 1..%d bytes", maxConfigBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode diagnostic exporter config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Config{}, errors.New("diagnostic exporter config must contain one YAML document")
		}
		return Config{}, fmt.Errorf("decode diagnostic exporter config trailer: %w", err)
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	var problems []string
	add := func(field, message string) { problems = append(problems, field+": "+message) }
	if c.SchemaVersion != 4 {
		add("schema_version", "must be 4 for exact multi-tenant trust scopes")
	}
	if !StageAccepted(c.DeploymentStage) {
		add("deployment_stage", "must remain "+strings.Join(AcceptedStages(), " or ")+" until RustFS production gates pass")
	}
	validateBoundedDirectory := func(field, value string) {
		if !filepath.IsAbs(value) || filepath.Clean(value) == string(filepath.Separator) {
			add(field, "must be an absolute bounded directory")
		}
	}
	validateBoundedDirectory("source_directory", c.SourceDirectory)
	validateBoundedDirectory("state_directory", c.StateDirectory)
	if directoriesOverlap(c.SourceDirectory, c.StateDirectory) {
		add("state_directory", "must not overlap the diagnostic source directory")
	}
	if c.SourceOwner != "garm" {
		add("source_owner", "must be garm for the private provider spool")
	}
	endpoint, err := url.ParseRequestURI(c.Endpoint)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		add("endpoint", "must be an origin-only HTTPS URL")
	}
	if !regionPattern.MatchString(c.Region) {
		add("region", "must be a bounded S3 signing region")
	}
	if !bucketPattern.MatchString(c.Bucket) || strings.Contains(c.Bucket, "..") {
		add("bucket", "must be a valid DNS-compatible S3 bucket")
	}
	if clean := path.Clean(c.Prefix); c.Prefix == "" || clean != c.Prefix || strings.HasPrefix(clean, "/") ||
		clean == "." || strings.HasPrefix(clean, "../") {
		add("prefix", "must be a normalized relative object prefix")
	}
	validateSortedIdentities := func(field string, values []string, repository bool) {
		if len(values) == 0 || len(values) > maxConfiguredPools {
			add(field, fmt.Sprintf("must contain between 1 and %d exact identities", maxConfiguredPools))
		}
		for index, value := range values {
			valid := validIdentity(value)
			if repository {
				owner, name, separated := strings.Cut(value, "/")
				valid = separated && repositoryPattern.MatchString(value) && validIdentity(owner) && validIdentity(name)
			}
			if !valid {
				add(fmt.Sprintf("%s[%d]", field, index), "must be a bounded exact identity")
			}
			if index > 0 && value <= values[index-1] {
				add(field, "must be strictly sorted and unique")
			}
		}
	}
	validateSortedIdentities("repositories", c.Repositories, true)
	validateSortedIdentities("account_scopes", c.AccountScopes, false)
	validateSortedIdentities("trusts", c.Trusts, false)
	for field, value := range map[string]string{
		"platform": c.Platform, "architecture": c.Architecture,
	} {
		if !validIdentity(value) {
			add(field, "must be a bounded identity")
		}
	}
	if len(c.Pools) == 0 || len(c.Pools) > maxConfiguredPools {
		add("pools", fmt.Sprintf("must contain between 1 and %d exact identities", maxConfiguredPools))
	}
	for index, pool := range c.Pools {
		if !validIdentity(pool) {
			add(fmt.Sprintf("pools[%d]", index), "must be a bounded identity")
		}
		if index > 0 && pool <= c.Pools[index-1] {
			add("pools", "must be strictly sorted and unique")
		}
	}
	if !c.PathStyle {
		add("path_style", "must be true for the RustFS endpoint")
	}
	for field, values := range map[string][2]string{
		"ca_file":         {c.CAFile, "/run/credentials/gha-diagnostic-exporter.service/rustfs-ca.pem"},
		"access_key_file": {c.AccessKeyFile, "/run/credentials/gha-diagnostic-exporter.service/rustfs-access-key"},
		"secret_key_file": {c.SecretKeyFile, "/run/credentials/gha-diagnostic-exporter.service/rustfs-secret-key"},
	} {
		if values[0] != values[1] {
			add(field, "must match the exact systemd per-unit credential path")
		}
	}
	if c.RequestTimeoutSeconds < 5 || c.RequestTimeoutSeconds > 60 {
		add("request_timeout_seconds", "must be between 5 and 60")
	}
	if c.SourceRetentionHours != 168 {
		add("source_retention_hours", "must match the seven-day local spool")
	}
	if c.MaxBundleBytes != 16*1024*1024 {
		add("max_bundle_bytes", "must match the 16 MiB provider bundle boundary")
	}
	if c.MaxDecompressedBytes < c.MaxBundleBytes || c.MaxDecompressedBytes > 64*1024*1024 {
		add("max_decompressed_bytes", "must be between max_bundle_bytes and 64 MiB")
	}
	if len(problems) != 0 {
		sort.Strings(problems)
		return errors.New("invalid diagnostic exporter config: " + strings.Join(problems, "; "))
	}
	return nil
}

func (c Config) AllowsRepository(repository string) bool {
	return sortedContains(c.Repositories, repository)
}

func (c Config) AllowsAccount(account string) bool {
	return sortedContains(c.AccountScopes, account)
}

// AllowsTrust reports whether a diagnostic manifest belongs to a reviewed
// trust domain. Invalid or unvalidated lists fail closed.
func (c Config) AllowsTrust(trust string) bool {
	return validIdentity(trust) && sortedContains(c.Trusts, trust)
}

func sortedContains(values []string, value string) bool {
	for index, candidate := range values {
		if candidate == "" || index > 0 && candidate <= values[index-1] {
			return false
		}
	}
	index := sort.SearchStrings(values, value)
	return index < len(values) && values[index] == value
}

func validIdentity(value string) bool {
	return value != "." && value != ".." && identityPattern.MatchString(value)
}

// AllowsPool reports whether a manifest pool is in the reviewed, strictly
// sorted allowlist. Invalid or unvalidated lists fail closed.
func (c Config) AllowsPool(pool string) bool {
	if !validIdentity(pool) || len(c.Pools) == 0 || len(c.Pools) > maxConfiguredPools {
		return false
	}
	for index, candidate := range c.Pools {
		if !validIdentity(candidate) || index > 0 && candidate <= c.Pools[index-1] {
			return false
		}
	}
	index := sort.SearchStrings(c.Pools, pool)
	return index < len(c.Pools) && c.Pools[index] == pool
}

func directoriesOverlap(left, right string) bool {
	if !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	isWithin := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return isWithin(left, right) || isWithin(right, left)
}
