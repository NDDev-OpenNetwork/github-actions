package diagnosticstore

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/rustfscache"
	"gopkg.in/yaml.v3"
)

const (
	SchemaVersion     = 1
	MaximumConfigSize = 64 * 1024
	DefaultConfigPath = "/etc/gha-fleet/diagnostic-storage.yaml"
)

var bucketPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)

// Config is the root-owned durability contract for the remote diagnostic WAL.
// It deliberately contains no exporter secret: bucket capacity and retention
// are reconciled with the RustFS root credential, while the exporter keeps its
// independently scoped write-only identity.
type Config struct {
	SchemaVersion     int    `yaml:"schema_version" json:"schema_version"`
	Endpoint          string `yaml:"endpoint" json:"endpoint"`
	Region            string `yaml:"region" json:"region"`
	CAFile            string `yaml:"ca_file" json:"ca_file"`
	RootAccessKeyFile string `yaml:"root_access_key_file" json:"root_access_key_file"`
	RootSecretKeyFile string `yaml:"root_secret_key_file" json:"root_secret_key_file"`
	Bucket            string `yaml:"bucket" json:"bucket"`
	Prefix            string `yaml:"prefix" json:"prefix"`
	QuotaBytes        int64  `yaml:"quota_bytes" json:"quota_bytes"`
	RetentionDays     int    `yaml:"retention_days" json:"retention_days"`
	MinimumHeadroom   int64  `yaml:"minimum_headroom_bytes" json:"minimum_headroom_bytes"`
}

// WithCredentialDirectory binds the two root credential paths to a systemd
// credential directory supplied by the unit's %d specifier. The public config
// remains secret-free and shared by observer and reconciler even though each
// service receives an isolated /run/credentials/<unit> mount.
func WithCredentialDirectory(config Config, directory string) (Config, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" {
		return Config{}, errors.New("credential directory must be an absolute clean non-root path")
	}
	config.RootAccessKeyFile = filepath.Join(directory, "rustfs-access-key")
	config.RootSecretKeyFile = filepath.Join(directory, "rustfs-secret-key")
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func Load(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read diagnostic storage config: %w", err)
	}
	if len(raw) == 0 || len(raw) > MaximumConfigSize {
		return Config{}, fmt.Errorf("diagnostic storage config must contain 1..%d bytes", MaximumConfigSize)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode diagnostic storage config: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return Config{}, errors.New("diagnostic storage config must contain exactly one YAML document")
	} else if !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("decode trailing diagnostic storage config: %w", err)
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
	if err := rustfscache.ValidateEndpoint(c.Endpoint); err != nil {
		return err
	}
	if c.Region != "us-east-1" {
		return errors.New("region must be us-east-1")
	}
	for label, value := range map[string]string{
		"ca_file": c.CAFile, "root_access_key_file": c.RootAccessKeyFile,
		"root_secret_key_file": c.RootSecretKeyFile,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || value == "/" {
			return fmt.Errorf("%s must be an absolute clean non-root path", label)
		}
	}
	if !bucketPattern.MatchString(c.Bucket) {
		return errors.New("bucket is not a valid S3 bucket name")
	}
	if c.Prefix == "" || strings.HasPrefix(c.Prefix, "/") || strings.HasSuffix(c.Prefix, "/") || strings.Contains(c.Prefix, "..") {
		return errors.New("prefix must be a clean non-empty relative object prefix")
	}
	if c.QuotaBytes < 2*1024*1024*1024 || c.QuotaBytes > 1024*1024*1024*1024 {
		return errors.New("quota_bytes must be between 2 GiB and 1 TiB")
	}
	if c.RetentionDays < 1 || c.RetentionDays > 30 {
		return errors.New("retention_days must be between 1 and 30")
	}
	if c.MinimumHeadroom < 1024*1024*1024 || c.MinimumHeadroom >= c.QuotaBytes {
		return errors.New("minimum_headroom_bytes must be at least 1 GiB and less than quota_bytes")
	}
	return nil
}
