package config

import "github.com/NDDev-OpenNetwork/github-actions/internal/tenant"

import "github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"

// Config is the versioned, portable policy consumed by the control plane.
// Secret values are intentionally absent: only environment-variable locators
// may appear in repository configuration.
type Config struct {
	SchemaVersion int                 `json:"schema_version" yaml:"schema_version"`
	Platform      Platform            `json:"platform" yaml:"platform"`
	ControlPlane  ControlPlane        `json:"control_plane" yaml:"control_plane"`
	Incus         Incus               `json:"incus" yaml:"incus"`
	Guardrails    Guardrails          `json:"guardrails" yaml:"guardrails"`
	HostReserve   HostReserve         `json:"host_reserve" yaml:"host_reserve"`
	Pressure      pressuregate.Policy `json:"pressure_admission" yaml:"pressure_admission"`
	Cache         Cache               `json:"cache" yaml:"cache"`
	Backends      []Backend           `json:"backends" yaml:"backends"`
	Pools         []Pool              `json:"pools" yaml:"pools"`
}

// Backend is one independently operated execution implementation. Platform and
// architecture are explicit because neither may be inferred from a pool label;
// failure domains remain separate even when two backends expose the same class.
type Backend struct {
	Name           string              `json:"name" yaml:"name"`
	Platform       string              `json:"platform" yaml:"platform"`
	Architecture   string              `json:"architecture" yaml:"architecture"`
	Implementation string              `json:"implementation" yaml:"implementation"`
	FailureDomain  string              `json:"failure_domain" yaml:"failure_domain"`
	Capabilities   BackendCapabilities `json:"capabilities" yaml:"capabilities"`
}

type BackendCapabilities struct {
	Docker bool `json:"docker" yaml:"docker"`
}

// Cluster is this host's place in the fleet's Incus cluster.
//
// The per-member ceilings stay in Incus above -- project_max_* is what one
// host may run -- because a cluster's project limits are a single fleet-wide
// total that Incus does not divide per member. Members is how the two relate:
// the project quota Incus is given is the per-member ceiling times Members,
// and the per-member ceiling is enforced by the placement scriptlet instead.
type Cluster struct {
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	MemberName string `json:"member_name" yaml:"member_name"`
	// APIAddress is what this member binds and what its peers reach it on. It
	// replaces the standalone loopback endpoint, which a cluster cannot use.
	APIAddress string `json:"api_address" yaml:"api_address"`
	// Members is the number of hosts in the cluster, and therefore the
	// multiplier between one host's ceiling and the fleet's.
	Members int `json:"members" yaml:"members"`
}

type Incus struct {
	Version                   string   `json:"version" yaml:"version"`
	APIAddress                string   `json:"api_address" yaml:"api_address"`
	Project                   string   `json:"project" yaml:"project"`
	StoragePool               string   `json:"storage_pool" yaml:"storage_pool"`
	StorageDriver             string   `json:"storage_driver" yaml:"storage_driver"`
	StorageSizeGiB            int      `json:"storage_size_gib" yaml:"storage_size_gib"`
	ProjectDiskLimitGiB       int      `json:"project_disk_limit_gib" yaml:"project_disk_limit_gib"`
	Network                   string   `json:"network" yaml:"network"`
	NetworkCIDR               string   `json:"network_cidr" yaml:"network_cidr"`
	EgressACL                 string   `json:"egress_acl" yaml:"egress_acl"`
	PublicHostAddress         string   `json:"public_host_address" yaml:"public_host_address"`
	EstatePublicHostAddresses []string `json:"estate_public_host_addresses" yaml:"estate_public_host_addresses"`
	ServicesHostAddress       string   `json:"services_host_address" yaml:"services_host_address"`
	ProjectMaxInstances       int      `json:"project_max_instances" yaml:"project_max_instances"`
	ProjectMaxCPUUnits        int      `json:"project_max_cpu_units" yaml:"project_max_cpu_units"`
	ProjectMaxMemoryMiB       int      `json:"project_max_memory_mib" yaml:"project_max_memory_mib"`
	// Cluster describes this host's membership of the fleet's Incus cluster.
	// A cluster is what lets one queue place a worker on any host: the
	// provider talks to one API and Incus decides which member runs the VM.
	// Left disabled, every field below is unused and the host is standalone,
	// which is what a single-host deployment and every existing test expect.
	Cluster          Cluster `json:"cluster" yaml:"cluster"`
	RegistryPort     int     `json:"registry_port" yaml:"registry_port"`
	RustFSPort       int     `json:"rustfs_port" yaml:"rustfs_port"`
	CacheGatewayPort int     `json:"cache_gateway_port" yaml:"cache_gateway_port"`
	GARMGatewayPort  int     `json:"garm_gateway_port" yaml:"garm_gateway_port"`
}

