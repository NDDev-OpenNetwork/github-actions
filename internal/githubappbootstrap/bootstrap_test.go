package githubappbootstrap

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBuildManifestIsPrivateLeastPrivilegeAndWebhookFree(t *testing.T) {
	t.Parallel()

	manifest := buildManifest(Options{
		Repository:  "NDDev-OpenNetwork/github-actions",
		AppName:     "nddev-gha-fleet",
		HomepageURL: "https://github.com/NDDev-OpenNetwork/github-actions",
	}, "http://127.0.0.1:43210")
	if manifest.Public || manifest.HookAttributes.Active || manifest.RequestOAuth || manifest.SetupOnUpdate {
		t.Fatalf("unsafe manifest switches: %#v", manifest)
	}
	if len(manifest.DefaultEvents) != 0 {
		t.Fatalf("unexpected webhook events: %v", manifest.DefaultEvents)
	}
	if len(manifest.DefaultPermissions) != 1 || manifest.DefaultPermissions["administration"] != "write" {
		t.Fatalf("unexpected permissions: %v", manifest.DefaultPermissions)
	}
	for _, callback := range []string{manifest.RedirectURL, manifest.SetupURL} {
		parsed, err := url.Parse(callback)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Scheme != "http" || parsed.Host != "127.0.0.1:43210" || parsed.RawQuery != "" || parsed.Fragment != "" {
			t.Fatalf("callback escaped loopback or contains a pre-existing query or fragment: %s", callback)
		}
	}
	if manifest.RedirectURL != "http://127.0.0.1:43210/callback" || manifest.SetupURL != "http://127.0.0.1:43210/installed" {
		t.Fatalf("unexpected callback paths: %#v", manifest)
	}
}

func TestValidateLeastPrivilegePermissions(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		permissions map[string]string
		wantError   string
	}{
		"exact":       {map[string]string{"administration": "write", "metadata": "read"}, ""},
		"implicit":    {map[string]string{"administration": "write"}, ""},
		"too weak":    {map[string]string{"administration": "read"}, "want write"},
		"extra scope": {map[string]string{"administration": "write", "contents": "read"}, "unexpected contents=read"},
		// An App that will not back an organization entity must not carry the
		// organization permission either. Least privilege is symmetric.
		"organization scope without the flag": {
			map[string]string{"administration": "write", OrganizationRunnersPermission: "write"},
			"unexpected " + OrganizationRunnersPermission + "=write",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateLeastPrivilegePermissions(testCase.permissions, false)
			if testCase.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if testCase.wantError != "" && (err == nil || !strings.Contains(err.Error(), testCase.wantError)) {
				t.Fatalf("error %v does not contain %q", err, testCase.wantError)
			}
		})
	}
}

// The organization path was unreachable: the guard required the permission and
// the loop below it called the same name unexpected, so every App that actually
// held the grant was refused by its own validator.
func TestValidateLeastPrivilegePermissionsAcceptsTheGrantedOrganizationScope(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		permissions map[string]string
		wantError   string
	}{
		"granted": {
			map[string]string{
				"administration":              "write",
				"metadata":                    "read",
				OrganizationRunnersPermission: "write",
			},
			"",
		},
		"missing": {
			map[string]string{"administration": "write", "metadata": "read"},
			OrganizationRunnersPermission + " permission is \"\"",
		},
		"read only": {
			map[string]string{
				"administration":              "write",
				"metadata":                    "read",
				OrganizationRunnersPermission: "read",
			},
			"want write",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := validateLeastPrivilegePermissions(testCase.permissions, true)
			if testCase.wantError == "" {
				if err != nil {
					t.Fatalf("granted organization scope rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("error %v does not contain %q", err, testCase.wantError)
			}
		})
	}
}

