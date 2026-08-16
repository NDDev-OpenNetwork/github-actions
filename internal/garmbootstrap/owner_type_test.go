package garmbootstrap

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The App bootstrapper writes owner_type and this decoder is strict, so a field
// declared on one side and not the other rejects the whole bundle. That is how
// every freshly bootstrapped App became unusable while the already-created
// credential kept working, which is the shape of defect a round trip catches
// and neither side's own tests do.
func TestAFreshlyWrittenBundleDecodesHere(t *testing.T) {
	selected := testTenant()
	installation := verifiedInstallation{
		SchemaVersion:       1,
		AppID:               12345,
		AppSlug:             selected.AppSlug,
		InstallationID:      67890,
		AccountLogin:        selected.Owner,
		OwnerType:           OwnerTypeOrganization,
		Repository:          selected.Repository,
		RepositorySelection: "all",
		Permissions:         map[string]string{"administration": "write", "metadata": "read", OrganizationRunnersPermission: "write"},
		PrivateKeyPath:      filepath.Join("/tmp", "github-app-private-key.pem"),
		VerifiedAt:          time.Now().UTC(),
	}
	if err := validateInstallationMetadata(installation, selected, time.Now()); err != nil {
		t.Fatalf("a bundle in the shape the bootstrapper writes was refused: %v", err)
	}
}

func TestAnUnknownOwnerTypeIsRefused(t *testing.T) {
	selected := testTenant()
	installation := verifiedInstallation{
		SchemaVersion:       1,
		AppID:               12345,
		AppSlug:             selected.AppSlug,
		InstallationID:      67890,
		AccountLogin:        selected.Owner,
		OwnerType:           "enterprise",
		Repository:          selected.Repository,
		RepositorySelection: "all",
		Permissions:         map[string]string{"administration": "write", "metadata": "read", OrganizationRunnersPermission: "write"},
		PrivateKeyPath:      filepath.Join("/tmp", "github-app-private-key.pem"),
		VerifiedAt:          time.Now().UTC(),
	}
	err := validateInstallationMetadata(installation, selected, time.Now())
	if err == nil || !strings.Contains(err.Error(), "owner type") {
		t.Fatalf("an unrecognised owner type was accepted: %v", err)
	}
}

// A plan is what an operator approves. When nothing exists yet the credential
// short-circuit is the only place the plan is written, and it named a
// repository whatever entity kind had been asked for — so onboarding a new
// account showed a plan for something the apply would not do.
func TestADryRunNamesTheEntityKindThatWasRequested(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Now: func() time.Time { return now }}

	result, err := runner.Run(context.Background(), Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		EntityKind:           EntityKindOrganization,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"create_github_app_credential", "create_organization", "create_disabled_scale_set"}
	if result.Applied || !reflect.DeepEqual(result.Actions, want) {
		t.Fatalf("organization dry-run planned %v, want %v", result.Actions, want)
	}
}
