package githubappbootstrap

import (
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The account that owns a GitHub App decides which registration form can
// create it. An organization-owned App is not reachable through the personal
// form at all, so picking the wrong one is the difference between a bootstrap
// that can recreate the deployed credential and one that cannot.
const (
	// OrganizationRunnersPermission is what POST /orgs/{org}/actions/runners/
	// registration-token requires. Repository `administration` does not cover
	// it: an organization entity without this permission creates its GARM
	// entity and then fails on the first scale set, which is the shape this
	// deployment hit.
	OrganizationRunnersPermission = "organization_self_hosted_runners"
	OwnerTypeOrganization         = "organization"
	OwnerTypeUser                 = "user"

	personalManifestActionURL = "https://github.com/settings/apps/new"
)

func manifestActionURL(owner, ownerType string) string {
	if ownerType == OwnerTypeUser {
		return personalManifestActionURL
	}
	return "https://github.com/organizations/" + url.PathEscape(owner) + "/settings/apps/new"
}

type Options struct {
	Registry tenant.Registry
	// Tenant names which registered account this App will serve. Empty means
	// the default tenant, so an existing invocation keeps working unchanged.
	Tenant string
	// OrganizationRunners requests the organization runner permission. Set it
	// when the App will back an organization entity; leave it off for a
	// repository entity, which does not need it.
	OrganizationRunners bool
	ListenAddress       string
	Repository          string
	// OwnerType is the kind of account that owns the App and receives its
	// installation. Both are the repository owner: the fleet credential is
	// owned by, installed on, and scoped to one account. A mixed arrangement
	// is not a supported deployment and fails closed.
	OwnerType       string
	AppName         string
	HomepageURL     string
	OutputDirectory string
	OpenBrowser     bool
}

type VerifiedInstallation struct {
	SchemaVersion       int               `json:"schema_version"`
	AppID               int64             `json:"app_id"`
	AppSlug             string            `json:"app_slug"`
	InstallationID      int64             `json:"installation_id"`
	AccountLogin        string            `json:"account_login"`
	OwnerType           string            `json:"owner_type"`
	Repository          string            `json:"repository"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	PrivateKeyPath      string            `json:"private_key_path"`
	VerifiedAt          time.Time         `json:"verified_at"`
}

type Runner struct {
	HTTPClient *http.Client
	APIBaseURL string
	OpenURL    func(string) error
	Random     io.Reader
}

type appManifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	Description        string            `json:"description"`
	RedirectURL        string            `json:"redirect_url"`
	SetupURL           string            `json:"setup_url"`
	Public             bool              `json:"public"`
	DefaultPermissions map[string]string `json:"default_permissions"`
	DefaultEvents      []string          `json:"default_events"`
	HookAttributes     hookAttributes    `json:"hook_attributes"`
	RequestOAuth       bool              `json:"request_oauth_on_install"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
}

type hookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

type flowState struct {
	mutex                sync.Mutex
	registration         *appRegistration
	installationConsumed bool
	result               chan flowResult
}

type flowResult struct {
	installation VerifiedInstallation
	err          error
}

func (r Runner) Run(ctx context.Context, options Options, stdout io.Writer) (VerifiedInstallation, error) {
	if err := validateOptions(options); err != nil {
		return VerifiedInstallation{}, err
	}
	random := r.Random
	if random == nil {
		random = rand.Reader
	}
	state, err := randomState(random)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	listener, err := net.Listen("tcp", options.ListenAddress)
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("listen for GitHub App callback: %w", err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	if !address.IP.IsLoopback() {
		return VerifiedInstallation{}, fmt.Errorf("callback listener resolved outside loopback")
	}
	callbackBase := "http://127.0.0.1:" + strconv.Itoa(address.Port)
	manifest := buildManifest(options, callbackBase)
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("encode GitHub App manifest: %w", err)
	}

	client := newGitHubClient(r.HTTPClient)
	if r.APIBaseURL != "" {
		client.baseURL = r.APIBaseURL
	}
	flow := &flowState{result: make(chan flowResult, 1)}
	mux := http.NewServeMux()
	startPath := "/start/" + state
	owner, _, err := splitRepository(options.Repository)
	if err != nil {
		return VerifiedInstallation{}, err
	}
	mux.HandleFunc(startPath, startHandler(state, manifestJSON, manifestActionURL(owner, options.OwnerType)))
	mux.HandleFunc("/callback", flow.callbackHandler(client, options, state))
	mux.HandleFunc("/installed", flow.installedHandler(client, options, state))
	server := &http.Server{
		Handler:           securityHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	defer server.Close()
	serverErrors := make(chan error, 1)
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			serverErrors <- serveErr
		}
	}()

	startURL := callbackBase + startPath
	if _, err := fmt.Fprintf(stdout, "Open this loopback URL to register the dedicated GitHub App:\n%s\n", startURL); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("write bootstrap URL: %w", err)
	}
	if options.OpenBrowser {
		opener := r.OpenURL
		if opener == nil {
			opener = openBrowser
		}
		if err := opener(startURL); err != nil {
			return VerifiedInstallation{}, fmt.Errorf("open browser: %w", err)
		}
	}

	var result flowResult
	select {
	case result = <-flow.result:
	case err := <-serverErrors:
		return VerifiedInstallation{}, fmt.Errorf("serve GitHub App callback: %w", err)
	case <-ctx.Done():
		return VerifiedInstallation{}, ctx.Err()
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("shutdown GitHub App callback: %w", err)
	}
	if result.err != nil {
		return VerifiedInstallation{}, result.err
	}
	return result.installation, nil
}

