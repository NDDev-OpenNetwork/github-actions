package githubappbootstrap

import (
	"bytes"
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
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAPIBaseURL = "https://api.github.com"
	githubAPIVersion  = "2026-03-10"
	maxResponseBytes  = 2 << 20
)

type githubClient struct {
	baseURL string
	client  *http.Client
	now     func() time.Time
}

type appRegistration struct {
	ID          int64             `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	PEM         string            `json:"pem"`
	Owner       githubAccount     `json:"owner"`
	Permissions map[string]string `json:"permissions"`
	Events      []string          `json:"events"`
}

type githubAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"`
}

type installation struct {
	ID                  int64             `json:"id"`
	AppID               int64             `json:"app_id"`
	Account             githubAccount     `json:"account"`
	TargetType          string            `json:"target_type"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
}

type installationToken struct {
	Token               string            `json:"token"`
	ExpiresAt           time.Time         `json:"expires_at"`
	Permissions         map[string]string `json:"permissions"`
	RepositorySelection string            `json:"repository_selection"`
}

type installationRepository struct {
	FullName string `json:"full_name"`
}

type installationRepositories struct {
	TotalCount   int                      `json:"total_count"`
	Repositories []installationRepository `json:"repositories"`
}

func newGitHubClient(client *http.Client) githubClient {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	localClient := *client
	localClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return githubClient{baseURL: defaultAPIBaseURL, client: &localClient, now: time.Now}
}

func (c githubClient) convertManifest(ctx context.Context, code, owner, ownerType string, organizationRunners bool) (appRegistration, error) {
	if code == "" || strings.ContainsAny(code, "/\\") {
		return appRegistration{}, fmt.Errorf("invalid manifest conversion code")
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/app-manifests/" + url.PathEscape(code) + "/conversions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return appRegistration{}, fmt.Errorf("create manifest conversion request: %w", err)
	}
	setGitHubHeaders(request)

	var registration appRegistration
	if err := c.doJSON(request, http.StatusCreated, &registration); err != nil {
		return appRegistration{}, fmt.Errorf("convert GitHub App manifest: %w", err)
	}
	if err := validateRegistration(registration, owner, ownerType, organizationRunners); err != nil {
		return appRegistration{}, err
	}
	return registration, nil
}

func (c githubClient) verifyInstallation(ctx context.Context, registration appRegistration, installationID int64, repository, ownerType string, organizationRunners bool) (VerifiedInstallation, error) {
	owner, _, err := splitRepository(repository)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	privateKeyPEM := []byte(registration.PEM)
	defer clear(privateKeyPEM)
	privateKey, err := parseRSAPrivateKey(privateKeyPEM)
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("parse manifest private key: %w", err)
	}
	jwt, err := signAppJWT(privateKey, registration.ID, c.now())
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("sign GitHub App JWT: %w", err)
	}

	var observed installation
	installURL := strings.TrimRight(c.baseURL, "/") + "/app/installations/" + strconv.FormatInt(installationID, 10)
	request, err := authenticatedRequest(ctx, http.MethodGet, installURL, nil, jwt)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	if err := c.doJSON(request, http.StatusOK, &observed); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("inspect GitHub App installation: %w", err)
	}
	if err := validateInstallation(observed, registration.ID, owner, ownerType, organizationRunners); err != nil {
		return VerifiedInstallation{}, err
	}

	// The probe token asks for exactly the set it is about to be judged
	// against. Asking for less and validating for more can only fail; asking
	// for the organization permission also proves the installation can really
	// mint it, which is what the runner registration will need at runtime.
	wantedTokenPermissions := map[string]string{"administration": "write"}
	if organizationRunners {
		wantedTokenPermissions[OrganizationRunnersPermission] = "write"
	}
	tokenRequest := struct {
		Permissions map[string]string `json:"permissions"`
	}{
		Permissions: wantedTokenPermissions,
	}
	body, err := json.Marshal(tokenRequest)
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("encode installation token request: %w", err)
	}
	tokenURL := installURL + "/access_tokens"
	request, err = authenticatedRequest(ctx, http.MethodPost, tokenURL, bytes.NewReader(body), jwt)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	var token installationToken
	if err := c.doJSON(request, http.StatusCreated, &token); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("mint installation verification token: %w", err)
	}
	if token.Token == "" || !token.ExpiresAt.After(c.now()) {
		return VerifiedInstallation{}, fmt.Errorf("GitHub returned an empty or expired installation token")
	}
	if !validRepositorySelection(token.RepositorySelection) {
		return VerifiedInstallation{}, fmt.Errorf("installation token repository selection is %q, want selected or all", token.RepositorySelection)
	}
	if err := validateLeastPrivilegePermissions(token.Permissions, organizationRunners); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("installation token permissions: %w", err)
	}

	if token.RepositorySelection == "selected" {
		// A narrow installation is enumerable, so hold it to the exact single
		// repository it claims to be scoped to.
		repositoriesURL := strings.TrimRight(c.baseURL, "/") + "/installation/repositories?per_page=100"
		request, err = authenticatedRequest(ctx, http.MethodGet, repositoriesURL, nil, token.Token)
		if err != nil {
			return VerifiedInstallation{}, err
		}
		var repositories installationRepositories
		if err := c.doJSON(request, http.StatusOK, &repositories); err != nil {
			return VerifiedInstallation{}, fmt.Errorf("inspect installation repositories: %w", err)
		}
		if repositories.TotalCount != 1 || len(repositories.Repositories) != 1 || repositories.Repositories[0].FullName != repository {
			return VerifiedInstallation{}, fmt.Errorf("installation must expose exactly %q", repository)
		}
	} else {
		// An account-wide installation cannot be enumerated in one page, and
		// paginating it would prove less than asking directly. Resolving the
		// managed repository through the installation token itself is the exact
		// property that matters: this credential really does cover it.
		repositoryURL := strings.TrimRight(c.baseURL, "/") + "/repos/" + repository
		request, err = authenticatedRequest(ctx, http.MethodGet, repositoryURL, nil, token.Token)
		if err != nil {
			return VerifiedInstallation{}, err
		}
		var covered installationRepository
		if err := c.doJSON(request, http.StatusOK, &covered); err != nil {
			return VerifiedInstallation{}, fmt.Errorf("resolve %q through the installation: %w", repository, err)
		}
		if covered.FullName != repository {
			return VerifiedInstallation{}, fmt.Errorf("installation resolved %q, want %q", covered.FullName, repository)
		}
	}

	// What gets written is what was verified, and the only thing that cannot
	// drift from it is the observed set itself. Restating the expected pair
	// here meant two places had to agree about a set that
	// validateLeastPrivilegePermissions had already checked exactly, so a
	// third permission added there would silently fail to be recorded. The
	// reconciler reads this to decide whether an organization entity may
	// exist at all, which is precisely the decision that must not read a
	// reconstruction.
	verifiedPermissions := maps.Clone(observed.Permissions)
	return VerifiedInstallation{
		AppID:               registration.ID,
		AppSlug:             registration.Slug,
		InstallationID:      installationID,
		AccountLogin:        owner,
		OwnerType:           ownerType,
		Repository:          repository,
		RepositorySelection: observed.RepositorySelection,
		Permissions:         verifiedPermissions,
		VerifiedAt:          c.now().UTC(),
	}, nil
}

func (c githubClient) doJSON(request *http.Request, wantedStatus int, output any) error {
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(data) > maxResponseBytes {
		clear(data)
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	defer clear(data)
	if response.StatusCode != wantedStatus {
		return fmt.Errorf("GitHub API returned HTTP %d", response.StatusCode)
	}
	if err := json.Unmarshal(data, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func authenticatedRequest(ctx context.Context, method, endpoint string, body io.Reader, token string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create GitHub API request: %w", err)
	}
	setGitHubHeaders(request)
	request.Header.Set("Authorization", "Bearer "+token)
	return request, nil
}

func setGitHubHeaders(request *http.Request) {
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	request.Header.Set("User-Agent", "example-actions-fleet")
}

func validateRegistration(registration appRegistration, owner, ownerType string, organizationRunners bool) error {
	if registration.ID <= 0 || registration.Slug == "" || registration.PEM == "" {
		return fmt.Errorf("manifest conversion returned incomplete GitHub App identity")
	}
	if !strings.EqualFold(registration.Owner.Login, owner) || registration.Owner.Type != accountType(ownerType) {
		return fmt.Errorf("GitHub App owner is %q (%s), want %q (%s)",
			registration.Owner.Login, registration.Owner.Type, owner, accountType(ownerType))
	}
	if err := validateLeastPrivilegePermissions(registration.Permissions, organizationRunners); err != nil {
		return fmt.Errorf("GitHub App permissions: %w", err)
	}
	if len(registration.Events) != 0 {
		return fmt.Errorf("GitHub App unexpectedly subscribes to webhook events")
	}
	return nil
}

// The App is installed account-wide so one fleet can serve every repository the
// account holds, so an account-wide selection is expected rather than a
// mistake. What must never widen is the rest: the installation stays on the
// account that owns the managed repository, carries exactly
// administration=write plus metadata=read, subscribes to no webhook events, and
// is proven to actually cover the repository being managed. `selected` remains
// valid for a narrower install.
func validRepositorySelection(selection string) bool {
	return selection == "selected" || selection == "all"
}

// accountType maps the declared owner kind onto the capitalized account type
// GitHub reports for it.
func accountType(ownerType string) string {
	if ownerType == OwnerTypeUser {
		return "User"
	}
	return "Organization"
}

func validateInstallation(observed installation, appID int64, owner, ownerType string, organizationRunners bool) error {
	if observed.ID <= 0 || observed.AppID != appID {
		return fmt.Errorf("GitHub App installation identity does not match the created App")
	}
	// The installation must land on the same account that owns the App and the
	// managed repository. Checking the login is the security property; the two
	// type fields corroborate it and keep a renamed account from passing.
	wanted := accountType(ownerType)
	if !strings.EqualFold(observed.Account.Login, owner) || observed.Account.Type != wanted || observed.TargetType != wanted {
		return fmt.Errorf("GitHub App installation target is %q (%s/%s), want %q (%s)",
			observed.Account.Login, observed.Account.Type, observed.TargetType, owner, wanted)
	}
	if !validRepositorySelection(observed.RepositorySelection) {
		return fmt.Errorf("GitHub App installation repository selection is %q, want selected or all", observed.RepositorySelection)
	}
	if err := validateLeastPrivilegePermissions(observed.Permissions, organizationRunners); err != nil {
		return fmt.Errorf("GitHub App installation permissions: %w", err)
	}
	return nil
}

func validateLeastPrivilegePermissions(permissions map[string]string, organizationRunners bool) error {
	if permissions["administration"] != "write" {
		return fmt.Errorf("administration permission is %q, want write", permissions["administration"])
	}
	if organizationRunners && permissions[OrganizationRunnersPermission] != "write" {
		return fmt.Errorf("%s permission is %q, want write", OrganizationRunnersPermission, permissions[OrganizationRunnersPermission])
	}
	for name, level := range permissions {
		switch name {
		case "administration":
			if level != "write" {
				return fmt.Errorf("administration permission is %q, want write", level)
			}
		case "metadata":
			if level != "read" {
				return fmt.Errorf("metadata permission is %q, want read", level)
			}
		case OrganizationRunnersPermission:
			// Least privilege cuts both ways. An App that will back an
			// organization entity needs this and is required to hold it by the
			// guard above; an App that will not must never carry it, so the
			// same name is unexpected in that case.
			if !organizationRunners {
				return fmt.Errorf("unexpected %s=%s permission", name, level)
			}
			if level != "write" {
				return fmt.Errorf("%s permission is %q, want write", name, level)
			}
		default:
			return fmt.Errorf("unexpected %s=%s permission", name, level)
		}
	}
	return nil
}

func parseRSAPrivateKey(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("invalid PEM encoding")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return validateRSAKey(key)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("private key is not RSA")
	}
	return validateRSAKey(key)
}

func validateRSAKey(key *rsa.PrivateKey) (*rsa.PrivateKey, error) {
	if err := key.Validate(); err != nil {
		return nil, fmt.Errorf("validate RSA private key: %w", err)
	}
	if key.N.BitLen() < 2048 {
		return nil, fmt.Errorf("RSA private key is smaller than 2048 bits")
	}
	return key, nil
}

func signAppJWT(privateKey *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	if privateKey == nil || appID <= 0 {
		return "", fmt.Errorf("invalid signing identity")
	}
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		IssuedAt  int64 `json:"iat"`
		ExpiresAt int64 `json:"exp"`
		Issuer    int64 `json:"iss"`
	}{
		IssuedAt:  now.Add(-60 * time.Second).Unix(),
		ExpiresAt: now.Add(9 * time.Minute).Unix(),
		Issuer:    appID,
	})
	if err != nil {
		return "", err
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func splitRepository(repository string) (string, string, error) {
	if strings.TrimSpace(repository) != repository || strings.Count(repository, "/") != 1 {
		return "", "", fmt.Errorf("repository must be canonical owner/name")
	}
	owner, name, found := strings.Cut(repository, "/")
	if !found || !validGitHubName(owner) || !validGitHubName(name) {
		return "", "", fmt.Errorf("repository must be canonical owner/name")
	}
	return owner, name, nil
}

func validGitHubName(value string) bool {
	if value == "" || len(value) > 100 || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func isContextError(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
