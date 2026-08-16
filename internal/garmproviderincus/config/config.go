// SPDX-License-Identifier: Apache-2.0
// Copyright 2023 Cloudbase Solutions SRL
// Modified by NDDev in 2026 for the hardened NDDev fleet provider.
//
//    Licensed under the Apache License, Version 2.0 (the "License"); you may
//    not use this file except in compliance with the License. You may obtain
//    a copy of the License at
//
//         http://www.apache.org/licenses/LICENSE-2.0
//
//    Unless required by applicable law or agreed to in writing, software
//    distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
//    WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
//    License for the specific language governing permissions and limitations
//    under the License.

package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/pkg/errors"
)

const (
	ExpectedIncusURL    = "https://127.0.0.1:8443"
	ExpectedProjectName = "gha-fleet"
)

// fleetPrivateNetwork is the cloud private network every fleet host sits on.
//
// A standalone host's Incus is reachable only on loopback, which is the
// tightest boundary there is. A cluster cannot use it: members reach each
// other, and the provider reaches the cluster, over the private network. So
// the boundary widens by exactly one subnet and no further -- an Incus API
// outside it would accept a client certificate from anywhere on the internet.
var fleetPrivateNetwork = netip.MustParsePrefix("172.16.0.0/20")

var fingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type IncusRemoteProtocol string
type IncusImageType string

func (l IncusImageType) String() string {
	return string(l)
}

const (
	SimpleStreams            IncusRemoteProtocol = "simplestreams"
	IncusImageVirtualMachine IncusImageType      = "virtual-machine"
	IncusImageContainer      IncusImageType      = "container"
)

// IncusImageRemote holds information about a remote server from which Incus can fetch
// OS images. Typically this will be a simplestreams server.
type IncusImageRemote struct {
	Address            string              `toml:"addr" json:"addr"`
	Public             bool                `toml:"public" json:"public"`
	Protocol           IncusRemoteProtocol `toml:"protocol" json:"protocol"`
	InsecureSkipVerify bool                `toml:"skip_verify" json:"skip-verify"`
}

// WorkerImage pins one local Incus alias to one immutable image fingerprint.
// The containing map is keyed by the exact GARM flavor / platform pool name.
type WorkerImage struct {
	Alias       string `toml:"alias" json:"alias"`
	Fingerprint string `toml:"fingerprint" json:"fingerprint"`
	Variant     string `toml:"variant" json:"variant"`
	RunnerUID   int64  `toml:"runner_uid" json:"runner_uid"`
	RunnerGID   int64  `toml:"runner_gid" json:"runner_gid"`
}

func (l *IncusImageRemote) Validate() error {
	if l.Protocol != SimpleStreams {
		// Only supports simplestreams for now.
		return fmt.Errorf("invalid remote protocol %s. Supported protocols: %s", l.Protocol, SimpleStreams)
	}
	if l.Address == "" {
		return fmt.Errorf("missing address")
	}

	url, err := url.ParseRequestURI(l.Address)
	if err != nil {
		return errors.Wrap(err, "validating address")
	}

	if url.Scheme != "http" && url.Scheme != "https" {
		return fmt.Errorf("address must be http or https")
	}

	return nil
}

// NewConfig returns a new Config
func NewConfig(cfgFile string) (*Incus, error) {
	var config Incus
	metadata, err := toml.DecodeFile(cfgFile, &config)
	if err != nil {
		return nil, fmt.Errorf("error decoding config: %w", err)
	}
	if undecoded := metadata.Undecoded(); len(undecoded) != 0 {
		return nil, fmt.Errorf("error decoding config: unknown keys: %v", undecoded)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("error validating config: %w", err)
	}
	return &config, nil
}

