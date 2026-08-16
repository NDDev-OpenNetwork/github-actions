package garmbootstrap

import (
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Runner struct {
	HTTPClient *http.Client
	Random     io.Reader
	Now        func() time.Time
}

type scaleSetSpec struct {
	Name      string
	Image     string
	Flavor    string
	DirectJIT bool
}

func (r Runner) Run(ctx context.Context, options Options) (Result, error) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	result := Result{SchemaVersion: 1, Applied: options.Apply, Actions: []string{}, ObservedAt: now().UTC()}
	selected, err := tenant.ByID(options.Tenant)
	if err != nil {
		return result, err
	}
	desiredSpec, err := resolveScaleSetSpec(options.ScaleSetName)
	if err != nil {
		return result, err
	}
	activationMode := options.ActivationMode
	if activationMode == "" {
		activationMode = ActivationModeDirectJIT
	}
	switch activationMode {
	case ActivationModeDirectJIT:
		desiredSpec.DirectJIT = true
	case ActivationModeMetadata:
		desiredSpec.DirectJIT = false
	default:
		return result, fmt.Errorf("activation mode must be exactly %q or %q", ActivationModeDirectJIT, ActivationModeMetadata)
	}
	if options.AdminCredentialsPath == "" || options.CredentialAnchorPath == "" {
		return result, fmt.Errorf("admin credentials and GARM credential anchor paths are required")
	}
	anchor, err := loadCredentialAnchor(options.CredentialAnchorPath, selected)
	if err != nil {
		return result, err
	}
	credentials, err := loadAdminCredentials(options.AdminCredentialsPath)
	if err != nil {
		return result, err
	}
	var bundle *appBundle
	if options.AppBundleDirectory != "" {
		loadedBundle, err := loadAppBundle(options.AppBundleDirectory, selected, now())
		if err != nil {
			return result, err
		}
		if !anchor.matchesBundle(loadedBundle) {
			clear(loadedBundle.PrivateKey)
			return result, fmt.Errorf("GitHub App bundle does not match the reviewed GARM credential anchor")
		}
		bundle = &loadedBundle
		defer clear(bundle.PrivateKey)
	}
	// An organization entity cannot hang from an App installed on a user
	// account. GARM would accept the pairing and fail later, when a scale set
	// has already been created against an entity that can never serve.
	if bundle != nil && options.EntityKind == EntityKindOrganization {
		if bundle.Installation.OwnerType != OwnerTypeOrganization {
			return result, fmt.Errorf("organization entity requires an organization-owned App, got %q", bundle.Installation.OwnerType)
		}
		// Without this the entity is created and the first scale set fails on a
		// GitHub 403, leaving GARM holding an organization it can never serve.
		if bundle.Installation.Permissions[OrganizationRunnersPermission] != "write" {
			return result, fmt.Errorf("organization entity requires the %s permission at write", OrganizationRunnersPermission)
		}
	}
	client, err := newAPIClient(options.BaseURL, r.HTTPClient)
	if err != nil {
		return result, err
	}
	token, err := client.login(ctx, credentials)
	credentials.Password = ""
	if err != nil {
		return result, err
	}

	credential, err := reconcileCredential(ctx, client, token, anchor, bundle, selected, options.Apply, options.Enable, &result)
	if err != nil {
		return result, err
	}
	if credential == nil {
		// Nothing exists yet, so the entity reconcilers below are never reached
		// and this is the only place the plan gets written. It has to name the
		// entity that was actually requested: a plan is what an operator
		// approves, and approving "create_repository" for a run that will
		// create an organization is approving something else.
		entityAction := "create_repository"
		if options.EntityKind == EntityKindOrganization {
			entityAction = "create_organization"
		}
		result.Actions = append(result.Actions, entityAction, "create_disabled_scale_set")
		return result, nil
	}
	result.Credential = &ResourceSummary{ID: strconv.FormatUint(uint64(credential.ID), 10), Name: credential.Name}

	entity, err := reconcileEntity(ctx, client, token, *credential, r.Random, selected, options, &result)
	if err != nil {
		return result, err
	}
	if entity == nil {
		result.Actions = append(result.Actions, "create_disabled_scale_set")
		return result, nil
	}
	summary := &ResourceSummary{ID: entity.ID, Name: entity.Name}
	if entity.Kind == EntityKindOrganization {
		result.Organization = summary
	} else {
		result.Repository = summary
	}

	scaleSet, err := reconcileScaleSet(ctx, client, token, *entity, desiredSpec, options, &result)
	if err != nil {
		return result, err
	}
	if scaleSet == nil {
		return result, nil
	}
	result.ScaleSet = summarizeScaleSet(*scaleSet)
	result.ReadyToEnable = !scaleSet.Enabled
	result.ReadyForCanary = scaleSet.Enabled
	return result, nil
}