type Platform struct {
	Name string `json:"name" yaml:"name"`
	Host string `json:"host" yaml:"host"`
}

type ControlPlane struct {
	Manager            string `json:"manager" yaml:"manager"`
	ManagerVersion     string `json:"manager_version" yaml:"manager_version"`
	SchedulingMode     string `json:"scheduling_mode" yaml:"scheduling_mode"`
	Provider           string `json:"provider" yaml:"provider"`
	ProviderVersion    string `json:"provider_version" yaml:"provider_version"`
	ProviderInterface  string `json:"provider_interface" yaml:"provider_interface"`
	WorkerKind         string `json:"worker_kind" yaml:"worker_kind"`
	Runner             string `json:"runner" yaml:"runner"`
	RunnerVersion      string `json:"runner_version" yaml:"runner_version"`
	RunnerUpdatePolicy string `json:"runner_update_policy" yaml:"runner_update_policy"`
}

type Guardrails struct {
	RequireEphemeral                bool   `json:"require_ephemeral" yaml:"require_ephemeral"`
	JobsPerWorker                   int    `json:"jobs_per_worker" yaml:"jobs_per_worker"`
	WarmInstancesUnregistered       bool   `json:"warm_instances_unregistered" yaml:"warm_instances_unregistered"`
	DenyHostDockerSocket            bool   `json:"deny_host_docker_socket" yaml:"deny_host_docker_socket"`
	DenyNestedVirtualization        bool   `json:"deny_nested_virtualization" yaml:"deny_nested_virtualization"`
	DenyPrivateNetworkByDefault     bool   `json:"deny_private_network_by_default" yaml:"deny_private_network_by_default"`
	CPUSchedulingMode               string `json:"cpu_scheduling_mode" yaml:"cpu_scheduling_mode"`
	HardMemoryExcludesEmergencySwap bool   `json:"hard_memory_excludes_emergency_swap" yaml:"hard_memory_excludes_emergency_swap"`
	EmergencySwapSchedulable        bool   `json:"emergency_swap_schedulable" yaml:"emergency_swap_schedulable"`
	AllowMemoryBallooning           bool   `json:"allow_memory_ballooning" yaml:"allow_memory_ballooning"`
}

// HostReserve is capacity the fleet must leave for everything that is not the
// fleet. Its floor depends on what the host actually carries: a host retaining
// the twelve legacy listeners and the Declaro and Captcha stacks must protect
// them, while a dedicated fleet host has only its own control plane and the
// operating system to protect. Declaring the mode is mandatory so a dedicated
// floor is always a deliberate statement about a host, never a default.
type HostReserve struct {
	Mode             string `json:"mode" yaml:"mode"`
	MinimumCPUUnits  int    `json:"minimum_cpu_units" yaml:"minimum_cpu_units"`
	MinimumMemoryMiB int    `json:"minimum_memory_mib" yaml:"minimum_memory_mib"`
	MinimumPercent   int    `json:"minimum_percent" yaml:"minimum_percent"`
	// MaximumFleetCPUPercent is the aggregate cgroup ceiling for Incus and all
	// worker processes it owns. It is deliberately below the host-wide SLO so
	// the kernel, telemetry and member services retain CPU during a burst.
	MaximumFleetCPUPercent int `json:"maximum_fleet_cpu_percent" yaml:"maximum_fleet_cpu_percent"`
	MinimumFreeDiskPercent int `json:"minimum_free_disk_percent" yaml:"minimum_free_disk_percent"`
}