// Incus holds connection information for an Incus cluster.
type Incus struct {
	// UnixSocket is the path on disk to the Incus unix socket. If defined,
	// this is prefered over connecting via HTTPs.
	UnixSocket string `toml:"unix_socket_path" json:"unix-socket-path"`

	// Project name is the name of the project in which this runner will create
	// instances. If this option is not set, the default project will be used.
	// The project used here, must have all required profiles created by you
	// beforehand. For Incus, the "flavor" used in the runner definition for a pool
	// equates to a profile in the desired project.
	ProjectName string `toml:"project_name" json:"project-name"`

	// IncludeDefaultProfile specifies whether or not this provider will always add
	// the "default" profile to any newly created instance.
	IncludeDefaultProfile bool `toml:"include_default_profile" json:"include-default-profile"`

	// URL holds the URL of the remote Incus server.
	// example: https://10.10.10.1:8443/
	URL string `toml:"url" json:"url"`
	// ClientCertificate is the x509 client certificate path used for authentication.
	ClientCertificate string `toml:"client_certificate" json:"client_certificate"`
	// ClientKey is the key used for client certificate authentication.
	ClientKey string `toml:"client_key" json:"client-key"`
	// TLS certificate of the remote server. If not specified, the system CA is used.
	TLSServerCert string `toml:"tls_server_certificate" json:"tls-server-certificate"`
	// TLSCA is the TLS CA certificate when running Incus in PKI mode.
	TLSCA string `toml:"tls_ca" json:"tls-ca"`

	// ImageRemotes is a map to a set of remote image repositories we can use to
	// download images.
	ImageRemotes map[string]IncusImageRemote `toml:"image_remotes" json:"image-remotes"`

	// SecureBoot enables secure boot for VMs spun up using this provider.
	SecureBoot bool `toml:"secure_boot" json:"secure-boot"`

	// InstanceType allows you to choose between a virtual machine and a container
	InstanceType IncusImageType `toml:"instance_type" json:"instance-type"`

	// WorkerImages is the exact flavor-to-image policy accepted from GARM.
	// Every configured alias resolves locally and is independently pinned by digest.
	WorkerImages map[string]WorkerImage `toml:"worker_images" json:"worker-images"`

	// PlatformConfigFile is the validated NDDev resource and trust policy.
	PlatformConfigFile string `toml:"platform_config_file" json:"platform-config-file"`
	// JournalFile and JournalLockFile provide cross-process admission leases.
	JournalFile     string `toml:"journal_file" json:"journal-file"`
	JournalLockFile string `toml:"journal_lock_file" json:"journal-lock-file"`
	// QueueIntentFile is the GARM-owned, fsync-published pre-AcquireJobs
	// scheduler journal. The provider is strictly a read-only consumer.
	QueueIntentFile string `toml:"queue_intent_file" json:"queue-intent-file"`
	// AdmissionLeaseSeconds bounds recovery of a crash before instance creation.
	AdmissionLeaseSeconds int `toml:"admission_lease_seconds" json:"admission-lease-seconds"`

	// Diagnostics are captured outside a disposable VM immediately before
	// teardown. These pilot limits are exact so config drift cannot silently
	// expand the amount of untrusted log data retained on the host.
	DiagnosticsDirectory      string `toml:"diagnostics_directory" json:"diagnostics-directory"`
	DiagnosticsRetentionHours int    `toml:"diagnostics_retention_hours" json:"diagnostics-retention-hours"`
	DiagnosticsMaxBundleBytes int64  `toml:"diagnostics_max_bundle_bytes" json:"diagnostics-max-bundle-bytes"`
	DiagnosticsMaxTotalBytes  int64  `toml:"diagnostics_max_total_bytes" json:"diagnostics-max-total-bytes"`
}

func (l *Incus) GetInstanceType() IncusImageType {
	switch l.InstanceType {
	case IncusImageVirtualMachine, IncusImageContainer:
		return l.InstanceType
	default:
		return IncusImageVirtualMachine
	}
}

func (l *Incus) WorkerImageForFlavor(flavor string) (WorkerImage, bool) {
	image, exists := l.WorkerImages[flavor]
	return image, exists
}