func reconcileCredential(ctx context.Context, client apiClient, token string, anchor credentialAnchor, bundle *appBundle, selected tenant.Tenant, apply, enable bool, result *Result) (*credentialDTO, error) {
	var credentials []credentialDTO
	if err := client.doJSON(ctx, http.MethodGet, "/github/credentials", token, nil, &credentials); err != nil {
		return nil, fmt.Errorf("list GARM GitHub credentials: %w", err)
	}
	matches := matchingCredentials(credentials, selected.CredentialName)
	if len(matches) > 1 {
		return nil, fmt.Errorf("GARM contains duplicate credential name %q", selected.CredentialName)
	}
	if len(matches) == 1 {
		if err := validateCredential(matches[0], selected, anchor.description()); err != nil {
			return nil, err
		}
		return &matches[0], nil
	}
	if enable {
		return nil, fmt.Errorf("refusing enable because the verified GARM credential does not already exist")
	}
	if bundle == nil {
		return nil, fmt.Errorf("GARM credential is missing; a one-time GitHub App bundle is required to create it")
	}
	result.Actions = append(result.Actions, "create_github_app_credential")
	if !apply {
		return nil, nil
	}
	request := createCredentialRequest{
		Name:        selected.CredentialName,
		Description: anchor.description(),
		Endpoint:    "github.com",
		AuthType:    "app",
	}
	request.App.AppID = bundle.Installation.AppID
	request.App.InstallationID = bundle.Installation.InstallationID
	request.App.PrivateKeyBytes = bundle.PrivateKey
	var created credentialDTO
	if err := client.doJSON(ctx, http.MethodPost, "/github/credentials", token, request, &created); err != nil {
		return nil, fmt.Errorf("create GARM GitHub App credential: %w", err)
	}
	if err := validateCredential(created, selected, anchor.description()); err != nil {
		return nil, fmt.Errorf("created GARM credential failed verification: %w", err)
	}
	return &created, nil
}

// garmEntity is the forge entity scale sets hang from. GARM addresses
// repositories and organizations through parallel endpoints, and everything
// downstream of this point differs only in that path and in what identity was
// verified to get here.
type garmEntity struct {
	Kind string
	ID   string
	Name string
}

func (e garmEntity) scaleSetPath() string {
	if e.Kind == EntityKindOrganization {
		return "/organizations/" + url.PathEscape(e.ID) + "/scalesets"
	}
	return "/repositories/" + url.PathEscape(e.ID) + "/scalesets"
}

func reconcileEntity(ctx context.Context, client apiClient, token string, credential credentialDTO, random io.Reader, selected tenant.Tenant, options Options, result *Result) (*garmEntity, error) {
	switch options.EntityKind {
	case EntityKindOrganization:
		organization, err := reconcileOrganization(ctx, client, token, credential, random, selected, options.Apply, options.Enable, result)
		if err != nil || organization == nil {
			return nil, err
		}
		return &garmEntity{Kind: EntityKindOrganization, ID: organization.ID, Name: organization.Name}, nil
	case EntityKindRepository, "":
		repository, err := reconcileRepository(ctx, client, token, credential, random, selected, options.Apply, options.Enable, result)
		if err != nil || repository == nil {
			return nil, err
		}
		return &garmEntity{Kind: EntityKindRepository, ID: repository.ID, Name: repository.Owner + "/" + repository.Name}, nil
	default:
		return nil, fmt.Errorf("entity kind is %q, want %q or %q", options.EntityKind, EntityKindRepository, EntityKindOrganization)
	}
}

