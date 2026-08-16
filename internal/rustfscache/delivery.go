package rustfscache

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Delivery is the smallest cache identity representation allowed to cross
// the host-to-disposable-worker boundary. SecretKey and CAPEM remain byte
// slices so callers can erase their copies after Incus accepts the file.
type Delivery struct {
	Role      string
	Endpoint  string
	Region    string
	Bucket    string
	Prefix    string
	Mode      string
	AccessKey string
	SecretKey []byte
	CAPEM     []byte
}

// Clear erases mutable secret-bearing buffers owned by the delivery.
func (d *Delivery) Clear() {
	clear(d.SecretKey)
	clear(d.CAPEM)
	d.SecretKey = nil
	d.CAPEM = nil
}

// LoadDelivery validates and reads exactly one reconciled identity. It does
// not accept root credentials and does not mutate either local or remote
// state. The caller supplies the reviewed host ownership contract so the same
// checks can be exercised without privileged test fixtures.
func LoadDelivery(config Config, role string, credentialUID, credentialGID, caUID, caGID int) (Delivery, error) {
	if err := config.Validate(); err != nil {
		return Delivery{}, err
	}
	if credentialUID < 0 || credentialGID < 0 || caUID < 0 || caGID < 0 {
		return Delivery{}, fmt.Errorf("delivery owner IDs must be non-negative")
	}
	if err := validateCredentialDirectory(config.CredentialsDirectory, credentialUID, credentialGID); err != nil {
		return Delivery{}, err
	}

	var identity Identity
	found := false
	for _, candidate := range config.Identities {
		if candidate.Role == role {
			identity = candidate
			found = true
			break
		}
	}
	if !found || role == "promoter" {
		return Delivery{}, fmt.Errorf("RustFS cache role %q is not deliverable to a worker", role)
	}

	accessPath := filepath.Join(config.CredentialsDirectory, "rustfs-"+role+"-access-key")
	secretPath := filepath.Join(config.CredentialsDirectory, "rustfs-"+role+"-secret-key")
	access, err := readCredentialFile(accessPath, credentialUID, credentialGID, credentialFileMode)
	if err != nil {
		return Delivery{}, fmt.Errorf("read %s delivery access key: %w", role, err)
	}
	if string(access) != accessKeyForRole(role) {
		clear(access)
		return Delivery{}, fmt.Errorf("RustFS %s delivery access key drifted", role)
	}
	secret, err := readCredentialFile(secretPath, credentialUID, credentialGID, credentialFileMode)
	if err != nil {
		clear(access)
		return Delivery{}, fmt.Errorf("read %s delivery secret key: %w", role, err)
	}
	if len(secret) != 64 {
		clear(access)
		clear(secret)
		return Delivery{}, fmt.Errorf("RustFS %s delivery secret key has an invalid length", role)
	}
	ca, err := readDeliveryCA(config.CAFile, caUID, caGID)
	if err != nil {
		clear(access)
		clear(secret)
		return Delivery{}, fmt.Errorf("read RustFS cache CA: %w", err)
	}
	if err := validateSingleCertificate(ca); err != nil {
		clear(access)
		clear(secret)
		clear(ca)
		return Delivery{}, fmt.Errorf("validate RustFS cache CA: %w", err)
	}

	delivery := Delivery{
		Role:      role,
		Endpoint:  config.Endpoint,
		Region:    config.Region,
		Bucket:    config.Bucket,
		Prefix:    identity.Prefix,
		Mode:      identity.Mode,
		AccessKey: string(access),
		SecretKey: secret,
		CAPEM:     ca,
	}
	clear(access)
	return delivery, nil
}

func readDeliveryCA(path string, uid, gid int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o644 ||
		int(stat.Uid) != uid || int(stat.Gid) != gid || stat.Nlink != 1 || info.Size() < 1 || info.Size() > MaximumConfigBytes {
		return nil, fmt.Errorf("cache CA must be a singly linked regular file owned by %d:%d with mode 0644", uid, gid)
	}
	return os.ReadFile(path)
}

func validateCredentialDirectory(path string, uid, gid int) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect credential directory: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o750 ||
		int(stat.Uid) != uid || int(stat.Gid) != gid {
		return fmt.Errorf("credential directory must be a real mode-0750 directory owned by %d:%d", uid, gid)
	}
	return nil
}

func validateSingleCertificate(raw []byte) error {
	rest := bytes.TrimSpace(raw)
	block, trailing := pem.Decode(rest)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(trailing)) != 0 {
		return fmt.Errorf("CA file must contain exactly one PEM certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return err
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid {
		return fmt.Errorf("certificate is not a valid CA")
	}
	return nil
}