func TestSignAppJWT(t *testing.T) {
	t.Parallel()

	key := testPrivateKey(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	token, err := signAppJWT(key, 12345, now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected JWT shape: %q", token)
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
		Issuer    int64 `json:"iss"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Issuer != 12345 || claims.IssuedAt != now.Add(-time.Minute).Unix() || claims.ExpiresAt != now.Add(9*time.Minute).Unix() {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Fatalf("verify JWT: %v", err)
	}
}

func TestRunnerCompletesVerifiedManifestFlow(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}))
	api := fakeGitHubAPI(t, privateKeyPEM)
	defer api.Close()

	outputDirectory := filepath.Join(t.TempDir(), "credential-bundle")
	browserDone := make(chan error, 1)
	var bootstrapOutput strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runner := Runner{
		APIBaseURL: api.URL,
		OpenURL: func(startURL string) error {
			go func() { browserDone <- driveManifestBrowser(startURL) }()
			return nil
		},
		Random: strings.NewReader(strings.Repeat("s", 32)),
	}
	result, err := runner.Run(ctx, Options{
		ListenAddress:   "127.0.0.1:0",
		Repository:      "NDDev-OpenNetwork/github-actions",
		OwnerType:       OwnerTypeOrganization,
		AppName:         "nddev-gha-fleet",
		HomepageURL:     "https://github.com/NDDev-OpenNetwork/github-actions",
		OutputDirectory: outputDirectory,
		OpenBrowser:     true,
	}, &bootstrapOutput)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-browserDone; err != nil {
		t.Fatal(err)
	}
	if result.AppID != 12345 || result.InstallationID != 67890 || result.Repository != "NDDev-OpenNetwork/github-actions" || result.RepositorySelection != "selected" {
		t.Fatalf("unexpected verified result: %#v", result)
	}
	if !strings.Contains(bootstrapOutput.String(), "http://127.0.0.1:") {
		t.Fatalf("missing loopback bootstrap URL: %s", bootstrapOutput.String())
	}
	assertMode(t, outputDirectory, 0o700)
	assertMode(t, filepath.Join(outputDirectory, "github-app-private-key.pem"), 0o600)
	assertMode(t, filepath.Join(outputDirectory, "installation.json"), 0o600)
	keyData, err := os.ReadFile(filepath.Join(outputDirectory, "github-app-private-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if string(keyData) != privateKeyPEM {
		t.Fatal("persisted private key does not match the one-time manifest key")
	}
	metadata, err := os.ReadFile(filepath.Join(outputDirectory, "installation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "BEGIN RSA PRIVATE KEY") || !strings.Contains(string(metadata), `"repository_selection": "selected"`) {
		t.Fatalf("unsafe or incomplete metadata: %s", metadata)
	}
	if RedactedResult(result).PrivateKeyPath != "github-app-private-key.pem" {
		t.Fatalf("private key path was not redacted: %#v", RedactedResult(result))
	}
}

func TestValidateOptionsRejectsExistingOutputAndNonLoopback(t *testing.T) {
	t.Parallel()

	base := Options{
		ListenAddress:   "127.0.0.1:0",
		Repository:      "NDDev-OpenNetwork/github-actions",
		OwnerType:       OwnerTypeOrganization,
		AppName:         "nddev-gha-fleet",
		HomepageURL:     "https://github.com/NDDev-OpenNetwork/github-actions",
		OutputDirectory: filepath.Join(t.TempDir(), "new"),
	}
	unsafe := base
	unsafe.ListenAddress = "0.0.0.0:0"
	if err := validateOptions(unsafe); err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("non-loopback listener accepted: %v", err)
	}
	wrongRepository := base
	wrongRepository.Repository = "example-user/other"
	if err := validateOptions(wrongRepository); err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Fatalf("wrong repository accepted: %v", err)
	}
	if err := os.Mkdir(base.OutputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateOptions(base); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing output accepted: %v", err)
	}
}

func TestGitHubClientRefusesRedirectsWithAppCredentials(t *testing.T) {
	t.Parallel()
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		redirected.Store(true)
		if request.Header.Get("Authorization") != "" {
			t.Error("GitHub App authorization escaped the API origin")
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	client := newGitHubClient(origin.Client())
	client.baseURL = origin.URL
	if _, err := client.convertManifest(context.Background(), "test-code", "NDDev-OpenNetwork", OwnerTypeOrganization, false); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect response was accepted: %v", err)
	}
	if redirected.Load() {
		t.Fatal("GitHub API redirect was followed")
	}
}

func fakeGitHubAPI(t *testing.T, privateKeyPEM string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/app-manifests/test-code/conversions":
			writer.WriteHeader(http.StatusCreated)
			writeTestJSON(t, writer, appRegistration{
				ID:   12345,
				Slug: "nddev-gha-fleet",
				Name: "NDDev GARM fleet",
				PEM:  privateKeyPEM,
				Owner: githubAccount{
					Login: "NDDev-OpenNetwork",
					Type:  "Organization",
				},
				Permissions: map[string]string{"administration": "write", "metadata": "read"},
				Events:      []string{},
			})
		case request.Method == http.MethodGet && request.URL.Path == "/app/installations/67890":
			requireBearer(t, request, "")
			writeTestJSON(t, writer, installation{
				ID:                  67890,
				AppID:               12345,
				Account:             githubAccount{Login: "NDDev-OpenNetwork", Type: "Organization"},
				TargetType:          "Organization",
				RepositorySelection: "selected",
				Permissions:         map[string]string{"administration": "write", "metadata": "read"},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/67890/access_tokens":
			requireBearer(t, request, "")
			requestBody, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
			}
			if strings.Contains(string(requestBody), `"repositories"`) || !strings.Contains(string(requestBody), `"administration":"write"`) {
				t.Errorf("unexpected token request: %s", requestBody)
			}
			writer.WriteHeader(http.StatusCreated)
			writeTestJSON(t, writer, installationToken{
				Token:               "install-token",
				ExpiresAt:           time.Now().Add(time.Hour),
				Permissions:         map[string]string{"administration": "write", "metadata": "read"},
				RepositorySelection: "selected",
			})
		case request.Method == http.MethodGet && request.URL.Path == "/installation/repositories":
			requireBearer(t, request, "install-token")
			writeTestJSON(t, writer, map[string]any{
				"total_count":  1,
				"repositories": []map[string]string{{"full_name": "NDDev-OpenNetwork/github-actions"}},
			})
		default:
			http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
}

func driveManifestBrowser(startURL string) error {
	start, err := url.Parse(startURL)
	if err != nil {
		return err
	}
	state := strings.TrimPrefix(start.Path, "/start/")
	if state == "" {
		return fmt.Errorf("missing callback state")
	}
	response, err := http.Get(startURL)
	if err != nil {
		return err
	}
	startBody, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if response.StatusCode != http.StatusOK || !strings.Contains(string(startBody), "administration") {
		return fmt.Errorf("unexpected manifest start response")
	}

	noRedirect := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	callback := start.Scheme + "://" + start.Host + "/callback?state=" + url.QueryEscape(state) + "&code=test-code"
	response, err = noRedirect.Get(callback)
	if err != nil {
		return err
	}
	response.Body.Close()
	wantInstallURL := "https://github.com/apps/nddev-gha-fleet/installations/new?state=" + url.QueryEscape(state)
	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != wantInstallURL {
		return fmt.Errorf("unexpected manifest callback redirect: %d %s", response.StatusCode, response.Header.Get("Location"))
	}
	installed := start.Scheme + "://" + start.Host + "/installed?state=" + url.QueryEscape(state) + "&installation_id=67890&setup_action=install"
	response, err = http.Get(installed)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected installation callback status: %d", response.StatusCode)
	}
	return nil
}

func testPrivateKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func requireBearer(t *testing.T, request *http.Request, exact string) {
	t.Helper()
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		t.Errorf("missing bearer authorization")
	}
	if exact != "" && authorization != "Bearer "+exact {
		t.Errorf("authorization does not match expected installation token")
	}
}

func writeTestJSON(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		t.Error(err)
	}
}

func assertMode(t *testing.T, path string, wanted os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wanted {
		t.Fatalf("%s mode is %o, want %o", path, info.Mode().Perm(), wanted)
	}
}

// The deployed credential is an organization-owned App. The personal
// registration form cannot create one, so the endpoint the bootstrap sends the
// operator to is part of the recovery contract, not a cosmetic detail.
func TestManifestActionURLFollowsTheOwningAccount(t *testing.T) {
	t.Parallel()
	if got := manifestActionURL("NDDev-OpenNetwork", OwnerTypeOrganization); got != "https://github.com/organizations/NDDev-OpenNetwork/settings/apps/new" {
		t.Fatalf("organization form is %q", got)
	}
	if got := manifestActionURL("example-user", OwnerTypeUser); got != "https://github.com/settings/apps/new" {
		t.Fatalf("personal form is %q", got)
	}
}

func TestValidateRegistrationBindsTheOwningAccount(t *testing.T) {
	t.Parallel()
	complete := func(login, accountType string) appRegistration {
		return appRegistration{
			ID: 12345, Slug: "nddev-gha-fleet", PEM: "key",
			Owner:       githubAccount{Login: login, Type: accountType},
			Permissions: map[string]string{"administration": "write", "metadata": "read"},
			Events:      []string{},
		}
	}
	if err := validateRegistration(complete("NDDev-OpenNetwork", "Organization"), "NDDev-OpenNetwork", OwnerTypeOrganization, false); err != nil {
		t.Fatalf("the deployed organization App was rejected: %v", err)
	}
	for name, registration := range map[string]appRegistration{
		"personal owner where an organization is expected": complete("example-user", "User"),
		"another organization":                             complete("someone-else", "Organization"),
		"right login, wrong account kind":                  complete("NDDev-OpenNetwork", "User"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateRegistration(registration, "NDDev-OpenNetwork", OwnerTypeOrganization, false); err == nil {
				t.Fatal("registration was accepted")
			}
		})
	}
}

func TestValidateInstallationBindsTheSameAccount(t *testing.T) {
	t.Parallel()
	observed := func(login, accountType, targetType string) installation {
		return installation{
			ID: 67890, AppID: 12345,
			Account:             githubAccount{Login: login, Type: accountType},
			TargetType:          targetType,
			RepositorySelection: "all",
			Permissions:         map[string]string{"administration": "write", "metadata": "read"},
		}
	}
	if err := validateInstallation(observed("NDDev-OpenNetwork", "Organization", "Organization"), 12345, "NDDev-OpenNetwork", OwnerTypeOrganization, false); err != nil {
		t.Fatalf("the deployed organization installation was rejected: %v", err)
	}
	for name, candidate := range map[string]installation{
		"installed on a personal account": observed("example-user", "User", "User"),
		"installed on another org":        observed("someone-else", "Organization", "Organization"),
		"target type disagrees":           observed("NDDev-OpenNetwork", "Organization", "User"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateInstallation(candidate, 12345, "NDDev-OpenNetwork", OwnerTypeOrganization, false); err == nil {
				t.Fatal("installation was accepted")
			}
		})
	}
}

func TestValidateOptionsRequiresADeclaredOwnerKind(t *testing.T) {
	t.Parallel()
	base := Options{
		ListenAddress:   "127.0.0.1:0",
		Repository:      "NDDev-OpenNetwork/github-actions",
		AppName:         "nddev-gha-fleet",
		HomepageURL:     "https://github.com/NDDev-OpenNetwork/github-actions",
		OutputDirectory: filepath.Join(t.TempDir(), "new"),
	}
	if err := validateOptions(base); err == nil || !strings.Contains(err.Error(), "owner type") {
		t.Fatalf("an undeclared owner kind was accepted: %v", err)
	}
	guessed := base
	guessed.OwnerType = "Organization"
	if err := validateOptions(guessed); err == nil {
		t.Fatal("a capitalized account type was accepted as an owner kind")
	}
}

// The reconciler decides whether an organization entity may exist by reading
// the permission set this function records. Verifying the grant and then not
// writing it down refused the entity the App had just been proven to serve.
func TestVerifyInstallationRecordsTheOrganizationScopeItProved(t *testing.T) {
	t.Parallel()

	privateKey := testPrivateKey(t)
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	}))
	granted := map[string]string{
		"administration":              "write",
		"metadata":                    "read",
		OrganizationRunnersPermission: "write",
	}

	var tokenRequestBody string
	api := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/app/installations/67890":
			writeTestJSON(t, writer, installation{
				ID:                  67890,
				AppID:               12345,
				Account:             githubAccount{Login: "example-guild", Type: "Organization"},
				TargetType:          "Organization",
				RepositorySelection: "all",
				Permissions:         granted,
			})
		case request.Method == http.MethodPost && request.URL.Path == "/app/installations/67890/access_tokens":
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Error(err)
			}
			tokenRequestBody = string(body)
			writer.WriteHeader(http.StatusCreated)
			writeTestJSON(t, writer, installationToken{
				Token:               "install-token",
				ExpiresAt:           time.Now().Add(time.Hour),
				Permissions:         map[string]string{"administration": "write", OrganizationRunnersPermission: "write"},
				RepositorySelection: "all",
			})
		case request.Method == http.MethodGet && request.URL.Path == "/repos/example-guild/ai_stp":
			writeTestJSON(t, writer, installationRepository{FullName: "example-guild/ai_stp"})
		default:
			http.Error(writer, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	defer api.Close()

	client := newGitHubClient(api.Client())
	client.baseURL = api.URL
	registration := appRegistration{
		ID:          12345,
		Slug:        "guild-gha-fleet",
		PEM:         privateKeyPEM,
		Owner:       githubAccount{Login: "example-guild", Type: "Organization"},
		Permissions: granted,
	}

	verified, err := client.verifyInstallation(
		t.Context(), registration, 67890, "example-guild/ai_stp", OwnerTypeOrganization, true,
	)
	if err != nil {
		t.Fatalf("organization installation refused: %v", err)
	}
	if verified.Permissions[OrganizationRunnersPermission] != "write" {
		t.Fatalf("recorded permissions %v drop the organization scope", verified.Permissions)
	}
	// Asking for less than it validates would make the probe fail on its own
	// request, so the requested scope is part of the contract, not an accident.
	if !strings.Contains(tokenRequestBody, `"`+OrganizationRunnersPermission+`":"write"`) {
		t.Fatalf("token request %s does not ask for the organization scope", tokenRequestBody)
	}
}