// reconcileOrganization creates the entity that serves every repository the
// organization holds. That is deliberately wider than a repository entity, and
// the widening is the point: one fleet replaces listeners that were shared
// across the account. Restricting which repositories may reach the resulting
// pools is a GitHub runner-group setting, not something GARM expresses here.
func reconcileOrganization(ctx context.Context, client apiClient, token string, credential credentialDTO, random io.Reader, selected tenant.Tenant, apply, enable bool, result *Result) (*organizationDTO, error) {
	var organizations []organizationDTO
	if err := client.doJSON(ctx, http.MethodGet, "/organizations", token, nil, &organizations); err != nil {
		return nil, fmt.Errorf("list GARM organizations: %w", err)
	}
	matches := matchingOrganizations(organizations, selected.Owner)
	if len(matches) > 1 {
		return nil, fmt.Errorf("GARM contains duplicate organization entity %q", selected.Owner)
	}
	if len(matches) == 1 {
		if err := validateOrganization(matches[0], selected, credential); err != nil {
			return nil, err
		}
		return &matches[0], nil
	}
	if enable {
		return nil, fmt.Errorf("refusing enable because the verified GARM organization does not already exist")
	}
	result.Actions = append(result.Actions, "create_organization")
	if !apply {
		return nil, nil
	}
	webhookSecret, err := randomSecret(random)
	if err != nil {
		return nil, err
	}
	request := createOrganizationRequest{
		Name:             selected.Owner,
		CredentialsName:  selected.CredentialName,
		WebhookSecret:    webhookSecret,
		PoolBalancerType: DefaultPoolBalancerType,
		ForgeType:        "github",
		AgentMode:        false,
	}
	var created organizationDTO
	err = client.doJSON(ctx, http.MethodPost, "/organizations", token, request, &created)
	request.WebhookSecret = ""
	if err != nil {
		return nil, fmt.Errorf("create GARM organization: %w", err)
	}
	if err := validateOrganization(created, selected, credential); err != nil {
		return nil, fmt.Errorf("created GARM organization failed verification: %w", err)
	}
	return &created, nil
}

func reconcileRepository(ctx context.Context, client apiClient, token string, credential credentialDTO, random io.Reader, selected tenant.Tenant, apply, enable bool, result *Result) (*repositoryDTO, error) {
	var repositories []repositoryDTO
	if err := client.doJSON(ctx, http.MethodGet, "/repositories", token, nil, &repositories); err != nil {
		return nil, fmt.Errorf("list GARM repositories: %w", err)
	}
	matches := matchingRepositories(repositories, selected.Repository)
	if len(matches) > 1 {
		return nil, fmt.Errorf("GARM contains duplicate repository entity %q", selected.Repository)
	}
	if len(matches) == 1 {
		if err := validateRepository(matches[0], selected, credential); err != nil {
			return nil, err
		}
		return &matches[0], nil
	}
	if enable {
		return nil, fmt.Errorf("refusing enable because the verified GARM repository does not already exist")
	}
	result.Actions = append(result.Actions, "create_repository")
	if !apply {
		return nil, nil
	}
	webhookSecret, err := randomSecret(random)
	if err != nil {
		return nil, err
	}
	// Both halves come from the same tenant field. Taking the owner from the
	// tenant and leaving the name at the literal it had before multi-tenancy
	// created `<tenant owner>/github-actions` for every tenant but the first —
	// an entity for a repository the tenant does not manage, which then failed
	// the verification directly below because it is not `selected.Repository`.
	owner, name, found := strings.Cut(selected.Repository, "/")
	if !found || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, fmt.Errorf("tenant repository %q is not owner/name", selected.Repository)
	}
	request := createRepositoryRequest{
		Owner:            owner,
		Name:             name,
		CredentialsName:  selected.CredentialName,
		WebhookSecret:    webhookSecret,
		PoolBalancerType: DefaultPoolBalancerType,
		ForgeType:        "github",
		AgentMode:        false,
	}
	var created repositoryDTO
	err = client.doJSON(ctx, http.MethodPost, "/repositories", token, request, &created)
	request.WebhookSecret = ""
	if err != nil {
		return nil, fmt.Errorf("create GARM repository: %w", err)
	}
	if err := validateRepository(created, selected, credential); err != nil {
		return nil, fmt.Errorf("created GARM repository failed verification: %w", err)
	}
	return &created, nil
}

