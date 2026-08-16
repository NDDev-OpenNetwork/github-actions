package githubappbootstrap

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

// maxPrivateKeyBytes bounds the file this command is willing to read as a key.
// A PKCS#1 RSA key is a couple of kilobytes; anything larger is not the file
// the operator meant to name.
const maxPrivateKeyBytes = 16 << 10

// VerifyOptions re-verifies an App that already exists.
//
// `bootstrap-github-app` is the only writer of a credential bundle, and it
// always creates a new App, because the manifest flow has no other shape. That
// is the wrong tool for the case this exists for: an App whose *permissions*
// changed after its bundle was written.
//
// The bundle records the permissions it was written with, and
// `garmbootstrap.Reconcile` reads them from that file rather than from GitHub —
// deliberately, since the file is what a reviewer approved. So approving a new
// permission on a live installation leaves a bundle that is now wrong about it,
// and an organization entity built from that bundle fails closed. Before this
// command the only remedy was to create a second App and retire the first,
// which churns an installation, an anchor and a registry row to fix a stale
// JSON file.
type VerifyOptions struct {
	// Tenant names the registered account. Identity comes from the registry.
	Tenant string
	// Repository, OwnerType and OrganizationRunners are held to the same values
	// `bootstrap-github-app` would have used, so a bundle written here is
	// indistinguishable from one written there.
	Repository          string
	OwnerType           string
	OrganizationRunners bool
	// AppID and InstallationID name the App being re-verified. Both are
	// non-secret and both appear in the credential anchor.
	AppID          int64
	InstallationID int64
	// PrivateKeyPath is the existing App key. It is read, used to sign one JWT
	// and copied into the new bundle; it is never logged.
	PrivateKeyPath string
	// OutputDirectory must not exist. A bundle is written whole or not at all.
	OutputDirectory string
}

// Verify re-reads a live installation and writes a current bundle for it.
//
// Every check `bootstrap-github-app` applies after the manifest is approved
// applies here too, because it is the same function: the installation must
// belong to the tenant's account, its repository selection must be one this
// deployment accepts, and its permissions must be exactly the least-privilege
// set the tenant asked for. What this skips is only the part that creates an
// App, which is the part that must not happen.
func (r Runner) Verify(ctx context.Context, options VerifyOptions) (VerifiedInstallation, error) {
	selected, err := validateVerifyOptions(&options)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	privateKey, err := readBoundedKey(options.PrivateKeyPath)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	defer clear(privateKey)

	registration := appRegistration{
		ID:   options.AppID,
		Slug: selected.AppSlug,
		PEM:  string(privateKey),
	}
	client := newGitHubClient(r.HTTPClient)
	if r.APIBaseURL != "" {
		client.baseURL = r.APIBaseURL
	}
	verified, err := client.verifyInstallation(
		ctx, registration, options.InstallationID,
		options.Repository, options.OwnerType, options.OrganizationRunners,
	)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	return persistCredentials(options.OutputDirectory, registration, verified)
}

func validateVerifyOptions(options *VerifyOptions) (tenant.Tenant, error) {
	if options.OwnerType != OwnerTypeOrganization && options.OwnerType != OwnerTypeUser {
		return tenant.Tenant{}, fmt.Errorf(
			"owner type is %q, want %q or %q",
			options.OwnerType, OwnerTypeOrganization, OwnerTypeUser)
	}
	selected, err := tenant.ByID(options.Tenant)
	if err != nil {
		return tenant.Tenant{}, fmt.Errorf("%w; known tenants: %v", err, tenant.IDs())
	}
	// The registry is the identity, exactly as it is for the bootstrap. An
	// override exists so a mismatch can be expressed and refused, not so one
	// can be configured.
	if options.Repository == "" {
		options.Repository = selected.Repository
	}
	if options.Repository != selected.Repository {
		return tenant.Tenant{}, fmt.Errorf(
			"repository must be exactly %s for tenant %q", selected.Repository, selected.ID)
	}
	if options.AppID <= 0 || options.InstallationID <= 0 {
		return tenant.Tenant{}, fmt.Errorf("app id and installation id are required and must be positive")
	}
	if !filepath.IsAbs(options.PrivateKeyPath) || filepath.Clean(options.PrivateKeyPath) != options.PrivateKeyPath {
		return tenant.Tenant{}, fmt.Errorf("private key path must be absolute and clean")
	}
	if !filepath.IsAbs(options.OutputDirectory) || filepath.Clean(options.OutputDirectory) != options.OutputDirectory {
		return tenant.Tenant{}, fmt.Errorf("output directory must be absolute and clean")
	}
	return selected, nil
}

// readBoundedKey refuses anything that is not a small, private regular file.
// A key readable by the group is a key that has already leaked, and saying so
// here is cheaper than discovering it from an audit record later.
func readBoundedKey(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect private key: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("private key must be a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("private key must not be readable by group or other")
	}
	if info.Size() <= 0 || info.Size() > maxPrivateKeyBytes {
		return nil, fmt.Errorf("private key size is implausible for a PKCS#1 RSA key")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	return data, nil
}