// manifestPermissions keeps least privilege the default. Organization runner
// endpoints need an organization permission that repository endpoints do not,
// and an App that only ever backs a repository entity has no business holding
// it, so it is requested on demand rather than always.
func manifestPermissions(organizationRunners bool) map[string]string {
	permissions := map[string]string{"administration": "write"}
	if organizationRunners {
		permissions[OrganizationRunnersPermission] = "write"
	}
	return permissions
}

func buildManifest(options Options, callbackBase string) appManifest {
	return appManifest{
		Name:               options.AppName,
		URL:                options.HomepageURL,
		Description:        "One-job ephemeral GitHub Actions runner management for the NDDev fleet.",
		RedirectURL:        callbackBase + "/callback",
		SetupURL:           callbackBase + "/installed",
		Public:             false,
		DefaultPermissions: manifestPermissions(options.OrganizationRunners),
		DefaultEvents:      []string{},
		HookAttributes: hookAttributes{
			URL:    options.HomepageURL,
			Active: false,
		},
		RequestOAuth:  false,
		SetupOnUpdate: false,
	}
}

func validateOptions(options Options) error {
	if options.ListenAddress == "" {
		return fmt.Errorf("listen address is required")
	}
	if options.OwnerType != OwnerTypeOrganization && options.OwnerType != OwnerTypeUser {
		return fmt.Errorf("owner type is %q, want %q or %q", options.OwnerType, OwnerTypeOrganization, OwnerTypeUser)
	}
	host, _, err := net.SplitHostPort(options.ListenAddress)
	if err != nil {
		return fmt.Errorf("parse listen address: %w", err)
	}
	if host != "127.0.0.1" {
		return fmt.Errorf("listen address must use 127.0.0.1")
	}
	owner, _, err := splitRepository(options.Repository)
	if err != nil {
		return err
	}
	// Identity comes from the tenant registry, not from these arguments. An
	// operator names an account; they cannot describe one. Registering an App
	// is the single irreversible step in this flow, so an account the GARM
	// reconciler would later refuse must fail here, before the key exists.
	selected, registry, err := tenant.Resolve(options.Registry, options.Tenant)
	if err != nil {
		return fmt.Errorf("%w; known tenants: %v", err, registry.IDs())
	}
	if owner == "" || options.Repository != selected.Repository {
		return fmt.Errorf("repository must be exactly %s for tenant %q", selected.Repository, selected.ID)
	}
	if options.AppName != selected.AppSlug {
		return fmt.Errorf("GitHub App name must be exactly %s for tenant %q", selected.AppSlug, selected.ID)
	}
	homepage, err := url.Parse(options.HomepageURL)
	if err != nil || homepage.String() != selected.HomepageURL || homepage.User != nil {
		return fmt.Errorf("homepage URL must be exactly %s for tenant %q", selected.HomepageURL, selected.ID)
	}
	if !filepath.IsAbs(options.OutputDirectory) || filepath.Clean(options.OutputDirectory) != options.OutputDirectory {
		return fmt.Errorf("output directory must be an absolute clean path")
	}
	if _, err := os.Stat(options.OutputDirectory); err == nil {
		return fmt.Errorf("output directory already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect output directory: %w", err)
	}
	return nil
}