func (l *Incus) Validate() error {
	if l.UnixSocket != "" {
		return fmt.Errorf("unix_socket_path is forbidden; use the pinned loopback TLS endpoint")
	}

	if l.URL == "" {
		return fmt.Errorf("url must be specified")
	}

	parsedURL, err := url.ParseRequestURI(l.URL)
	if err != nil {
		return fmt.Errorf("invalid Incus URL")
	}
	if parsedURL.Scheme != "https" || parsedURL.User != nil ||
		(parsedURL.Path != "" && parsedURL.Path != "/") ||
		parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return fmt.Errorf("url must be a bare https endpoint with no path, query, fragment or credentials")
	}
	if err := validateIncusEndpoint(parsedURL.Host); err != nil {
		return fmt.Errorf("invalid Incus endpoint %q: %w", parsedURL.Host, err)
	}

	if l.ClientCertificate == "" || l.ClientKey == "" {
		return fmt.Errorf("client_certificate and client_key are mandatory")
	}
	if err := validateRegularFile(l.ClientCertificate, false); err != nil {
		return fmt.Errorf("invalid client certificate %s: %w", l.ClientCertificate, err)
	}
	if err := validateRegularFile(l.ClientKey, true); err != nil {
		return fmt.Errorf("invalid client key %s: %w", l.ClientKey, err)
	}
	if l.TLSServerCert == "" {
		return fmt.Errorf("tls_server_certificate is mandatory")
	}
	if err := validateRegularFile(l.TLSServerCert, false); err != nil {
		return fmt.Errorf("invalid tls_server_certificate %s: %w", l.TLSServerCert, err)
	}
	if l.TLSCA != "" {
		if err := validateRegularFile(l.TLSCA, false); err != nil {
			return fmt.Errorf("invalid tls_ca %s: %w", l.TLSCA, err)
		}
	}

	if len(l.ImageRemotes) != 0 {
		return fmt.Errorf("image_remotes are forbidden; only the pinned local image is allowed")
	}
	if l.ProjectName != ExpectedProjectName {
		return fmt.Errorf("project_name must be %s", ExpectedProjectName)
	}
	if l.IncludeDefaultProfile {
		return fmt.Errorf("include_default_profile must be false")
	}
	if !l.SecureBoot {
		return fmt.Errorf("secure_boot must be true")
	}
	if l.InstanceType != IncusImageVirtualMachine {
		return fmt.Errorf("instance_type must be %s", IncusImageVirtualMachine)
	}
	if len(l.WorkerImages) == 0 {
		return fmt.Errorf("worker_images must pin at least one pool image")
	}
	aliases := make(map[string]string, len(l.WorkerImages))
	for flavor, image := range l.WorkerImages {
		if flavor == "" || strings.TrimSpace(flavor) != flavor {
			return fmt.Errorf("worker_images key must be an exact non-empty flavor")
		}
		if image.Alias == "" || strings.TrimSpace(image.Alias) != image.Alias || strings.Contains(image.Alias, ":") {
			return fmt.Errorf("worker_images.%s.alias must name one local Incus alias", flavor)
		}
		if !fingerprintPattern.MatchString(image.Fingerprint) {
			return fmt.Errorf("worker_images.%s.fingerprint must be a lowercase SHA-256 digest", flavor)
		}
		if image.Variant != "standard" && image.Variant != "integration" {
			return fmt.Errorf("worker_images.%s.variant must be standard or integration", flavor)
		}
		if image.RunnerUID < 1 || image.RunnerUID > 65535 {
			return fmt.Errorf("worker_images.%s.runner_uid must be in 1..65535", flavor)
		}
		if image.RunnerGID < 1 || image.RunnerGID > 65535 {
			return fmt.Errorf("worker_images.%s.runner_gid must be in 1..65535", flavor)
		}
		if fingerprint, exists := aliases[image.Alias]; exists && fingerprint != image.Fingerprint {
			return fmt.Errorf("worker image alias %q is pinned to conflicting fingerprints", image.Alias)
		}
		aliases[image.Alias] = image.Fingerprint
	}
	if err := validateRegularFile(l.PlatformConfigFile, false); err != nil {
		return fmt.Errorf("invalid platform_config_file %s: %w", l.PlatformConfigFile, err)
	}
	if err := validateStatePath(l.JournalFile); err != nil {
		return fmt.Errorf("invalid journal_file %s: %w", l.JournalFile, err)
	}
	if err := validateStatePath(l.JournalLockFile); err != nil {
		return fmt.Errorf("invalid journal_lock_file %s: %w", l.JournalLockFile, err)
	}
	if filepath.Clean(l.JournalFile) == filepath.Clean(l.JournalLockFile) {
		return fmt.Errorf("journal_file and journal_lock_file must differ")
	}
	if err := validateStatePath(l.QueueIntentFile); err != nil {
		return fmt.Errorf("invalid queue_intent_file %s: %w", l.QueueIntentFile, err)
	}
	if filepath.Clean(l.QueueIntentFile) == filepath.Clean(l.JournalFile) ||
		filepath.Clean(l.QueueIntentFile) == filepath.Clean(l.JournalLockFile) {
		return fmt.Errorf("queue_intent_file must differ from provider journal paths")
	}
	if l.AdmissionLeaseSeconds != 300 {
		return fmt.Errorf("admission_lease_seconds must be exactly 300 during the pilot")
	}
	if err := validateStateDirectory(l.DiagnosticsDirectory); err != nil {
		return fmt.Errorf("invalid diagnostics_directory %s: %w", l.DiagnosticsDirectory, err)
	}
	if l.DiagnosticsRetentionHours != 168 {
		return fmt.Errorf("diagnostics_retention_hours must be exactly 168 during the pilot")
	}
	if l.DiagnosticsMaxBundleBytes != 16*1024*1024 {
		return fmt.Errorf("diagnostics_max_bundle_bytes must be exactly 16777216 during the pilot")
	}
	if l.DiagnosticsMaxTotalBytes != 1024*1024*1024 {
		return fmt.Errorf("diagnostics_max_total_bytes must be exactly 1073741824 during the pilot")
	}
	return nil
}