type Cache struct {
	ObjectStore         ObjectStore `json:"object_store" yaml:"object_store"`
	NamespaceTemplate   string      `json:"namespace_template" yaml:"namespace_template"`
	UntrustedWriteScope string      `json:"untrusted_write_scope" yaml:"untrusted_write_scope"`
	ReleaseWriteScope   string      `json:"release_write_scope" yaml:"release_write_scope"`
}

type ObjectStore struct {
	Implementation string `json:"implementation" yaml:"implementation"`
	EndpointEnv    string `json:"endpoint_env" yaml:"endpoint_env"`
	AccessKeyEnv   string `json:"access_key_env" yaml:"access_key_env"`
	SecretKeyEnv   string `json:"secret_key_env" yaml:"secret_key_env"`
	Bucket         string `json:"bucket" yaml:"bucket"`
}

type Pool struct {
	Name string `json:"name" yaml:"name"`
	// Backend selects one typed execution implementation. Hosted jobs never
	// select a fleet pool and therefore never enter this configuration.
	Backend string `json:"backend" yaml:"backend"`
	// Tenant names the forge account this pool serves. A GitHub scale set
	// name is unique per entity, not globally, so the pair is the identity
	// and this field is the half the scale set name cannot carry. Empty
	// means the fleet's own tenant, which keeps every single-tenant host
	// configuration valid without restating what it already means.
	Tenant       string    `json:"tenant,omitempty" yaml:"tenant,omitempty"`
	ScaleSetName string    `json:"scale_set_name" yaml:"scale_set_name"`
	Trust        string    `json:"trust" yaml:"trust"`
	Resources    Resources `json:"resources" yaml:"resources"`
	// Reservation is measured admission demand, not the container hard limit.
	// Incus still applies Resources as the safety ceiling; queue/provider
	// admission and placement use this p95-derived envelope so idle headroom is
	// not reserved as if every worker simultaneously reached its hard maximum.
	Reservation  Reservation  `json:"reservation" yaml:"reservation"`
	MaxRunning   int          `json:"max_running" yaml:"max_running"`
	Warm         WarmPool     `json:"warm" yaml:"warm"`
	Capabilities Capabilities `json:"capabilities" yaml:"capabilities"`
}

// TenantID resolves the declared tenant, defaulting to the fleet's own.
func (p Pool) TenantID() string {
	if p.Tenant == "" {
		return tenant.DefaultID
	}
	return p.Tenant
}

type Resources struct {
	VCPU      int `json:"vcpu" yaml:"vcpu"`
	MemoryMiB int `json:"memory_mib" yaml:"memory_mib"`
	DiskGiB   int `json:"disk_gib" yaml:"disk_gib"`
}

type Reservation struct {
	CPUUnits  int `json:"cpu_units" yaml:"cpu_units"`
	MemoryMiB int `json:"memory_mib" yaml:"memory_mib"`
}

// EffectiveReservation returns the measured fleet admission envelope. The
// current immutable classes use seven-day OpenObserve p95 memory rounded up to
// 256 MiB; one CPU unit lets host PSI, rather than worst-case cgroup ceilings,
// close admission under real contention. Unknown development fixtures retain
// their hard limits until they have measured evidence.
func (p Pool) EffectiveReservation() Reservation {
	if p.Reservation.CPUUnits > 0 && p.Reservation.MemoryMiB > 0 {
		return p.Reservation
	}
	memory := map[int]int{2048: 512, 3072: 512, 4096: 2560, 6144: 2048}[p.Resources.MemoryMiB]
	if memory == 0 {
		return Reservation{CPUUnits: p.Resources.VCPU, MemoryMiB: p.Resources.MemoryMiB}
	}
	return Reservation{CPUUnits: 1, MemoryMiB: memory}
}