func randomState(reader io.Reader) (string, error) {
	data := make([]byte, 32)
	defer clear(data)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", fmt.Errorf("generate callback state: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func startHandler(state string, manifestJSON []byte, actionURL string) http.HandlerFunc {
	page := template.Must(template.New("manifest").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>NDDev GARM GitHub App</title></head>
<body><p>Redirecting to GitHub's reviewed App registration form...</p>
<form id="register" method="post" action="{{.Action}}">
<input type="hidden" name="manifest" value="{{.Manifest}}">
<noscript><button type="submit">Register dedicated GitHub App</button></noscript>
</form><script>document.getElementById('register').submit();</script></body></html>`))
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		action := actionURL + "?" + url.Values{"state": []string{state}}.Encode()
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := page.Execute(writer, map[string]string{"Action": action, "Manifest": string(manifestJSON)}); err != nil {
			return
		}
	}
}

func (f *flowState) callbackHandler(client githubClient, options Options, state string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Query().Get("state") != state {
			http.Error(writer, "invalid callback", http.StatusBadRequest)
			return
		}
		code := request.URL.Query().Get("code")
		owner, _, err := splitRepository(options.Repository)
		if err != nil {
			f.complete(flowResult{err: err})
			http.Error(writer, "invalid repository", http.StatusBadRequest)
			return
		}
		registration, err := client.convertManifest(request.Context(), code, owner, options.OwnerType, options.OrganizationRunners)
		if err != nil {
			f.complete(flowResult{err: err})
			http.Error(writer, "manifest conversion failed", http.StatusBadGateway)
			return
		}
		f.mutex.Lock()
		if f.registration != nil {
			f.mutex.Unlock()
			http.Error(writer, "manifest callback already consumed", http.StatusConflict)
			return
		}
		f.registration = &registration
		f.mutex.Unlock()
		installURL := "https://github.com/apps/" + url.PathEscape(registration.Slug) + "/installations/new?" +
			url.Values{"state": []string{state}}.Encode()
		http.Redirect(writer, request, installURL, http.StatusSeeOther)
	}
}

func (f *flowState) installedHandler(client githubClient, options Options, state string) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Query().Get("state") != state {
			http.Error(writer, "invalid setup callback", http.StatusBadRequest)
			return
		}
		installationID, err := strconv.ParseInt(request.URL.Query().Get("installation_id"), 10, 64)
		if err != nil || installationID <= 0 {
			http.Error(writer, "invalid installation identity", http.StatusBadRequest)
			return
		}
		f.mutex.Lock()
		if f.installationConsumed {
			f.mutex.Unlock()
			http.Error(writer, "setup callback already consumed", http.StatusConflict)
			return
		}
		if f.registration == nil {
			f.mutex.Unlock()
			http.Error(writer, "manifest callback has not completed", http.StatusConflict)
			return
		}
		f.installationConsumed = true
		registration := *f.registration
		f.mutex.Unlock()
		verified, err := client.verifyInstallation(request.Context(), registration, installationID, options.Repository, options.OwnerType, options.OrganizationRunners)
		if err == nil {
			verified, err = persistCredentials(options.OutputDirectory, registration, verified)
		}
		registration.PEM = ""
		f.mutex.Lock()
		f.registration.PEM = ""
		f.mutex.Unlock()
		if err != nil {
			f.complete(flowResult{err: err})
			http.Error(writer, "installation verification failed", http.StatusBadGateway)
			return
		}
		f.complete(flowResult{installation: verified})
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(writer, "<!doctype html><title>NDDev GARM App verified</title><p>Verified. You may close this tab.</p>")
	}
}

func (f *flowState) complete(result flowResult) {
	select {
	case f.result <- result:
	default:
	}
}

func persistCredentials(directory string, registration appRegistration, verified VerifiedInstallation) (VerifiedInstallation, error) {
	if err := os.Mkdir(directory, 0o700); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("create credential output directory: %w", err)
	}
	privateKeyPath := filepath.Join(directory, "github-app-private-key.pem")
	metadataPath := filepath.Join(directory, "installation.json")
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(metadataPath)
			_ = os.Remove(privateKeyPath)
			_ = os.Remove(directory)
		}
	}()
	if err := os.Chmod(directory, 0o700); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("secure credential output directory: %w", err)
	}
	privateKey := []byte(registration.PEM)
	defer clear(privateKey)
	if err := writeExclusive(privateKeyPath, privateKey, 0o600); err != nil {
		return VerifiedInstallation{}, err
	}
	verified.SchemaVersion = 1
	verified.PrivateKeyPath = privateKeyPath
	metadata, err := json.MarshalIndent(verified, "", "  ")
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("encode verified installation: %w", err)
	}
	metadata = append(metadata, '\n')
	defer clear(metadata)
	if err := writeExclusive(metadataPath, metadata, 0o600); err != nil {
		return VerifiedInstallation{}, err
	}
	directoryHandle, err := os.Open(directory)
	if err != nil {
		return VerifiedInstallation{}, fmt.Errorf("open credential directory: %w", err)
	}
	if err := directoryHandle.Sync(); err != nil {
		directoryHandle.Close()
		return VerifiedInstallation{}, fmt.Errorf("sync credential directory: %w", err)
	}
	if err := directoryHandle.Close(); err != nil {
		return VerifiedInstallation{}, fmt.Errorf("close credential directory: %w", err)
	}
	complete = true
	return verified, nil
}

func writeExclusive(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", filepath.Base(path), err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return fmt.Errorf("secure %s: %w", filepath.Base(path), err)
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return fmt.Errorf("sync %s: %w", filepath.Base(path), err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	complete = true
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Security-Policy", "default-src 'none'; form-action https://github.com; script-src 'unsafe-inline'; style-src 'none'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func openBrowser(target string) error {
	command := exec.Command("xdg-open", target)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func RedactedResult(verified VerifiedInstallation) VerifiedInstallation {
	verified.PrivateKeyPath = filepath.Base(verified.PrivateKeyPath)
	return verified
}

func IsTimeout(err error) bool {
	return isContextError(err)
}

func NormalizeAppName(value string) string {
	return strings.TrimSpace(value)
}
