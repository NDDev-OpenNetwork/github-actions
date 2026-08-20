package garmbootstrap

import (
	"errors"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

	"bytes"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxCredentialFileBytes = 1 << 20

func loadAdminCredentials(path string) (adminCredentials, error) {
	data, err := readSecureFile(path)
	if err != nil {
		return adminCredentials{}, fmt.Errorf("read GARM admin credentials: %w", err)
	}
	defer clear(data)
	var credentials adminCredentials
	if err := decodeStrict(data, &credentials); err != nil {
		return adminCredentials{}, fmt.Errorf("decode GARM admin credentials: %w", err)
	}
	if strings.TrimSpace(credentials.Username) == "" || credentials.Password == "" {
		return adminCredentials{}, fmt.Errorf("GARM admin credentials require username and password")
	}
	return credentials, nil
}

func loadAppBundle(directory string, selected tenant.Tenant, now time.Time) (appBundle, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return appBundle{}, fmt.Errorf("GitHub App bundle directory must be an absolute clean path")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return appBundle{}, fmt.Errorf("inspect GitHub App bundle directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return appBundle{}, fmt.Errorf("GitHub App bundle directory must be a private real directory")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return appBundle{}, fmt.Errorf("list GitHub App bundle directory: %w", err)
	}
	if len(entries) != 2 || entries[0].Name() != "github-app-private-key.pem" || entries[1].Name() != "installation.json" {
		return appBundle{}, fmt.Errorf("GitHub App bundle must contain exactly the private key and installation metadata")
	}
	metadataPath := filepath.Join(directory, "installation.json")
	metadata, err := readSecureFile(metadataPath)
	if err != nil {
		return appBundle{}, fmt.Errorf("read verified GitHub App metadata: %w", err)
	}
	var installation verifiedInstallation
	if err := decodeStrict(metadata, &installation); err != nil {
		return appBundle{}, fmt.Errorf("decode verified GitHub App metadata: %w", err)
	}
	if err := validateInstallationMetadata(installation, selected, now); err != nil {
		return appBundle{}, err
	}
	keyPath := filepath.Join(directory, "github-app-private-key.pem")
	privateKey, err := readSecureFile(keyPath)
	if err != nil {
		return appBundle{}, fmt.Errorf("read GitHub App private key: %w", err)
	}
	parsedKey, err := parsePKCS1PrivateKey(privateKey)
	if err != nil {
		clear(privateKey)
		return appBundle{}, err
	}
	publicKeyDER, err := x509.MarshalPKIXPublicKey(&parsedKey.PublicKey)
	if err != nil {
		clear(privateKey)
		return appBundle{}, fmt.Errorf("encode GitHub App public key: %w", err)
	}
	fingerprint := sha256.Sum256(publicKeyDER)
	anchor := credentialAnchor{
		SchemaVersion:  1,
		CredentialName: selected.CredentialName,
		AppID:          installation.AppID,
		InstallationID: installation.InstallationID,
		KeySHA256:      hex.EncodeToString(fingerprint[:]),
	}
	return appBundle{
		Installation: installation,
		PrivateKey:   privateKey,
		Anchor:       anchor,
		Description:  anchor.description(),
	}, nil
}

func loadCredentialAnchor(path string, selected tenant.Tenant) (credentialAnchor, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return credentialAnchor{}, fmt.Errorf("GARM credential anchor path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return credentialAnchor{}, fmt.Errorf("inspect GARM credential anchor: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > maxCredentialFileBytes {
		return credentialAnchor{}, fmt.Errorf("GARM credential anchor must be a bounded, non-writable regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return credentialAnchor{}, fmt.Errorf("read GARM credential anchor: %w", err)
	}
	var anchor credentialAnchor
	if err := decodeStrict(data, &anchor); err != nil {
		return credentialAnchor{}, fmt.Errorf("decode GARM credential anchor: %w", err)
	}
	if err := anchor.validate(selected); err != nil {
		return credentialAnchor{}, err
	}
	return anchor, nil
}

func (a credentialAnchor) validate(selected tenant.Tenant) error {
	if a.SchemaVersion != 1 || a.CredentialName != selected.CredentialName || a.AppID <= 0 || a.InstallationID <= 0 {
		return fmt.Errorf("GARM credential anchor identity is invalid")
	}
	decoded, err := hex.DecodeString(a.KeySHA256)
	if err != nil || len(decoded) != sha256.Size || a.KeySHA256 != strings.ToLower(a.KeySHA256) {
		return fmt.Errorf("GARM credential anchor key fingerprint must be a lowercase SHA-256 digest")
	}
	return nil
}

func (a credentialAnchor) description() string {
	return fmt.Sprintf(
		"managed-by=gha-fleet;app=%d;installation=%d;key-sha256=%s",
		a.AppID,
		a.InstallationID,
		a.KeySHA256,
	)
}

func (a credentialAnchor) matchesBundle(bundle appBundle) bool {
	return a == bundle.Anchor
}

func validateInstallationMetadata(installation verifiedInstallation, selected tenant.Tenant, now time.Time) error {
	if installation.SchemaVersion != 1 {
		return fmt.Errorf("GitHub App metadata schema is %d, want 1", installation.SchemaVersion)
	}
	if installation.AppID <= 0 || installation.InstallationID <= 0 || installation.AppSlug != selected.AppSlug {
		return fmt.Errorf("GitHub App metadata has invalid App identity")
	}
	if installation.AccountLogin != selected.Owner || installation.Repository != selected.Repository {
		return fmt.Errorf("GitHub App metadata is outside the exact managed repository scope")
	}
	// The App is installed account-wide so one fleet can serve every repository
	// the owner holds; `selected` stays valid for a narrower install. Account
	// and exact managed repository are still pinned above, and the permission
	// set below is still exactly administration=write, actions=read and
	// metadata=read.
	if installation.RepositorySelection != "selected" && installation.RepositorySelection != "all" {
		return fmt.Errorf("GitHub App repository selection is %q, want selected or all", installation.RepositorySelection)
	}
	// administration=write, actions=read and metadata=read are the repository
	// set. An App
	// that also backs an organization entity carries one more, and nothing
	// beyond these three is accepted.
	if installation.Permissions["administration"] != "write" ||
		installation.Permissions[ActionsReadPermission] != "read" ||
		installation.Permissions["metadata"] != "read" {
		return fmt.Errorf("GitHub App permissions differ from administration=write, actions=read and metadata=read")
	}
	for name := range installation.Permissions {
		if name != "administration" && name != ActionsReadPermission && name != "metadata" && name != OrganizationRunnersPermission {
			return fmt.Errorf("GitHub App permissions include unexpected %q", name)
		}
	}
	if level, present := installation.Permissions[OrganizationRunnersPermission]; present && level != "write" {
		return fmt.Errorf("%s permission is %q, want write", OrganizationRunnersPermission, level)
	}
	// The App is owned by, and installed on, one account. Which kind of account
	// that is decides whether an organization entity can exist at all, so an
	// unrecognised value is refused rather than carried forward as empty.
	if installation.OwnerType != OwnerTypeOrganization && installation.OwnerType != OwnerTypeUser {
		return fmt.Errorf("GitHub App owner type is %q, want %q or %q", installation.OwnerType, OwnerTypeOrganization, OwnerTypeUser)
	}
	if filepath.Base(installation.PrivateKeyPath) != "github-app-private-key.pem" {
		return fmt.Errorf("GitHub App metadata references an unexpected private key")
	}
	if installation.VerifiedAt.IsZero() || installation.VerifiedAt.After(now.Add(5*time.Minute)) || installation.VerifiedAt.Before(now.Add(-24*time.Hour)) {
		return fmt.Errorf("GitHub App verification timestamp is outside the one-time import window")
	}
	return nil
}

func parsePKCS1PrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, trailing := pem.Decode(data)
	if block == nil || block.Type != "RSA PRIVATE KEY" || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, fmt.Errorf("GitHub App private key must contain exactly one PKCS#1 RSA PEM block")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GitHub App PKCS#1 private key: %w", err)
	}
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("validate GitHub App private key: %w", err)
	}
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("GitHub App private key is smaller than 2048 bits")
	}
	return key, nil
}

func readSecureFile(path string) ([]byte, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s must be a private regular file", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxCredentialFileBytes {
		clear(data)
		return nil, fmt.Errorf("%s exceeds %d bytes", filepath.Base(path), maxCredentialFileBytes)
	}
	return data, nil
}

func decodeStrict(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