type WarmPool struct {
	TargetReady int `json:"target_ready" yaml:"target_ready"`
	MaxReady    int `json:"max_ready" yaml:"max_ready"`
}

type Capabilities struct {
	Docker          bool   `json:"docker" yaml:"docker"`
	Credentials     string `json:"credentials" yaml:"credentials"`
	NetworkPolicy   string `json:"network_policy" yaml:"network_policy"`
	CacheWriteScope string `json:"cache_write_scope" yaml:"cache_write_scope"`
	// EgressAllowlist is the exact set of destinations this pool may reach
	// beyond the bounded public egress every pool receives. It is only
	// meaningful under the release-allowlist policy, and it is per pool
	// rather than per host on purpose: a destination opened on the shared
	// bridge is opened for every pool on it, including other tenants'.
	EgressAllowlist []EgressDestination `json:"egress_allowlist" yaml:"egress_allowlist"`
}

// EgressDestination is one reviewed hole in a release pool's egress.
//
// Every field is required and none has a default. A destination is one exact
// public address, a protocol, explicit ports, and the reason it exists -- the
// reason is what a reviewer reads, so a rule cannot be added without stating
// what it is for.
type EgressDestination struct {
	Destination string `json:"destination" yaml:"destination"`
	Protocol    string `json:"protocol" yaml:"protocol"`
	Ports       string `json:"ports" yaml:"ports"`
	Purpose     string `json:"purpose" yaml:"purpose"`
}

// ReservedEgressDestinations are the ranges no worker may reach whatever its
// pool declares: private, loopback, carrier-grade NAT, link-local metadata,
// benchmarking, multicast and reserved space.
//
// The egress ACL rejects them and pool allowlist validation refuses to name
// anything inside them, which is what makes the two independent of rule
// ordering: a declared destination cannot overlap a rejected range, so it
// cannot matter which rule an implementation evaluates first.
func ReservedEgressDestinations() []string {
	return []string{
		"0.0.0.0/8",
		"10.0.0.0/8",
		"100.64.0.0/10",
		"127.0.0.0/8",
		"169.254.0.0/16",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"198.18.0.0/15",
		"224.0.0.0/4",
		"240.0.0.0/4",
	}
}

func (c Config) Pool(name string) (Pool, bool) {
	for _, pool := range c.Pools {
		if pool.Name == name {
			return pool, true
		}
	}
	return Pool{}, false
}

func (c Config) Backend(name string) (Backend, bool) {
	for _, backend := range c.Backends {
		if backend.Name == name {
			return backend, true
		}
	}
	return Backend{}, false
}

// EffectiveAPIAddress is the address this host's Incus actually binds. A
// cluster member cannot serve its peers on loopback, so clustering replaces
// the standalone endpoint rather than adding to it.
func (i Incus) EffectiveAPIAddress() string {
	if i.Cluster.Enabled && i.Cluster.APIAddress != "" {
		return i.Cluster.APIAddress
	}
	return i.APIAddress
}

// ClusterMembers is how many hosts share this project's quota. One when
// standalone, so every derivation below is the same arithmetic either way.
func (i Incus) ClusterMembers() int {
	if i.Cluster.Enabled && i.Cluster.Members > 0 {
		return i.Cluster.Members
	}
	return 1
}

// FleetMaxInstances, FleetMaxCPUUnits and FleetMaxMemoryMiB are the project
// quota Incus is given. In a cluster that quota is one fleet-wide total, not a
// per-member limit, so it is the per-member ceiling times the member count and
// the placement scriptlet is what keeps any single member inside its share.
func (i Incus) FleetMaxInstances() int { return i.ProjectMaxInstances * i.ClusterMembers() }

func (i Incus) FleetMaxCPUUnits() int { return i.ProjectMaxCPUUnits * i.ClusterMembers() }

func (i Incus) FleetMaxMemoryMiB() int { return i.ProjectMaxMemoryMiB * i.ClusterMembers() }

func (i Incus) FleetDiskLimitGiB() int { return i.ProjectDiskLimitGiB * i.ClusterMembers() }