func reconcileScaleSet(ctx context.Context, client apiClient, token string, entity garmEntity, spec scaleSetSpec, options Options, result *Result) (*scaleSetDTO, error) {
	endpoint := entity.scaleSetPath()
	var scaleSets []scaleSetDTO
	if err := client.doJSON(ctx, http.MethodGet, endpoint, token, nil, &scaleSets); err != nil {
		return nil, fmt.Errorf("list GARM %s scale sets: %w", entity.Kind, err)
	}
	matches := matchingScaleSets(scaleSets, spec.Name)
	if len(matches) > 1 {
		return nil, fmt.Errorf("GARM contains duplicate scale set name %q", spec.Name)
	}
	if len(matches) == 0 {
		if options.Enable {
			return nil, fmt.Errorf("refusing to create an enabled scale set; create and inspect the disabled pilot first")
		}
		result.Actions = append(result.Actions, "create_disabled_scale_set")
		if !options.Apply {
			return nil, nil
		}
		request := desiredScaleSetRequest(spec)
		var created scaleSetDTO
		if err := client.doJSON(ctx, http.MethodPost, endpoint, token, request, &created); err != nil {
			return nil, fmt.Errorf("create disabled GARM scale set: %w", err)
		}
		if err := validateScaleSet(created, entity, spec); err != nil {
			return nil, fmt.Errorf("created GARM scale set failed verification: %w", err)
		}
		if created.Enabled {
			return nil, fmt.Errorf("created GARM scale set unexpectedly started enabled")
		}
		return &created, nil
	}
	scaleSet := matches[0]
	if !activationSpecsMatch(scaleSet.ExtraSpecs, spec.DirectJIT) {
		if !knownActivationSpecs(scaleSet.ExtraSpecs) {
			return nil, fmt.Errorf("GARM scale set activation extra specs drifted outside the two supported exact states")
		}
		candidate := scaleSet
		candidate.ExtraSpecs = desiredExtraSpecs(spec.DirectJIT)
		if err := validateScaleSet(candidate, entity, spec); err != nil {
			return nil, err
		}
		if !options.MigrateActivation {
			return nil, fmt.Errorf("GARM scale set activation mode differs; explicit activation migration is required")
		}
		result.Actions = append(result.Actions, "disable_and_migrate_scale_set_activation")
		candidate.Enabled = false
		if options.Apply {
			disabled := false
			request := struct {
				Enabled    *bool           `json:"enabled"`
				ExtraSpecs json.RawMessage `json:"extra_specs"`
			}{Enabled: &disabled, ExtraSpecs: desiredExtraSpecs(spec.DirectJIT)}
			var updated scaleSetDTO
			updateEndpoint := "/scalesets/" + strconv.FormatUint(uint64(scaleSet.ID), 10)
			if err := client.doJSON(ctx, http.MethodPut, updateEndpoint, token, request, &updated); err != nil {
				return nil, fmt.Errorf("disable and migrate GARM scale set activation: %w", err)
			}
			if err := validateScaleSet(updated, entity, spec); err != nil {
				return nil, fmt.Errorf("migrated GARM scale set failed verification: %w", err)
			}
			if updated.Enabled {
				return nil, fmt.Errorf("GARM scale set remained enabled during activation migration")
			}
			candidate = updated
		}
		scaleSet = candidate
	} else if err := validateScaleSet(scaleSet, entity, spec); err != nil {
		return nil, err
	}
	if options.Enable && !scaleSet.Enabled {
		result.Actions = append(result.Actions, "enable_verified_scale_set")
		if options.Apply {
			request := struct {
				Enabled bool `json:"enabled"`
			}{Enabled: true}
			var updated scaleSetDTO
			updateEndpoint := "/scalesets/" + strconv.FormatUint(uint64(scaleSet.ID), 10)
			if err := client.doJSON(ctx, http.MethodPut, updateEndpoint, token, request, &updated); err != nil {
				return nil, fmt.Errorf("enable verified GARM scale set: %w", err)
			}
			if err := validateScaleSet(updated, entity, spec); err != nil {
				return nil, fmt.Errorf("enabled GARM scale set failed verification: %w", err)
			}
			if !updated.Enabled {
				return nil, fmt.Errorf("GARM scale set remained disabled after enable request")
			}
			scaleSet = updated
		}
	}
	if !options.Enable && scaleSet.Enabled {
		return nil, fmt.Errorf("scale set is enabled but this reconciliation requires the disabled inspection state")
	}
	return &scaleSet, nil
}

