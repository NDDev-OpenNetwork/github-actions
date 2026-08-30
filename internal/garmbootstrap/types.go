package garmbootstrap

import (
	"encoding/json"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

const (
	DefaultBaseURL = "http://127.0.0.1:9997/api/v1"
	// Account identity — owner, repository, App slug and credential name —
	// belongs to the selected Tenant rather than to a constant, so one build
	// can serve more than one account without either being able to answer for
	// the other. See tenant.go for why that set is closed.
	//
	// GARM binds scale sets to one forge entity. A repository entity serves
	// exactly the repository it names; an organization entity serves every
	// repository the organization holds, including ones created later. The
	// second is what lets one fleet replace listeners shared across an
	// account, and it widens who can reach these pools, so it is opt-in and
	// never inferred. A repository allowlist, if one is wanted, belongs on the
	// GitHub runner group rather than here.
	EntityKindRepository              = "repository"
	EntityKindOrganization            = "organization"
	DefaultScaleSetName               = "nddev-linux-standard"
	IntegrationScaleSetName           = "nddev-linux-integration"
	FastScaleSetName                  = "nddev-linux-fast"
	UntrustedScaleSetName             = "nddev-linux-untrusted"
	ReleaseScaleSetName               = "nddev-linux-release"
	ContainerCanaryScaleSetName       = "nddev-linux-container-canary"
	DockerContainerCanaryScaleSetName = "nddev-linux-docker-container-canary"
	PriorityStandardScaleSetName      = "nddev-priority-standard"
	PriorityIntegrationScaleSetName   = "nddev-priority-integration"
	PriorityUntrustedScaleSetName     = "nddev-priority-untrusted"
	DefaultPoolBalancerType           = "roundrobin"
	DefaultProviderName               = "nddev-incus"
	DefaultImage                      = "nddev-ubuntu-24.04-amd64-container-current"
	IntegrationImage                  = "nddev-u24-amd64-ctr-docker-runner-2.336.0-r20260801-b10"
	PriorityStandardImage             = "nddev-ubuntu-24.04-amd64-container-runner-2.336.0-r20260801-b16"
	PriorityIntegrationImage          = "nddev-u24-amd64-ctr-docker-runner-2.336.0-r20260801-b10"
	// Every Linux class is an ephemeral Incus container. Docker-capable classes
	// use their nested-runtime image; release uses a separately stage-smoked
	// standard image so OIDC authority does not inherit Docker/nesting.
	FastImage                   = ContainerCanaryImage
	UntrustedImage              = IntegrationImage
	ReleaseImage                = "nddev-ubuntu-24.04-amd64-container-runner-2.336.0-r20260801-b16"
	ContainerCanaryImage        = "nddev-ubuntu-24.04-amd64-container-current"
	DockerContainerCanaryImage  = "nddev-u24-amd64-ctr-docker-runner-2.336.0-r20260801-b10"
	DefaultFlavor               = "nddev-linux-standard"
	IntegrationFlavor           = "nddev-linux-integration"
	FastFlavor                  = "nddev-linux-fast"
	UntrustedFlavor             = "nddev-linux-untrusted"
	ReleaseFlavor               = "nddev-linux-release"
	ContainerCanaryFlavor       = "nddev-linux-container-canary"
	DockerContainerCanaryFlavor = "nddev-linux-docker-container-canary"
	PriorityStandardFlavor      = "nddev-priority-standard"
	PriorityIntegrationFlavor   = "nddev-priority-integration"
	PriorityUntrustedFlavor     = "nddev-priority-untrusted"
	DefaultRunnerPrefix         = "nddev"
	DefaultBootstrapTimeoutMins = uint(5)
	// Mirrors githubappbootstrap. Duplicated rather than imported because the
	// reconciler must not depend on the one-time bootstrap path to read a
	// bundle that bootstrap already wrote and left behind.
	ActionsReadPermission = "actions"
	// Mirrors githubappbootstrap. An organization entity needs this permission
	// on its App; a repository entity does not and should not hold it.
	OrganizationRunnersPermission = "organization_self_hosted_runners"
	OwnerTypeOrganization         = "organization"
	OwnerTypeUser                 = "user"
	ActivationModeDirectJIT       = "direct-jit"
	ActivationModeMetadata        = "metadata"
)

// ScaleSetClass is one runner class the reconciler can register: the GARM scale
// set name, the Incus image alias a worker of that class boots from, and the
// pool policy the provider selects by.
type ScaleSetClass struct {
	Name             string
	Image            string
	Flavor           string
	MaxRunners       uint
	RepositoryScoped bool
	Trust            string
	Credentials      string
	NetworkPolicy    string
	CacheWriteScope  string
	Docker           bool
	Browser          bool
	VCPU             int
	MemoryMiB        int
	DiskGiB          int
}

// PublishedScaleSets is the closed set of classes this reconciler can register,
// and the only place that set is written down. Registering a class is what
// makes GitHub keep it and route jobs to it, so the deployment contract proves
// every entry here has a worker image the provider can build: a class that is
// registrable and not buildable does not fail to exist, it accepts work and
// fails every create with a pool that has no pinned worker image.
func PublishedScaleSets() []ScaleSetClass {
	return []ScaleSetClass{
		class(DefaultScaleSetName, DefaultImage, DefaultFlavor, 16, "trusted", "repository", "public-internet", "trusted", false, false, 2, 4096, 30, false),
		class(IntegrationScaleSetName, IntegrationImage, IntegrationFlavor, 8, "trusted", "repository", "public-internet", "trusted", true, true, 4, 6144, 50, false),
		class(FastScaleSetName, FastImage, FastFlavor, 16, "trusted", "repository", "public-internet", "trusted", false, false, 2, 3072, 30, false),
		class(UntrustedScaleSetName, UntrustedImage, UntrustedFlavor, 8, "untrusted", "none", "public-internet", "none", true, false, 4, 6144, 50, false),
		class(ReleaseScaleSetName, ReleaseImage, ReleaseFlavor, 1, "release", "oidc-only", "release-allowlist", "none", false, false, 4, 6144, 40, false),
		class(ContainerCanaryScaleSetName, ContainerCanaryImage, ContainerCanaryFlavor, 12, "trusted", "none", "public-internet", "none", false, false, 2, 2048, 20, false),
		class(DockerContainerCanaryScaleSetName, DockerContainerCanaryImage, DockerContainerCanaryFlavor, 1, "trusted", "repository", "public-internet", "none", true, false, 2, 4096, 30, false),
		class(PriorityStandardScaleSetName, PriorityStandardImage, PriorityStandardFlavor, 16, "trusted", "repository", "public-internet", "none", false, false, 2, 4096, 30, true),
		class(PriorityIntegrationScaleSetName, PriorityIntegrationImage, PriorityIntegrationFlavor, 8, "trusted", "repository", "public-internet", "none", true, true, 4, 6144, 50, true),
		class(PriorityUntrustedScaleSetName, UntrustedImage, PriorityUntrustedFlavor, 8, "untrusted", "none", "public-internet", "none", true, false, 4, 6144, 50, true),
	}
}

func class(name, image, flavor string, maxRunners uint, trust, credentials, networkPolicy, cacheWriteScope string, docker, browser bool, vcpu, memoryMiB, diskGiB int, repositoryScoped bool) ScaleSetClass {
	return ScaleSetClass{
		Name: name, Image: image, Flavor: flavor, MaxRunners: maxRunners, RepositoryScoped: repositoryScoped,
		Trust: trust, Credentials: credentials, NetworkPolicy: networkPolicy,
		CacheWriteScope: cacheWriteScope, Docker: docker, Browser: browser,
		VCPU: vcpu, MemoryMiB: memoryMiB, DiskGiB: diskGiB,
	}
}

type Options struct {
	Registry tenant.Registry
	// Tenant selects which account this run reconciles. Empty means
	// DefaultTenantID, so an operator who passes nothing gets the account that
	// was already deployed.
	Tenant               string
	Repository           string
	BaseURL              string
	AdminCredentialsPath string
	CredentialAnchorPath string
	AppBundleDirectory   string
	ScaleSetName         string
	EntityKind           string
	Apply                bool
	Enable               bool
	ActivationMode       string
	MigrateActivation    bool
	MigrateCapacity      bool
	MigrateImage         bool
}

type Result struct {
	SchemaVersion  int              `json:"schema_version"`
	Applied        bool             `json:"applied"`
	Actions        []string         `json:"actions"`
	Credential     *ResourceSummary `json:"credential,omitempty"`
	Repository     *ResourceSummary `json:"repository,omitempty"`
	Organization   *ResourceSummary `json:"organization,omitempty"`
	ScaleSet       *ScaleSetSummary `json:"scale_set,omitempty"`
	ReadyToEnable  bool             `json:"ready_to_enable"`
	ReadyForCanary bool             `json:"ready_for_canary"`
	ObservedAt     time.Time        `json:"observed_at"`
}

type ResourceSummary struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ScaleSetSummary struct {
	ID                    uint   `json:"id"`
	GitHubScaleSetID      int    `json:"github_scale_set_id"`
	Name                  string `json:"name"`
	Enabled               bool   `json:"enabled"`
	DisableRunnerUpdate   bool   `json:"disable_runner_update"`
	Provider              string `json:"provider"`
	Image                 string `json:"image"`
	Flavor                string `json:"flavor"`
	MaximumRunners        uint   `json:"maximum_runners"`
	MinimumIdleRunners    uint   `json:"minimum_idle_runners"`
	BootstrapTimeoutMins  uint   `json:"bootstrap_timeout_minutes"`
	RemoteShellEnabled    bool   `json:"remote_shell_enabled"`
	ImmutableGuestUpdates bool   `json:"immutable_guest_updates_disabled"`
	DirectJITActivation   bool   `json:"direct_jit_warm_activation"`
}

type adminCredentials struct {
	Email    string `json:"email"`
	FullName string `json:"full_name"`
	Password string `json:"password"`
	Username string `json:"username"`
}

type verifiedInstallation struct {
	SchemaVersion  int    `json:"schema_version"`
	AppID          int64  `json:"app_id"`
	AppSlug        string `json:"app_slug"`
	InstallationID int64  `json:"installation_id"`
	AccountLogin   string `json:"account_login"`
	// OwnerType mirrors the field the App bootstrapper writes. The decoder
	// here is strict, so a field the writer emits and the reader does not
	// declare rejects the whole bundle — which is what happened to every
	// bundle produced after the writer gained this field.
	OwnerType           string            `json:"owner_type"`
	Repository          string            `json:"repository"`
	RepositorySelection string            `json:"repository_selection"`
	Permissions         map[string]string `json:"permissions"`
	PrivateKeyPath      string            `json:"private_key_path"`
	VerifiedAt          time.Time         `json:"verified_at"`
}

type appBundle struct {
	Installation verifiedInstallation
	PrivateKey   []byte
	Anchor       credentialAnchor
	Description  string
}

type credentialAnchor struct {
	SchemaVersion  int    `json:"schema_version"`
	CredentialName string `json:"credential_name"`
	AppID          int64  `json:"app_id"`
	InstallationID int64  `json:"installation_id"`
	KeySHA256      string `json:"key_sha256"`
}

type endpointDTO struct {
	Name         string `json:"name"`
	EndpointType string `json:"endpoint_type"`
}

type credentialDTO struct {
	ID                uint        `json:"id"`
	Name              string      `json:"name"`
	Description       string      `json:"description"`
	AuthType          string      `json:"auth-type"`
	AlternateAuthType string      `json:"auth_type"`
	APIBaseURL        string      `json:"api_base_url"`
	Endpoint          endpointDTO `json:"endpoint"`
	ForgeType         string      `json:"forge_type"`
}

func (c credentialDTO) effectiveAuthType() string {
	if c.AuthType != "" {
		return c.AuthType
	}
	return c.AlternateAuthType
}

type repositoryDTO struct {
	ID               string            `json:"id"`
	Owner            string            `json:"owner"`
	Name             string            `json:"name"`
	CredentialsName  string            `json:"credentials_name"`
	CredentialsID    uint              `json:"credentials_id"`
	Credentials      credentialDTO     `json:"credentials"`
	AgentMode        bool              `json:"agent_mode"`
	PoolBalancerType string            `json:"pool_balancing_type"`
	Endpoint         endpointDTO       `json:"endpoint"`
	Pools            []json.RawMessage `json:"pool"`
}

type tagDTO struct {
	Name string `json:"name"`
}

type scaleSetDTO struct {
	ID                     uint            `json:"id"`
	ScaleSetID             int             `json:"scale_set_id"`
	Name                   string          `json:"name"`
	DisableUpdate          bool            `json:"disable_update"`
	ProviderName           string          `json:"provider_name"`
	MaxRunners             uint            `json:"max_runners"`
	MinIdleRunners         uint            `json:"min_idle_runners"`
	Image                  string          `json:"image"`
	Flavor                 string          `json:"flavor"`
	OSType                 string          `json:"os_type"`
	OSArch                 string          `json:"os_arch"`
	Enabled                bool            `json:"enabled"`
	EnableShell            bool            `json:"enable_shell"`
	RunnerPrefix           string          `json:"runner_prefix"`
	RunnerBootstrapTimeout uint            `json:"runner_bootstrap_timeout"`
	ExtraSpecs             json.RawMessage `json:"extra_specs"`
	GitHubRunnerGroup      string          `json:"github-runner-group"`
	Tags                   []tagDTO        `json:"tags"`
	// GARM populates exactly one owner field per scale set, according to the
	// entity it hangs from. Checking the wrong one passes vacuously against an
	// empty string, so validation selects by entity kind rather than reading
	// whichever is non-empty.
	RepoID string `json:"repo_id"`
	OrgID  string `json:"org_id"`
}

type createCredentialRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Endpoint    string `json:"endpoint"`
	AuthType    string `json:"auth_type"`
	App         struct {
		AppID           int64  `json:"app_id"`
		InstallationID  int64  `json:"installation_id"`
		PrivateKeyBytes []byte `json:"private_key_bytes"`
	} `json:"app"`
}

