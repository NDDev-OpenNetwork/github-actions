package garmbootstrap

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

func TestReconcileCreatesDisabledExactResourcesThenEnables(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("w", 32)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		Apply:                true,
	}

	created, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.Actions, []string{"create_github_app_credential", "create_repository", "create_disabled_scale_set"}) {
		t.Fatalf("unexpected create actions: %#v", created.Actions)
	}
	if created.ScaleSet == nil || created.ScaleSet.Enabled || !created.ScaleSet.DisableRunnerUpdate || !created.ScaleSet.ImmutableGuestUpdates || !created.ReadyToEnable || created.ReadyForCanary {
		t.Fatalf("unsafe disabled result: %#v", created)
	}
	state.mutex.Lock()
	request := state.createdScaleSet
	state.mutex.Unlock()
	if request == nil || !request.DisableUpdate || request.Enabled || request.EnableShell || request.GitHubRunnerGroup != "Default" ||
		request.Image != DefaultImage || string(request.ExtraSpecs) != `{"disable_updates":true,"nddev_direct_jit":true}` {
		t.Fatalf("unsafe scale set create request: %#v", request)
	}

	idempotent, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(idempotent.Actions) != 0 || !idempotent.ReadyToEnable {
		t.Fatalf("reconciliation was not idempotent: %#v", idempotent)
	}

	options.Enable = true
	enabled, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(enabled.Actions, []string{"enable_verified_scale_set"}) || !enabled.ReadyForCanary || enabled.ReadyToEnable || enabled.ScaleSet == nil || !enabled.ScaleSet.Enabled {
		t.Fatalf("unexpected enabled result: %#v", enabled)
	}
	again, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Actions) != 0 || !again.ReadyForCanary {
		t.Fatalf("enabled reconciliation was not idempotent: %#v", again)
	}
}

func TestReconcileActivationMigrationIsExplicitReversibleAndDisablesFirst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("m", 32)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath,
		Apply: true, ActivationMode: ActivationModeMetadata,
	}
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.AppBundleDirectory = ""
	options.Enable = true
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}

	options.ActivationMode = ActivationModeDirectJIT
	if _, err := runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "explicit activation migration") {
		t.Fatalf("activation drift was not fail-closed: %v", err)
	}
	options.Apply = false
	options.MigrateActivation = true
	planned, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planned.Actions, []string{"disable_and_migrate_scale_set_activation", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected activation migration plan: %#v", planned.Actions)
	}

	options.Apply = true
	migrated, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migrated.Actions, []string{"disable_and_migrate_scale_set_activation", "enable_verified_scale_set"}) ||
		migrated.ScaleSet == nil || !migrated.ScaleSet.Enabled || !migrated.ScaleSet.DirectJITActivation {
		t.Fatalf("unexpected direct-JIT migration result: %#v", migrated)
	}

	options.ActivationMode = ActivationModeMetadata
	rolledBack, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.ScaleSet == nil || !rolledBack.ScaleSet.Enabled || rolledBack.ScaleSet.DirectJITActivation ||
		!reflect.DeepEqual(rolledBack.Actions, []string{"disable_and_migrate_scale_set_activation", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected metadata rollback result: %#v", rolledBack)
	}
}

func TestReconcileActivationMigrationRejectsUnknownExtraSpecs(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("u", 32)), Now: func() time.Time { return now }}
	if _, err := runner.Run(context.Background(), Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath, Apply: true,
	}); err != nil {
		t.Fatal(err)
	}
	state.mutex.Lock()
	state.scaleSets[DefaultScaleSetName].ExtraSpecs = json.RawMessage(`{"disable_updates":true,"attacker":true}`)
	state.mutex.Unlock()
	_, err := runner.Run(context.Background(), Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, Apply: true, Enable: true,
		ActivationMode: ActivationModeDirectJIT, MigrateActivation: true,
	})
	if err == nil || !strings.Contains(err.Error(), "outside the two supported exact states") {
		t.Fatalf("unknown activation extra specs were not rejected: %v", err)
	}
}