func desiredScaleSetRequest(spec scaleSetSpec) createScaleSetRequest {
	return createScaleSetRequest{
		Name:                   spec.Name,
		DisableUpdate:          true,
		ProviderName:           DefaultProviderName,
		MaxRunners:             1,
		MinIdleRunners:         0,
		Image:                  spec.Image,
		Flavor:                 spec.Flavor,
		OSType:                 "linux",
		OSArch:                 "amd64",
		Enabled:                false,
		RunnerBootstrapTimeout: DefaultBootstrapTimeoutMins,
		ExtraSpecs:             desiredExtraSpecs(spec.DirectJIT),
		EnableShell:            false,
		RunnerPrefix:           DefaultRunnerPrefix,
		GitHubRunnerGroup:      "Default",
		Labels:                 []string{},
	}
}

func validateCredential(credential credentialDTO, selected tenant.Tenant, description string) error {
	if credential.ID == 0 || credential.Name != selected.CredentialName || credential.Description != description {
		return fmt.Errorf("GARM credential identity or managed key fingerprint drifted")
	}
	if credential.effectiveAuthType() != "app" || credential.Endpoint.Name != "github.com" || credential.Endpoint.EndpointType != "github" || (credential.ForgeType != "" && credential.ForgeType != "github") {
		return fmt.Errorf("GARM credential must use the github.com App endpoint")
	}
	return nil
}

func validateRepository(repository repositoryDTO, selected tenant.Tenant, credential credentialDTO) error {
	if repository.ID == "" || repository.Owner+"/"+repository.Name != selected.Repository {
		return fmt.Errorf("GARM repository identity drifted")
	}
	credentialName := repository.Credentials.Name
	if credentialName == "" {
		credentialName = repository.CredentialsName
	}
	if credentialName != selected.CredentialName || (repository.CredentialsID != 0 && repository.CredentialsID != credential.ID) {
		return fmt.Errorf("GARM repository credential binding drifted")
	}
	if repository.AgentMode || repository.Endpoint.Name != "github.com" || repository.Endpoint.EndpointType != "github" || repository.PoolBalancerType != DefaultPoolBalancerType || len(repository.Pools) != 0 {
		return fmt.Errorf("GARM repository execution mode, endpoint or pool boundary drifted")
	}
	return nil
}

func validateScaleSet(scaleSet scaleSetDTO, entity garmEntity, spec scaleSetSpec) error {
	owner := scaleSet.RepoID
	if entity.Kind == EntityKindOrganization {
		owner = scaleSet.OrgID
	}
	if scaleSet.ID == 0 || scaleSet.Name != spec.Name || owner == "" || owner != entity.ID {
		return fmt.Errorf("GARM scale set identity drifted")
	}
	if !scaleSet.DisableUpdate {
		return fmt.Errorf("GARM scale set runner auto-update is enabled")
	}
	if scaleSet.ProviderName != DefaultProviderName || scaleSet.Image != spec.Image || scaleSet.Flavor != spec.Flavor {
		return fmt.Errorf("GARM scale set provider, image or flavor drifted")
	}
	if scaleSet.MaxRunners != 1 || scaleSet.MinIdleRunners != 0 || scaleSet.RunnerBootstrapTimeout != DefaultBootstrapTimeoutMins {
		return fmt.Errorf("GARM scale set capacity or bootstrap timeout drifted")
	}
	if scaleSet.OSType != "linux" || scaleSet.OSArch != "amd64" || scaleSet.RunnerPrefix != DefaultRunnerPrefix {
		return fmt.Errorf("GARM scale set platform or runner prefix drifted")
	}
	if scaleSet.EnableShell || scaleSet.GitHubRunnerGroup != "Default" || len(scaleSet.Tags) != 0 {
		return fmt.Errorf("GARM scale set shell, runner group or labels drifted")
	}
	if !activationSpecsMatch(scaleSet.ExtraSpecs, spec.DirectJIT) {
		return fmt.Errorf("GARM scale set immutable guest update policy drifted")
	}
	return nil
}

