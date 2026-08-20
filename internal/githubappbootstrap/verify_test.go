package githubappbootstrap

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeTestKey(t *testing.T) string {
	t.Helper()
	key := testPrivateKey(t)
	path := filepath.Join(t.TempDir(), "github-app-private-key.pem")
	data := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func liveInstallation(t *testing.T, granted map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app/installations/153066649":
			writeTestJSON(t, w, installation{
				ID: 153066649, AppID: 4565021,
				Account:             githubAccount{Login: "example-media", Type: "Organization"},
				TargetType:          "Organization",
				RepositorySelection: "all",
				Permissions:         granted,
			})
		case "/app/installations/153066649/access_tokens":
			w.WriteHeader(http.StatusCreated)
			writeTestJSON(t, w, installationToken{
				Token: "install-token", ExpiresAt: time.Now().Add(time.Hour),
				Permissions: granted, RepositorySelection: "all",
			})
		case "/repos/example-media/example-service":
			writeTestJSON(t, w, installationRepository{FullName: "example-media/example-service"})
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
}

func verifyOptions(key, out string) VerifyOptions {
	return VerifyOptions{
		Tenant: "example-media", OwnerType: OwnerTypeOrganization,
		OrganizationRunners: true, AppID: 4565021, InstallationID: 153066649,
		PrivateKeyPath: key, OutputDirectory: out,
	}
}

// The case this command exists for: an App whose permission was approved after
// its bundle was written. Before it, the only remedy was a second App.
func TestReVerifyingWritesTheCurrentPermissions(t *testing.T) {
	t.Parallel()
	api := liveInstallation(t, map[string]string{
		"administration": "write", ActionsReadPermission: "read", "metadata": "read",
		OrganizationRunnersPermission: "write",
	})
	defer api.Close()

	out := filepath.Join(t.TempDir(), "bundle")
	got, err := Runner{APIBaseURL: api.URL}.Verify(
		context.Background(), verifyOptions(writeTestKey(t), out))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if got.Permissions[OrganizationRunnersPermission] != "write" {
		t.Fatalf("the refreshed record dropped the grant: %v", got.Permissions)
	}

	var onDisk VerifiedInstallation
	data, err := os.ReadFile(filepath.Join(out, "installation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Permissions[OrganizationRunnersPermission] != "write" {
		t.Fatalf("the file is what Reconcile reads, and it is stale: %v", onDisk.Permissions)
	}
	if onDisk.AppSlug != "example-media-fleet" || onDisk.RepositorySelection != "all" {
		t.Fatalf("identity was not carried through: %+v", onDisk)
	}
}

// It must refuse everything the bootstrap refuses. Re-verification is not a
// second, weaker door to a credential bundle.
func TestReVerifyingRefusesAnInstallationOnAnotherAccount(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeTestJSON(t, w, installation{
			ID: 153066649, AppID: 4565021,
			Account:             githubAccount{Login: "Someone-Else", Type: "Organization"},
			TargetType:          "Organization",
			RepositorySelection: "all",
			Permissions: map[string]string{
				"administration": "write", ActionsReadPermission: "read", "metadata": "read",
				OrganizationRunnersPermission: "write",
			},
		})
	}))
	defer api.Close()

	out := filepath.Join(t.TempDir(), "bundle")
	if _, err := (Runner{APIBaseURL: api.URL}).Verify(
		context.Background(), verifyOptions(writeTestKey(t), out)); err == nil {
		t.Fatal("an installation on another account was accepted")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("a refused verification left a bundle behind")
	}
}

func TestReVerifyingRefusesAKeyOthersCanRead(t *testing.T) {
	t.Parallel()
	key := writeTestKey(t)
	if err := os.Chmod(key, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (Runner{}).Verify(context.Background(),
		verifyOptions(key, filepath.Join(t.TempDir(), "bundle")))
	if err == nil {
		t.Fatal("a world-readable private key was accepted")
	}
}

func TestReVerifyingRefusesAnUnknownTenant(t *testing.T) {
	t.Parallel()
	options := verifyOptions(writeTestKey(t), filepath.Join(t.TempDir(), "bundle"))
	options.Tenant = "does-not-exist"
	if _, err := (Runner{}).Verify(context.Background(), options); err == nil {
		t.Fatal("an unknown tenant was accepted")
	}
}