func TestReconcileCapacityMigrationIsExplicitAndDisablesFirst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("c", 32)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath, Apply: true,
	}
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state.mutex.Lock()
	state.scaleSets[DefaultScaleSetName].MaxRunners = 1
	state.scaleSets[DefaultScaleSetName].Enabled = true
	state.mutex.Unlock()
	options.AppBundleDirectory = ""
	options.Enable = true
	if _, err := runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "explicit capacity migration") {
		t.Fatalf("capacity drift was not fail-closed: %v", err)
	}
	options.Apply = false
	options.MigrateCapacity = true
	planned, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planned.Actions, []string{"disable_and_migrate_scale_set_capacity", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected capacity migration plan: %#v", planned.Actions)
	}
	options.Apply = true
	migrated, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.ScaleSet == nil || !migrated.ScaleSet.Enabled || migrated.ScaleSet.MaximumRunners != 12 ||
		!reflect.DeepEqual(migrated.Actions, []string{"disable_and_migrate_scale_set_capacity", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected capacity migration result: %#v", migrated)
	}
}

func TestReconcileImageMigrationIsExplicitAndDisablesFirst(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 17, 16, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("i", 64)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath,
		ScaleSetName: FastScaleSetName, Apply: true,
	}
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state.mutex.Lock()
	state.scaleSets[FastScaleSetName].Image = ReleaseImage
	state.scaleSets[FastScaleSetName].Enabled = true
	state.mutex.Unlock()
	options.AppBundleDirectory = ""
	options.Enable = true
	if _, err := runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "explicit image migration") {
		t.Fatalf("image drift was not fail-closed: %v", err)
	}
	options.Apply = false
	options.MigrateImage = true
	planned, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(planned.Actions, []string{"disable_and_migrate_scale_set_image", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected image migration plan: %#v", planned.Actions)
	}
	options.Apply = true
	migrated, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if migrated.ScaleSet == nil || !migrated.ScaleSet.Enabled || migrated.ScaleSet.Image != ContainerCanaryImage ||
		!reflect.DeepEqual(migrated.Actions, []string{"disable_and_migrate_scale_set_image", "enable_verified_scale_set"}) {
		t.Fatalf("unexpected image migration result: %#v", migrated)
	}
}

func TestReconcileIntegrationScaleSetAlongsideStandard(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("w", 64)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		Apply:                true,
	}

	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	options.AppBundleDirectory = ""
	options.ScaleSetName = IntegrationScaleSetName
	created, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.Actions, []string{"create_disabled_scale_set"}) || created.ScaleSet == nil ||
		created.ScaleSet.Name != IntegrationScaleSetName || created.ScaleSet.Image != IntegrationImage ||
		created.ScaleSet.Flavor != IntegrationFlavor || created.ScaleSet.Enabled || !created.ReadyToEnable {
		t.Fatalf("unexpected integration create result: %#v", created)
	}
	state.mutex.Lock()
	request := state.createdScaleSet
	standard := state.scaleSets[DefaultScaleSetName]
	integration := state.scaleSets[IntegrationScaleSetName]
	state.mutex.Unlock()
	if request == nil || request.Name != IntegrationScaleSetName || request.Image != IntegrationImage ||
		request.Flavor != IntegrationFlavor || !request.DisableUpdate || request.Enabled || request.EnableShell ||
		standard == nil || integration == nil || standard.Image != DefaultImage {
		t.Fatalf("unsafe integration scale set state: request=%#v standard=%#v integration=%#v", request, standard, integration)
	}

	options.Enable = true
	enabled, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(enabled.Actions, []string{"enable_verified_scale_set"}) || enabled.ScaleSet == nil ||
		!enabled.ScaleSet.Enabled || enabled.ScaleSet.Name != IntegrationScaleSetName || !enabled.ReadyForCanary {
		t.Fatalf("unexpected integration enable result: %#v", enabled)
	}
}

