package garmbootstrap

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The App is installed account-wide so one fleet can serve every repository the
// owner holds. That is the only scope property that widened: the bundle must
// still be rejected for any other selection, and every other identity and
// permission assertion still has to hold. Cover all three cases together so a
// future edit cannot quietly turn "all is allowed" into "anything is allowed".
func TestBundleRepositorySelection(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	for _, testCase := range []struct {
		selection string
		wantError string
	}{
		{selection: "selected"},
		{selection: "all"},
		{selection: "none", wantError: "want selected or all"},
		{selection: "", wantError: "want selected or all"},
	} {
		t.Run(testCase.selection, func(t *testing.T) {
			bundlePath := bundleWithSelection(t, now, testCase.selection)
			bundle, err := loadAppBundleForTest(bundlePath, now)
			if testCase.wantError != "" {
				if err == nil {
					clear(bundle.PrivateKey)
					t.Fatalf("selection %q was accepted", testCase.selection)
				}
				if !strings.Contains(err.Error(), testCase.wantError) {
					t.Fatalf("error %q does not mention %q", err, testCase.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("selection %q was rejected: %v", testCase.selection, err)
			}
			defer clear(bundle.PrivateKey)
			if bundle.Installation.Repository != testTenant().Repository {
				t.Errorf("bundle repository is %q, want %q", bundle.Installation.Repository, testTenant().Repository)
			}
			if bundle.Installation.AccountLogin != testTenant().Owner {
				t.Errorf("bundle account is %q, want the managed owner", bundle.Installation.AccountLogin)
			}
		})
	}
}

// A wider repository selection must not become a licence to widen permissions.
func TestBundleRejectsExtraPermissionWhenAccountWide(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	bundlePath := bundleWithSelection(t, now, "all")
	writePrivateJSON(t, filepath.Join(bundlePath, "installation.json"), verifiedInstallation{
		SchemaVersion:       1,
		AppID:               12345,
		AppSlug:             "example-actions-fleet",
		InstallationID:      67890,
		AccountLogin:        testTenant().Owner,
		OwnerType:           OwnerTypeOrganization,
		Repository:          testTenant().Repository,
		RepositorySelection: "all",
		Permissions:         map[string]string{"administration": "write", "metadata": "read", "contents": "write"},
		PrivateKeyPath:      "/staging/github-app-private-key.pem",
		VerifiedAt:          now,
	})
	bundle, err := loadAppBundleForTest(bundlePath, now)
	if err == nil {
		clear(bundle.PrivateKey)
		t.Fatal("an account-wide bundle with an extra permission was accepted")
	}
	if !strings.Contains(err.Error(), "permissions") {
		t.Fatalf("error %q does not mention permissions", err)
	}
}

func bundleWithSelection(t *testing.T, now time.Time, selection string) string {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "bundle")
	if err := os.Mkdir(bundlePath, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	if err := os.WriteFile(filepath.Join(bundlePath, "github-app-private-key.pem"), keyData, 0o600); err != nil {
		t.Fatal(err)
	}
	writePrivateJSON(t, filepath.Join(bundlePath, "installation.json"), verifiedInstallation{
		SchemaVersion:       1,
		AppID:               12345,
		AppSlug:             "example-actions-fleet",
		InstallationID:      67890,
		AccountLogin:        testTenant().Owner,
		OwnerType:           OwnerTypeOrganization,
		Repository:          testTenant().Repository,
		RepositorySelection: selection,
		Permissions:         map[string]string{"administration": "write", "metadata": "read"},
		PrivateKeyPath:      "/staging/github-app-private-key.pem",
		VerifiedAt:          now,
	})
	return bundlePath
}