func resolveScaleSetSpec(name string) (scaleSetSpec, error) {
	if name == "" {
		name = DefaultScaleSetName
	}
	published := PublishedScaleSets()
	for _, class := range published {
		if class.Name == name {
			return scaleSetSpec{Name: class.Name, Image: class.Image, Flavor: class.Flavor}, nil
		}
	}
	names := make([]string, 0, len(published))
	for _, class := range published {
		names = append(names, strconv.Quote(class.Name))
	}
	return scaleSetSpec{}, fmt.Errorf("scale set must be exactly %s", strings.Join(names, ", "))
}

func summarizeScaleSet(scaleSet scaleSetDTO) *ScaleSetSummary {
	return &ScaleSetSummary{
		ID:                    scaleSet.ID,
		GitHubScaleSetID:      scaleSet.ScaleSetID,
		Name:                  scaleSet.Name,
		Enabled:               scaleSet.Enabled,
		DisableRunnerUpdate:   scaleSet.DisableUpdate,
		Provider:              scaleSet.ProviderName,
		Image:                 scaleSet.Image,
		Flavor:                scaleSet.Flavor,
		MaximumRunners:        scaleSet.MaxRunners,
		MinimumIdleRunners:    scaleSet.MinIdleRunners,
		BootstrapTimeoutMins:  scaleSet.RunnerBootstrapTimeout,
		RemoteShellEnabled:    scaleSet.EnableShell,
		ImmutableGuestUpdates: knownActivationSpecs(scaleSet.ExtraSpecs),
		DirectJITActivation:   activationSpecsMatch(scaleSet.ExtraSpecs, true),
	}
}

func desiredExtraSpecs(directJIT bool) json.RawMessage {
	if directJIT {
		return json.RawMessage(`{"disable_updates":true,"nddev_direct_jit":true}`)
	}
	return json.RawMessage(`{"disable_updates":true}`)
}

func activationSpecsMatch(raw json.RawMessage, directJIT bool) bool {
	var specs map[string]any
	if json.Unmarshal(raw, &specs) != nil || specs["disable_updates"] != true {
		return false
	}
	if directJIT {
		return len(specs) == 2 && specs["nddev_direct_jit"] == true
	}
	return len(specs) == 1
}

func knownActivationSpecs(raw json.RawMessage) bool {
	return activationSpecsMatch(raw, false) || activationSpecsMatch(raw, true)
}

func matchingCredentials(credentials []credentialDTO, name string) []credentialDTO {
	var matches []credentialDTO
	for _, credential := range credentials {
		if credential.Name == name {
			matches = append(matches, credential)
		}
	}
	return matches
}

func validateOrganization(organization organizationDTO, selected tenant.Tenant, credential credentialDTO) error {
	if organization.ID == "" || organization.Name != selected.Owner {
		return fmt.Errorf("GARM organization identity drifted")
	}
	credentialName := organization.Credentials.Name
	if credentialName == "" {
		credentialName = organization.CredentialsName
	}
	if credentialName != selected.CredentialName || (organization.CredentialsID != 0 && organization.CredentialsID != credential.ID) {
		return fmt.Errorf("GARM organization credential binding drifted")
	}
	if organization.AgentMode || organization.Endpoint.Name != "github.com" || organization.Endpoint.EndpointType != "github" || organization.PoolBalancerType != DefaultPoolBalancerType || len(organization.Pools) != 0 {
		return fmt.Errorf("GARM organization execution mode, endpoint or pool boundary drifted")
	}
	return nil
}

func matchingOrganizations(organizations []organizationDTO, name string) []organizationDTO {
	var matches []organizationDTO
	for _, candidate := range organizations {
		if candidate.Name == name {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func matchingRepositories(repositories []repositoryDTO, repository string) []repositoryDTO {
	var matches []repositoryDTO
	for _, candidate := range repositories {
		if candidate.Owner+"/"+candidate.Name == repository {
			matches = append(matches, candidate)
		}
	}
	return matches
}

func matchingScaleSets(scaleSets []scaleSetDTO, name string) []scaleSetDTO {
	var matches []scaleSetDTO
	for _, scaleSet := range scaleSets {
		if scaleSet.Name == name {
			matches = append(matches, scaleSet)
		}
	}
	return matches
}

func randomSecret(reader io.Reader) (string, error) {
	if reader == nil {
		reader = rand.Reader
	}
	buffer := make([]byte, 32)
	defer clear(buffer)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", fmt.Errorf("generate unused webhook guard secret: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}