func TestReconcileMissingCredentialRequiresOneTimeBundle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, _, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	_, err := (Runner{HTTPClient: server.Client(), Now: func() time.Time { return now }}).Run(
		context.Background(),
		Options{
			BaseURL:              server.URL + "/api/v1",
			AdminCredentialsPath: adminPath,
			CredentialAnchorPath: anchorPath,
			Apply:                true,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "one-time GitHub App bundle") {
		t.Fatalf("missing credential was accepted without its creation bundle: %v", err)
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.credential != nil || state.repository != nil || len(state.scaleSets) != 0 {
		t.Fatal("failed anchor-only credential reconciliation mutated GARM")
	}
}

func TestReconcileRejectsBundleAnchorMismatch(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	anchor, err := loadCredentialAnchorForTest(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	anchor.InstallationID++
	writePrivateJSON(t, anchorPath, anchor)
	_, err = (Runner{Now: func() time.Time { return now }}).Run(context.Background(), Options{
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
	})
	if err == nil || !strings.Contains(err.Error(), "does not match the reviewed") {
		t.Fatalf("mismatched bundle and anchor were accepted: %v", err)
	}
}

func TestCredentialAnchorValidationFailsClosed(t *testing.T) {
	t.Parallel()
	valid := credentialAnchor{
		SchemaVersion:  1,
		CredentialName: testTenant().CredentialName,
		AppID:          1,
		InstallationID: 2,
		KeySHA256:      strings.Repeat("a", 64),
	}
	tests := []struct {
		name   string
		mutate func(*credentialAnchor)
	}{
		{"schema", func(anchor *credentialAnchor) { anchor.SchemaVersion = 2 }},
		{"credential name", func(anchor *credentialAnchor) { anchor.CredentialName = "other" }},
		{"app identity", func(anchor *credentialAnchor) { anchor.AppID = 0 }},
		{"installation identity", func(anchor *credentialAnchor) { anchor.InstallationID = 0 }},
		{"short fingerprint", func(anchor *credentialAnchor) { anchor.KeySHA256 = "aa" }},
		{"uppercase fingerprint", func(anchor *credentialAnchor) { anchor.KeySHA256 = strings.Repeat("A", 64) }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.validate(testTenant()); err == nil {
				t.Fatalf("invalid credential anchor was accepted: %#v", candidate)
			}
		})
	}

	unknownPath := filepath.Join(t.TempDir(), "unknown-anchor.json")
	if err := os.WriteFile(unknownPath, []byte(`{"schema_version":1,"credential_name":"example-actions-fleet","app_id":1,"installation_id":2,"key_sha256":"`+strings.Repeat("a", 64)+`","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentialAnchorForTest(unknownPath); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown credential anchor field was accepted: %v", err)
	}
}

func TestReconcileRejectsUnknownScaleSetBeforeReadingSecrets(t *testing.T) {
	t.Parallel()
	result, err := (Runner{}).Run(context.Background(), Options{ScaleSetName: "nddev-linux-typo"})
	if err == nil || !strings.Contains(err.Error(), "scale set must be exactly") || result.Applied {
		t.Fatalf("unknown managed scale set was accepted: result=%#v error=%v", result, err)
	}
}

func TestReconcileCreatesReviewedRepositoryScopedPriorityScaleSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 19, 15, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("a", 64)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath, Apply: true,
	}
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state.mutex.Lock()
	state.repository = nil
	state.mutex.Unlock()
	options.AppBundleDirectory = ""
	options.Repository = "example-org/example-library"
	options.ScaleSetName = PriorityStandardScaleSetName
	created, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if created.Repository == nil || created.Repository.Name != options.Repository || created.ScaleSet == nil ||
		created.ScaleSet.Name != PriorityStandardScaleSetName || created.ScaleSet.Image != PriorityStandardImage ||
		created.ScaleSet.Flavor != PriorityStandardFlavor {
		t.Fatalf("unexpected Priority repository reconciliation: %#v", created)
	}
}

func TestReconcileRejectsUnreviewedRepositoryBeforeReadingSecrets(t *testing.T) {
	result, err := (Runner{}).Run(context.Background(), Options{Repository: "example-org/public-repository"})
	if err == nil || !strings.Contains(err.Error(), "is not a managed repository") || result.Applied {
		t.Fatalf("unreviewed repository was accepted: result=%#v error=%v", result, err)
	}
	if _, err := (Runner{}).Run(context.Background(), Options{
		Repository: "example-org/example-library", EntityKind: EntityKindOrganization,
	}); err == nil || !strings.Contains(err.Error(), "cannot be used with an organization") {
		t.Fatalf("repository override was accepted for an organization entity: %v", err)
	}
}

func TestReconcileDryRunAndFailClosedDrift(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Now: func() time.Time { return now }}
	options := Options{BaseURL: server.URL + "/api/v1", AdminCredentialsPath: adminPath, CredentialAnchorPath: anchorPath, AppBundleDirectory: bundlePath}

	result, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if result.Applied || !reflect.DeepEqual(result.Actions, []string{"create_github_app_credential", "create_repository", "create_disabled_scale_set"}) {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	state.mutex.Lock()
	if state.credential != nil || state.repository != nil || len(state.scaleSets) != 0 {
		t.Fatal("dry-run mutated fake GARM")
	}
	state.mutex.Unlock()

	options.Apply = true
	if _, err := runner.Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	state.mutex.Lock()
	state.repository.PoolBalancerType = "pack"
	state.mutex.Unlock()
	if _, err := runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "pool boundary") {
		t.Fatalf("repository balancer drift was accepted: %v", err)
	}
	state.mutex.Lock()
	state.repository.PoolBalancerType = DefaultPoolBalancerType
	state.scaleSets[DefaultScaleSetName].DisableUpdate = false
	state.mutex.Unlock()
	if _, err := runner.Run(context.Background(), options); err == nil || !strings.Contains(err.Error(), "auto-update") {
		t.Fatalf("runner update drift was accepted: %v", err)
	}
}

func TestEnableNeverBootstrapsMissingResources(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Now: func() time.Time { return now }}
	_, err := runner.Run(context.Background(), Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		Apply:                true,
		Enable:               true,
	})
	if err == nil || !strings.Contains(err.Error(), "does not already exist") {
		t.Fatalf("unsafe direct enable was accepted: %v", err)
	}
	state.mutex.Lock()
	defer state.mutex.Unlock()
	if state.credential != nil || state.repository != nil || len(state.scaleSets) != 0 {
		t.Fatal("failed enable mutated GARM")
	}
}

func TestSecureInputsAndLoopbackOnlyAPI(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	if err := os.Chmod(adminPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAdminCredentials(adminPath); err == nil || !strings.Contains(err.Error(), "private regular file") {
		t.Fatalf("world-readable admin file accepted: %v", err)
	}
	if _, err := newAPIClient("https://127.0.0.1:9997/api/v1", nil); err == nil {
		t.Fatal("non-local transport shape was accepted")
	}
	if _, err := loadAppBundleForTest(bundlePath, now.Add(25*time.Hour)); err == nil || !strings.Contains(err.Error(), "import window") {
		t.Fatalf("stale one-time bundle accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAppBundleForTest(bundlePath, now); err == nil || !strings.Contains(err.Error(), "exactly") {
		t.Fatalf("bundle with extra material accepted: %v", err)
	}
	if err := os.Chmod(anchorPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCredentialAnchorForTest(anchorPath); err == nil || !strings.Contains(err.Error(), "non-writable regular file") {
		t.Fatalf("writable credential anchor accepted: %v", err)
	}
}

func TestAPIClientRefusesRedirectsWithCredentials(t *testing.T) {
	t.Parallel()
	var redirected atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		redirected.Store(true)
		if request.Header.Get("Authorization") != "" {
			t.Error("authorization escaped the loopback API origin")
		}
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusFound)
	}))
	defer origin.Close()
	client, err := newAPIClient(origin.URL+"/api/v1", origin.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.doJSON(context.Background(), http.MethodGet, "/github/credentials", "secret-token", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect response was accepted: %v", err)
	}
	if redirected.Load() {
		t.Fatal("GARM API redirect was followed")
	}
}

type fakeGARMState struct {
	mutex           sync.Mutex
	credential      *credentialDTO
	repository      *repositoryDTO
	organization    *organizationDTO
	scaleSets       map[string]*scaleSetDTO
	createdScaleSet *createScaleSetRequest
}

func (s *fakeGARMState) handler(t *testing.T) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/v1/auth/login" {
			var login map[string]string
			readJSON(t, request.Body, &login)
			if login["username"] != "admin" || login["password"] != "correct-horse" {
				t.Errorf("unexpected login")
			}
			writeJSONTest(t, writer, map[string]string{"token": "test-token"})
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.mutex.Lock()
		defer s.mutex.Unlock()
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/github/credentials":
			if s.credential == nil {
				writeJSONTest(t, writer, []credentialDTO{})
			} else {
				writeJSONTest(t, writer, []credentialDTO{*s.credential})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/github/credentials":
			var input createCredentialRequest
			readJSON(t, request.Body, &input)
			if input.AuthType != "app" || input.App.AppID != 12345 || input.App.InstallationID != 67890 || !strings.Contains(string(input.App.PrivateKeyBytes), "BEGIN RSA PRIVATE KEY") {
				t.Errorf("unexpected credential request: %#v", input)
			}
			s.credential = &credentialDTO{ID: 1, Name: input.Name, Description: input.Description, AuthType: "app", Endpoint: endpointDTO{Name: "github.com", EndpointType: "github"}}
			writeJSONTest(t, writer, s.credential)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/organizations":
			if s.organization == nil {
				writeJSONTest(t, writer, []organizationDTO{})
			} else {
				writeJSONTest(t, writer, []organizationDTO{*s.organization})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/organizations":
			var input createOrganizationRequest
			readJSON(t, request.Body, &input)
			if input.Name != testTenant().Owner || len(input.WebhookSecret) != 64 || input.AgentMode || input.ForgeType != "github" || input.PoolBalancerType != DefaultPoolBalancerType {
				t.Errorf("unexpected organization request: %#v", input)
			}
			s.organization = &organizationDTO{ID: "org-1", Name: input.Name, CredentialsID: 1, Credentials: *s.credential, PoolBalancerType: input.PoolBalancerType, Endpoint: endpointDTO{Name: "github.com", EndpointType: "github"}}
			writeJSONTest(t, writer, s.organization)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/organizations/org-1/scalesets":
			scaleSets := make([]scaleSetDTO, 0, len(s.scaleSets))
			for _, scaleSet := range s.scaleSets {
				scaleSets = append(scaleSets, *scaleSet)
			}
			writeJSONTest(t, writer, scaleSets)
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/organizations/org-1/scalesets":
			var input createScaleSetRequest
			readJSON(t, request.Body, &input)
			s.createdScaleSet = &input
			spec, err := resolveScaleSetSpec(input.Name)
			if err != nil {
				t.Error(err)
				http.Error(writer, "invalid scale set", http.StatusBadRequest)
				return
			}
			spec.DirectJIT = activationSpecsMatch(input.ExtraSpecs, true)
			if s.scaleSets == nil {
				s.scaleSets = make(map[string]*scaleSetDTO)
			}
			s.scaleSets[input.Name] = desiredScaleSetDTO(garmEntity{Kind: EntityKindOrganization, ID: "org-1"}, spec)
			writeJSONTest(t, writer, s.scaleSets[input.Name])
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repositories":
			if s.repository == nil {
				writeJSONTest(t, writer, []repositoryDTO{})
			} else {
				writeJSONTest(t, writer, []repositoryDTO{*s.repository})
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/repositories":
			var input createRepositoryRequest
			readJSON(t, request.Body, &input)
			_, repositoryErr := tenant.WithRepository(testTenant(), input.Owner+"/"+input.Name)
			if repositoryErr != nil || len(input.WebhookSecret) != 64 || input.AgentMode || input.ForgeType != "github" || input.PoolBalancerType != DefaultPoolBalancerType {
				t.Errorf("unexpected repository request: %#v", input)
			}
			s.repository = &repositoryDTO{ID: "repo-1", Owner: input.Owner, Name: input.Name, CredentialsID: 1, Credentials: *s.credential, PoolBalancerType: input.PoolBalancerType, Endpoint: endpointDTO{Name: "github.com", EndpointType: "github"}}
			writeJSONTest(t, writer, s.repository)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repositories/repo-1/scalesets":
			if len(s.scaleSets) == 0 {
				writeJSONTest(t, writer, []scaleSetDTO{})
			} else {
				scaleSets := make([]scaleSetDTO, 0, len(s.scaleSets))
				for _, scaleSet := range s.scaleSets {
					scaleSets = append(scaleSets, *scaleSet)
				}
				writeJSONTest(t, writer, scaleSets)
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/repositories/repo-1/scalesets":
			var input createScaleSetRequest
			readJSON(t, request.Body, &input)
			s.createdScaleSet = &input
			spec, err := resolveScaleSetSpec(input.Name)
			if err != nil {
				t.Error(err)
				http.Error(writer, "invalid scale set", http.StatusBadRequest)
				return
			}
			spec.DirectJIT = activationSpecsMatch(input.ExtraSpecs, true)
			if s.scaleSets == nil {
				s.scaleSets = make(map[string]*scaleSetDTO)
			}
			s.scaleSets[input.Name] = desiredScaleSetDTO(garmEntity{Kind: EntityKindRepository, ID: "repo-1"}, spec)
			writeJSONTest(t, writer, s.scaleSets[input.Name])
		case request.Method == http.MethodPut && strings.HasPrefix(request.URL.Path, "/api/v1/scalesets/"):
			var input struct {
				Enabled    *bool           `json:"enabled"`
				ExtraSpecs json.RawMessage `json:"extra_specs"`
				MaxRunners *uint           `json:"max_runners"`
				Image      string          `json:"image"`
				Flavor     string          `json:"flavor"`
			}
			readJSON(t, request.Body, &input)
			var selected *scaleSetDTO
			for _, scaleSet := range s.scaleSets {
				if request.URL.Path == fmt.Sprintf("/api/v1/scalesets/%d", scaleSet.ID) {
					selected = scaleSet
					break
				}
			}
			if selected == nil {
				http.Error(writer, "unknown scale set", http.StatusNotFound)
				return
			}
			if input.Enabled != nil {
				selected.Enabled = *input.Enabled
			}
			if len(input.ExtraSpecs) != 0 {
				selected.ExtraSpecs = input.ExtraSpecs
			}
			if input.MaxRunners != nil {
				selected.MaxRunners = *input.MaxRunners
			}
			if input.Image != "" {
				selected.Image = input.Image
			}
			if input.Flavor != "" {
				selected.Flavor = input.Flavor
			}
			writeJSONTest(t, writer, selected)
		default:
			http.Error(writer, fmt.Sprintf("unexpected %s %s", request.Method, request.URL.Path), http.StatusNotFound)
		}
	})
}

func desiredScaleSetDTO(entity garmEntity, spec scaleSetSpec) *scaleSetDTO {
	request := desiredScaleSetRequest(spec)
	id := uint(7)
	scaleSetID := 91
	if spec.Name == IntegrationScaleSetName {
		id = 8
		scaleSetID = 92
	}
	return &scaleSetDTO{
		ID:                     id,
		ScaleSetID:             scaleSetID,
		Name:                   request.Name,
		DisableUpdate:          request.DisableUpdate,
		ProviderName:           request.ProviderName,
		MaxRunners:             request.MaxRunners,
		MinIdleRunners:         request.MinIdleRunners,
		Image:                  request.Image,
		Flavor:                 request.Flavor,
		OSType:                 request.OSType,
		OSArch:                 request.OSArch,
		Enabled:                request.Enabled,
		EnableShell:            request.EnableShell,
		RunnerPrefix:           request.RunnerPrefix,
		RunnerBootstrapTimeout: request.RunnerBootstrapTimeout,
		ExtraSpecs:             request.ExtraSpecs,
		GitHubRunnerGroup:      request.GitHubRunnerGroup,
		RepoID:                 ownerField(entity, EntityKindRepository),
		OrgID:                  ownerField(entity, EntityKindOrganization),
	}
}

func testFiles(t *testing.T, now time.Time) (string, string, string) {
	t.Helper()
	root := t.TempDir()
	adminPath := filepath.Join(root, "admin.json")
	writePrivateJSON(t, adminPath, adminCredentials{Email: "admin@example.invalid", FullName: "Admin", Username: "admin", Password: "correct-horse"})
	bundlePath := filepath.Join(root, "bundle")
	if err := os.Mkdir(bundlePath, 0o700); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyData := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	keyPath := filepath.Join(bundlePath, "github-app-private-key.pem")
	if err := os.WriteFile(keyPath, keyData, 0o600); err != nil {
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
		RepositorySelection: "selected",
		Permissions:         map[string]string{"administration": "write", ActionsReadPermission: "read", "metadata": "read", OrganizationRunnersPermission: "write"},
		PrivateKeyPath:      "/old/staging/github-app-private-key.pem",
		VerifiedAt:          now,
	})
	bundle, err := loadAppBundleForTest(bundlePath, now)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(bundle.PrivateKey)
	anchorPath := filepath.Join(root, "credential-anchor.json")
	writePrivateJSON(t, anchorPath, bundle.Anchor)
	return adminPath, bundlePath, anchorPath
}

func writePrivateJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, reader io.Reader, output any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(output); err != nil {
		t.Error(err)
	}
}

func writeJSONTest(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

// ownerField mirrors GARM, which populates exactly one owner field per scale
// set according to the entity it hangs from and leaves the others empty.
func ownerField(entity garmEntity, kind string) string {
	if entity.Kind == kind {
		return entity.ID
	}
	return ""
}

// The organization entity is what lets one fleet serve every repository the
// account holds instead of only the one it lives in. It is the same
// reconciliation with a different forge entity, so the disabled-first,
// fail-closed and idempotent properties must survive the switch unchanged.
func TestReconcileCreatesTheOrganizationEntityAndItsScaleSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("w", 32)), Now: func() time.Time { return now }}
	options := Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		EntityKind:           EntityKindOrganization,
		Apply:                true,
	}

	created, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(created.Actions, []string{"create_github_app_credential", "create_organization", "create_disabled_scale_set"}) {
		t.Fatalf("unexpected create actions: %#v", created.Actions)
	}
	if created.Organization == nil || created.Organization.Name != testTenant().Owner || created.Organization.ID != "org-1" {
		t.Fatalf("organization summary is %#v", created.Organization)
	}
	if created.Repository != nil {
		t.Fatalf("an organization run reported a repository entity: %#v", created.Repository)
	}
	if created.ScaleSet == nil || created.ScaleSet.Enabled || !created.ReadyToEnable {
		t.Fatalf("scale set was not created disabled: %#v", created.ScaleSet)
	}

	idempotent, err := runner.Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if len(idempotent.Actions) != 0 {
		t.Fatalf("second run was not idempotent: %#v", idempotent.Actions)
	}
}

// GARM leaves the owner fields it does not use empty, so comparing the wrong
// one succeeds against an empty string and would accept a scale set belonging
// to another entity. This is the check that would have shipped broken.
func TestValidateScaleSetChecksTheOwnerFieldOfItsEntityKind(t *testing.T) {
	t.Parallel()
	spec := scaleSetSpec{Name: DefaultScaleSetName, Image: DefaultImage, Flavor: DefaultFlavor}
	sound := desiredScaleSetDTO(garmEntity{Kind: EntityKindOrganization, ID: "org-1"}, spec)
	if err := validateScaleSet(*sound, garmEntity{Kind: EntityKindOrganization, ID: "org-1"}, spec); err != nil {
		t.Fatalf("an organization scale set was rejected: %v", err)
	}
	for name, entity := range map[string]garmEntity{
		"repository entity against an organization scale set": {Kind: EntityKindRepository, ID: "repo-1"},
		"another organization":                                {Kind: EntityKindOrganization, ID: "org-2"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateScaleSet(*sound, entity, spec); err == nil {
				t.Fatal("scale set ownership was accepted")
			}
		})
	}
}

func TestReconcileRejectsAnUnknownEntityKind(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	adminPath, bundlePath, anchorPath := testFiles(t, now)
	state := &fakeGARMState{}
	server := httptest.NewServer(state.handler(t))
	defer server.Close()
	runner := Runner{HTTPClient: server.Client(), Random: strings.NewReader(strings.Repeat("w", 32)), Now: func() time.Time { return now }}

	_, err := runner.Run(context.Background(), Options{
		BaseURL:              server.URL + "/api/v1",
		AdminCredentialsPath: adminPath,
		CredentialAnchorPath: anchorPath,
		AppBundleDirectory:   bundlePath,
		EntityKind:           "enterprise",
		Apply:                true,
	})
	if err == nil || !strings.Contains(err.Error(), "entity kind") {
		t.Fatalf("an unsupported entity kind was accepted: %v", err)
	}
}

// Both halves of the entity must come from the same tenant field. The name was
// left at the literal that multi-tenancy inherited from the first tenant, so
// every other tenant got an entity for a repository it does not manage — and
// the default tenant's own repository being named `github-actions` is exactly
// why no existing test noticed.
func TestReconcileRepositoryTakesBothHalvesFromTheTenant(t *testing.T) {
	t.Parallel()

	selected, err := tenant.ByID("example-guild")
	if err != nil {
		t.Fatal(err)
	}
	var created createRepositoryRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v1/repositories":
			writeJSONTest(t, writer, []repositoryDTO{})
		case request.Method == http.MethodPost && request.URL.Path == "/api/v1/repositories":
			readJSON(t, request.Body, &created)
			writeJSONTest(t, writer, repositoryDTO{
				ID:               "repo-guild",
				Owner:            created.Owner,
				Name:             created.Name,
				CredentialsID:    1,
				CredentialsName:  selected.CredentialName,
				PoolBalancerType: DefaultPoolBalancerType,
				Endpoint:         endpointDTO{Name: "github.com", EndpointType: "github"},
			})
		default:
			http.Error(writer, "unexpected", http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := newAPIClient(server.URL+"/api/v1", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	credential := credentialDTO{ID: 1, Name: selected.CredentialName}
	result := &Result{}
	repository, err := reconcileRepository(
		context.Background(), client, "token", credential,
		strings.NewReader(strings.Repeat("w", 32)), selected, true, false, result,
	)
	if err != nil {
		t.Fatalf("reconcile refused its own creation: %v", err)
	}
	if created.Owner+"/"+created.Name != selected.Repository {
		t.Fatalf("created %q, want %q", created.Owner+"/"+created.Name, selected.Repository)
	}
	if repository == nil || repository.Owner+"/"+repository.Name != selected.Repository {
		t.Fatalf("verified entity does not match the tenant: %#v", repository)
	}
}