func validateRegularFile(path string, private bool) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve parent directory: %w", err)
	}
	if filepath.Clean(resolvedParent) != filepath.Clean(parent) {
		return fmt.Errorf("parent directory must not traverse symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("path must reference a regular file, not a symlink")
	}
	if private && info.Mode().Perm()&0o027 != 0 {
		return fmt.Errorf("private key permissions may grant group read, but not group write/execute or other access")
	}
	return nil
}

func validateStatePath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return fmt.Errorf("path must be absolute and bounded")
	}
	parent := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return fmt.Errorf("resolve parent directory: %w", err)
	}
	if filepath.Clean(resolvedParent) != filepath.Clean(parent) {
		return fmt.Errorf("parent directory must not traverse symlinks")
	}
	info, err := os.Lstat(parent)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("parent must be a real directory")
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("existing state must be a private regular file")
	}
	return nil
}

func validateStateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) == string(filepath.Separator) {
		return fmt.Errorf("path must be absolute and bounded")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if filepath.Clean(resolved) != filepath.Clean(path) {
		return fmt.Errorf("directory must not traverse symlinks")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("path must reference a directory, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("directory must not grant group or other access")
	}
	return nil
}

// validateIncusEndpoint accepts the standalone loopback endpoint, or a cluster
// member inside the fleet's own private network. Nothing else: a public
// address here would expose instance creation to any holder of a client
// certificate, and this provider is the only thing standing between GARM and
// the hypervisor.
func validateIncusEndpoint(host string) error {
	if host == "127.0.0.1:8443" {
		return nil
	}
	address, err := netip.ParseAddrPort(host)
	if err != nil {
		return fmt.Errorf("must be host:port")
	}
	if address.Port() != 8443 {
		return fmt.Errorf("must use port 8443")
	}
	if !fleetPrivateNetwork.Contains(address.Addr()) {
		return fmt.Errorf("must be %s or a cluster member inside %s", ExpectedIncusURL, fleetPrivateNetwork)
	}
	return nil
}