type organizationDTO struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	CredentialsName  string            `json:"credentials_name"`
	CredentialsID    uint              `json:"credentials_id"`
	Credentials      credentialDTO     `json:"credentials"`
	AgentMode        bool              `json:"agent_mode"`
	PoolBalancerType string            `json:"pool_balancing_type"`
	Endpoint         endpointDTO       `json:"endpoint"`
	Pools            []json.RawMessage `json:"pool"`
}

type createOrganizationRequest struct {
	Name             string `json:"name"`
	CredentialsName  string `json:"credentials_name"`
	WebhookSecret    string `json:"webhook_secret"`
	PoolBalancerType string `json:"pool_balancer_type"`
	ForgeType        string `json:"forge_type"`
	AgentMode        bool   `json:"agent_mode"`
}

type createRepositoryRequest struct {
	Owner            string `json:"owner"`
	Name             string `json:"name"`
	CredentialsName  string `json:"credentials_name"`
	WebhookSecret    string `json:"webhook_secret"`
	PoolBalancerType string `json:"pool_balancer_type"`
	ForgeType        string `json:"forge_type"`
	AgentMode        bool   `json:"agent_mode"`
}

type createScaleSetRequest struct {
	Name                   string          `json:"name"`
	DisableUpdate          bool            `json:"disable_update"`
	ProviderName           string          `json:"provider_name"`
	MaxRunners             uint            `json:"max_runners"`
	MinIdleRunners         uint            `json:"min_idle_runners"`
	Image                  string          `json:"image"`
	Flavor                 string          `json:"flavor"`
	OSType                 string          `json:"os_type"`
	OSArch                 string          `json:"os_arch"`
	Enabled                bool            `json:"enabled"`
	RunnerBootstrapTimeout uint            `json:"runner_bootstrap_timeout"`
	ExtraSpecs             json.RawMessage `json:"extra_specs"`
	EnableShell            bool            `json:"enable_shell"`
	RunnerPrefix           string          `json:"runner_prefix"`
	GitHubRunnerGroup      string          `json:"github-runner-group"`
	Labels                 []string        `json:"labels"`
}
