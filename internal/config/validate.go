package config

import (
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/cachenamespace"
	"github.com/NDDev-OpenNetwork/github-actions/internal/tenant"
)

var (
	namePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{2,62}$`)
	envPattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	bucketPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
)

type Issue struct {
	Path    string
	Message string
}

type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Issues))
	for _, issue := range e.Issues {
		parts = append(parts, issue.Path+": "+issue.Message)
	}
	return "invalid platform configuration: " + strings.Join(parts, "; ")
}

func (c Config) Validate() error {
	issues := make([]Issue, 0)
	add := func(path, message string) {
		issues = append(issues, Issue{Path: path, Message: message})
	}

	if c.SchemaVersion != 2 {
		add("schema_version", "must be 2")
	}
	if !namePattern.MatchString(c.Platform.Name) {
		add("platform.name", "must be a lowercase DNS-style name")
	}
	if !namePattern.MatchString(c.Platform.Host) {
		add("platform.host", "must be a lowercase DNS-style name")
	}

	requireEqual(add, "control_plane.manager", c.ControlPlane.Manager, "garm")
	validateVersion(add, "control_plane.manager_version", c.ControlPlane.ManagerVersion)
	requireEqual(add, "control_plane.scheduling_mode", c.ControlPlane.SchedulingMode, "scale-set")
	requireEqual(add, "control_plane.provider", c.ControlPlane.Provider, "incus")
	validateVersion(add, "control_plane.provider_version", c.ControlPlane.ProviderVersion)
	requireEqual(add, "control_plane.provider_interface", c.ControlPlane.ProviderInterface, "v0.1.0")
	requireEqual(add, "control_plane.worker_kind", c.ControlPlane.WorkerKind, "incus-container")
	requireEqual(add, "control_plane.runner", c.ControlPlane.Runner, "actions/runner")
	validateVersion(add, "control_plane.runner_version", c.ControlPlane.RunnerVersion)
	requireEqual(add, "control_plane.runner_update_policy", c.ControlPlane.RunnerUpdatePolicy, "image-canary")
	validateIncus(add, c.Incus)

	if !c.Guardrails.RequireEphemeral {
		add("guardrails.require_ephemeral", "must be true")
	}
	if c.Guardrails.JobsPerWorker != 1 {
		add("guardrails.jobs_per_worker", "must be exactly 1")
	}
	if !c.Guardrails.WarmInstancesUnregistered {
		add("guardrails.warm_instances_unregistered", "must be true")
	}
	if !c.Guardrails.DenyHostDockerSocket {
		add("guardrails.deny_host_docker_socket", "must be true")
	}
	if !c.Guardrails.DenyNestedVirtualization {
		add("guardrails.deny_nested_virtualization", "must be true")
	}
	if !c.Guardrails.DenyPrivateNetworkByDefault {
		add("guardrails.deny_private_network_by_default", "must be true")
	}
	if c.Guardrails.CPUSchedulingMode != "weighted-overcommit" {
		add("guardrails.cpu_scheduling_mode", "must be weighted-overcommit because limits.cpu.allowance is a work-conserving share")
	}
	if !c.Guardrails.HardMemoryExcludesEmergencySwap {
		add("guardrails.hard_memory_excludes_emergency_swap", "must be true")
	}
	if c.Guardrails.EmergencySwapSchedulable {
		add("guardrails.emergency_swap_schedulable", "must be false")
	}
	if c.Guardrails.AllowMemoryBallooning {
		add("guardrails.allow_memory_ballooning", "must remain false until benchmark approval")
	}

	// A retained-workloads host shares its capacity with the legacy listeners
	// and the retained application stacks, so its reserve must be large enough
	// to keep them alive under fleet load. A dedicated host protects only the
	// control plane, measured below 1 GiB, and the operating system. The 2 GiB
	// emergency swap absorbs a short anonymous-memory spike without turning
	// swap into scheduled capacity. Dedicated fleet hosts may use a measured
	// five-percent RAM reserve when at least 768 MiB remains for the host; PSI
	// and the hard worker limits remain the runtime stop before swap thrash.
	cpuFloor, memoryFloor := 4, 16*1024
	switch c.HostReserve.Mode {
	case "retained-workloads":
	case "dedicated":
		cpuFloor, memoryFloor = 2, 768
	default:
		add("host_reserve.mode", "must be retained-workloads or dedicated")
	}
	if c.HostReserve.MinimumCPUUnits < cpuFloor {
		add("host_reserve.minimum_cpu_units", fmt.Sprintf("must be at least %d for a %s host", cpuFloor, c.HostReserve.Mode))
	}
	if c.HostReserve.MinimumMemoryMiB < memoryFloor {
		add("host_reserve.minimum_memory_mib", fmt.Sprintf("must be at least %d for a %s host", memoryFloor, c.HostReserve.Mode))
	}
	if c.HostReserve.MinimumPercent < 5 || c.HostReserve.MinimumPercent > 50 {
		add("host_reserve.minimum_percent", "must be between 5 and 50")
	}
	if c.HostReserve.MaximumFleetCPUPercent < 80 || c.HostReserve.MaximumFleetCPUPercent > 98 {
		add("host_reserve.maximum_fleet_cpu_percent", "must be between 80 and 98 so the hard host-wide CPU ceiling leaves operating-system headroom")
	}
	if c.HostReserve.MinimumFreeDiskPercent < 20 || c.HostReserve.MinimumFreeDiskPercent > 80 {
		add("host_reserve.minimum_free_disk_percent", "must be between 20 and 80")
	}
	if err := c.Pressure.Validate(); err != nil {
		add("pressure_admission", err.Error())
	}

	requireEqual(add, "cache.object_store.implementation", c.Cache.ObjectStore.Implementation, "rustfs")
	validateEnv(add, "cache.object_store.endpoint_env", c.Cache.ObjectStore.EndpointEnv)
	validateEnv(add, "cache.object_store.access_key_env", c.Cache.ObjectStore.AccessKeyEnv)
	validateEnv(add, "cache.object_store.secret_key_env", c.Cache.ObjectStore.SecretKeyEnv)
	if !bucketPattern.MatchString(c.Cache.ObjectStore.Bucket) {
		add("cache.object_store.bucket", "must be a valid S3 bucket name")
	}
	// Containment was the whole check, so the template could name every token
	// and still describe a different namespace than the one built: it declared
	// eight segments while internal/cachenamespace wrote nine, and nothing
	// compared the two. The template is now held to the implementation, because
	// a shape nobody builds from is documentation pretending to be a contract.
	if c.Cache.NamespaceTemplate != cachenamespace.Template() {
		add("cache.namespace_template", "must be "+cachenamespace.Template())
	}
	if c.Cache.UntrustedWriteScope != "isolated" {
		add("cache.untrusted_write_scope", "must be isolated")
	}
	if c.Cache.ReleaseWriteScope != "none" {
		add("cache.release_write_scope", "must be none")
	}

	if len(c.Backends) == 0 {
		add("backends", "must contain at least one execution backend")
	}
	seenBackends := make(map[string]struct{}, len(c.Backends))
	backendByName := make(map[string]Backend, len(c.Backends))
	seenBackendIdentities := make(map[string]struct{}, len(c.Backends))
	for index, backend := range c.Backends {
		prefix := fmt.Sprintf("backends[%d]", index)
		validateBackend(add, prefix, backend, c.Platform.Host)
		if _, exists := seenBackends[backend.Name]; exists {
			add(prefix+".name", "must be unique")
		}
		seenBackends[backend.Name] = struct{}{}
		backendByName[backend.Name] = backend
		identity := strings.Join([]string{backend.Platform, backend.Architecture, backend.Implementation, backend.FailureDomain}, "\x00")
		if _, exists := seenBackendIdentities[identity]; exists {
			add(prefix, "must not duplicate another platform, architecture, implementation and failure domain")
		}
		seenBackendIdentities[identity] = struct{}{}
	}

	if len(c.Pools) == 0 {
		add("pools", "must contain at least one pool")
	}
	seenNames := make(map[string]struct{}, len(c.Pools))
	seenScaleSets := make(map[string]struct{}, len(c.Pools))
	maxPoolCPU := 0
	maxPoolMemory := 0
	maxPoolDisk := 0
	for index, pool := range c.Pools {
		prefix := fmt.Sprintf("pools[%d]", index)
		validatePool(add, prefix, pool)
		backend, exists := backendByName[pool.Backend]
		if !exists {
			add(prefix+".backend", "must reference a declared execution backend")
		} else if pool.Capabilities.Docker && !backend.Capabilities.Docker {
			add(prefix+".capabilities.docker", "requires Docker support from the selected execution backend")
		}
		if exists && backend.Implementation == "incus-container" && pool.Warm.MaxReady != 0 {
			add(prefix+".warm.max_ready", "container backend warm capacity requires a separate completed soak")
		}
		if _, exists := seenNames[pool.Name]; exists {
			add(prefix+".name", "must be unique")
		}
		seenNames[pool.Name] = struct{}{}
		// GitHub scopes a scale set name to one forge entity, so the same
		// class name under two tenants is two scale sets, not a collision.
		// Keying this by the name alone refused a second tenant on a host
		// that had room for it, which is a policy the forge does not have.
		scaleSetKey := pool.TenantID() + "\x00" + pool.ScaleSetName
		if _, exists := seenScaleSets[scaleSetKey]; exists {
			add(prefix+".scale_set_name", "must be unique for its tenant")
		}
		seenScaleSets[scaleSetKey] = struct{}{}
		maxPoolCPU = max(maxPoolCPU, pool.Resources.VCPU)
		maxPoolMemory = max(maxPoolMemory, pool.Resources.MemoryMiB)
		maxPoolDisk = max(maxPoolDisk, pool.Resources.DiskGiB)
	}
	if c.Incus.ProjectMaxCPUUnits < maxPoolCPU {
		add("incus.project_max_cpu_units", "must fit the largest pool CPU request")
	}
	if c.Incus.ProjectMaxMemoryMiB < maxPoolMemory {
		add("incus.project_max_memory_mib", "must fit the largest pool memory request")
	}
	if c.Incus.ProjectDiskLimitGiB < maxPoolDisk {
		add("incus.project_disk_limit_gib", "must fit the largest pool disk request")
	}
	if c.Incus.ProjectHardMemoryLimitMiB != 0 &&
		(c.Incus.ProjectHardMemoryLimitMiB < c.Incus.ProjectMaxMemoryMiB || c.Incus.ProjectHardMemoryLimitMiB > c.Incus.ProjectMaxInstances*maxPoolMemory) {
		add("incus.project_hard_memory_limit_mib", "must be between scheduling capacity and the absolute per-member instance envelope")
	}
	if c.Incus.ProjectHardDiskLimitGiB != 0 &&
		(c.Incus.ProjectHardDiskLimitGiB < c.Incus.ProjectDiskLimitGiB || c.Incus.ProjectHardDiskLimitGiB > c.Incus.ProjectMaxInstances*maxPoolDisk) {
		add("incus.project_hard_disk_limit_gib", "must be between scheduling capacity and the absolute per-member instance envelope")
	}

	if len(issues) == 0 {
		return nil
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Path == issues[j].Path {
			return issues[i].Message < issues[j].Message
		}
		return issues[i].Path < issues[j].Path
	})
	return &ValidationError{Issues: issues}
}

func validateBackend(add func(string, string), prefix string, backend Backend, host string) {
	if !namePattern.MatchString(backend.Name) {
		add(prefix+".name", "must be a lowercase DNS-style name")
	}
	if backend.Platform != "linux" {
		add(prefix+".platform", "must be linux until another platform backend is approved")
	}
	if backend.Architecture != "amd64" {
		add(prefix+".architecture", "must be amd64 until another architecture backend is approved")
	}
	if backend.Implementation != "incus-vm" && backend.Implementation != "incus-container" {
		add(prefix+".implementation", "must be incus-vm or incus-container for an approved Linux backend")
	}
	if !namePattern.MatchString(backend.FailureDomain) {
		add(prefix+".failure_domain", "must be a lowercase DNS-style name")
	} else if backend.FailureDomain != host {
		add(prefix+".failure_domain", "must match platform.host for a single-host backend")
	}
}

func validateIncus(add func(string, string), incus Incus) {
	validateVersion(add, "incus.version", incus.Version)
	if incus.Version != "v6.0.6" {
		add("incus.version", "must remain pinned to the fleet Incus 6.0.6 LTS baseline")
	}
	address, err := netip.ParseAddrPort(incus.APIAddress)
	if err != nil || !address.Addr().IsLoopback() || address.Port() != 8443 {
		add("incus.api_address", "must be the loopback TLS endpoint 127.0.0.1:8443")
	}
	validateCluster(add, incus.Cluster)
	for path, value := range map[string]string{
		"incus.project":      incus.Project,
		"incus.storage_pool": incus.StoragePool,
		"incus.network":      incus.Network,
		"incus.egress_acl":   incus.EgressACL,
	} {
		if !namePattern.MatchString(value) {
			add(path, "must be a lowercase DNS-style name")
		}
	}
	if incus.StorageDriver != "lvm" {
		add("incus.storage_driver", "must be lvm for the bounded loop-backed pilot pool")
	}
	if incus.StorageSizeGiB < 80 || incus.StorageSizeGiB > 200 {
		add("incus.storage_size_gib", "must be between 80 and 200 GiB")
	}
	if incus.ProjectDiskLimitGiB < 40 || incus.ProjectDiskLimitGiB >= incus.StorageSizeGiB {
		add("incus.project_disk_limit_gib", "must be at least 40 GiB and smaller than the storage pool")
	}
	prefix, err := netip.ParsePrefix(incus.NetworkCIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() != 24 || prefix.Addr().As4()[3] != 1 {
		add("incus.network_cidr", "must be an isolated IPv4 /24 whose declared gateway is .1")
	}
	publicAddress, err := netip.ParseAddr(incus.PublicHostAddress)
	if err != nil || !publicAddress.Is4() || !publicAddress.IsGlobalUnicast() || publicAddress.IsPrivate() {
		add("incus.public_host_address", "must be the host public IPv4 address")
	}
	servicesAddress, err := netip.ParseAddr(incus.ServicesHostAddress)
	if err != nil || !servicesAddress.Is4() || !servicesAddress.IsGlobalUnicast() || !servicesAddress.IsPrivate() {
		add("incus.services_host_address", "must be the private unicast IPv4 address of the queue/services host")
	}
	if incus.Cluster.Enabled {
		if len(incus.EstatePublicHostAddresses) != incus.Cluster.Members+1 {
			add("incus.estate_public_host_addresses", "must contain every cluster member plus the queue host")
		}
		if !sort.StringsAreSorted(incus.EstatePublicHostAddresses) {
			add("incus.estate_public_host_addresses", "must be strictly sorted")
		}
		seen := make(map[string]struct{}, len(incus.EstatePublicHostAddresses))
		for index, value := range incus.EstatePublicHostAddresses {
			address, parseErr := netip.ParseAddr(value)
			if parseErr != nil || !address.Is4() || !address.IsGlobalUnicast() || address.IsPrivate() {
				add(fmt.Sprintf("incus.estate_public_host_addresses[%d]", index), "must be a public IPv4 address")
			}
			if _, duplicate := seen[value]; duplicate {
				add("incus.estate_public_host_addresses", "must be unique")
			}
			seen[value] = struct{}{}
		}
		if _, exists := seen[incus.PublicHostAddress]; !exists {
			add("incus.estate_public_host_addresses", "must include this host public address")
		}
	}
	// These are fleet-wide bootstrap ceilings, not the real limit. Admission
	// enforces that against the host it observes, so a configuration that asks
	// for more than the hardware has is refused at runtime rather than here.
	//
	// Dedicated fleet hosts may expose up to fifteen of sixteen GiB per member
	// to the shared project. Admission, PSI and placement retain the
	// live reserve; the project ceiling prevents a configuration typo from
	// exceeding physical capacity but no longer strands a fixed quarter.
	if incus.ProjectMaxInstances < 1 || incus.ProjectMaxInstances > 8 {
		add("incus.project_max_instances", "must be between one and eight")
	}
	if incus.ProjectMaxCPUUnits < 1 || incus.ProjectMaxCPUUnits > 10 {
		add("incus.project_max_cpu_units", "must be between one and ten logical scheduling units")
	}
	if incus.ProjectMaxMemoryMiB < 4096 || incus.ProjectMaxMemoryMiB > 15*1024 {
		add("incus.project_max_memory_mib", "must be between 4096 and 15360 MiB")
	}
	validateServicePort(add, "incus.registry_port", incus.RegistryPort)
	validateServicePort(add, "incus.rustfs_port", incus.RustFSPort)
	validateServicePort(add, "incus.cache_gateway_port", incus.CacheGatewayPort)
	validateServicePort(add, "incus.garm_gateway_port", incus.GARMGatewayPort)
	ports := map[int]string{}
	for _, service := range []struct {
		path string
		port int
	}{
		{"incus.registry_port", incus.RegistryPort},
		{"incus.rustfs_port", incus.RustFSPort},
		{"incus.cache_gateway_port", incus.CacheGatewayPort},
		{"incus.garm_gateway_port", incus.GARMGatewayPort},
	} {
		if previous, exists := ports[service.port]; exists {
			add(service.path, "must differ from "+previous)
		}
		ports[service.port] = service.path
	}
}

func validateServicePort(add func(string, string), path string, port int) {
	if port < 1024 || port > 65535 {
		add(path, "must be an unprivileged TCP port")
	}
}

func validatePool(add func(string, string), prefix string, pool Pool) {
	if !namePattern.MatchString(pool.Name) {
		add(prefix+".name", "must be a lowercase DNS-style name")
	}
	if !namePattern.MatchString(pool.Backend) {
		add(prefix+".backend", "must be a lowercase DNS-style backend name")
	}
	if !namePattern.MatchString(pool.ScaleSetName) || !strings.HasPrefix(pool.ScaleSetName, "nddev-") {
		add(prefix+".scale_set_name", "must be a unique nddev-* scale set name")
	}
	// A pool that names a tenant the registry does not know would reconcile
	// against an account the fleet has no credential for, so the closed set
	// is enforced here rather than at the first API call.
	if pool.Tenant != "" {
		if _, err := tenant.ByID(pool.Tenant); err != nil {
			add(prefix+".tenant", "must be a known tenant")
		}
	}
	if pool.Resources.VCPU < 1 {
		add(prefix+".resources.vcpu", "must be positive")
	}
	if pool.Resources.MemoryMiB < 1024 || pool.Resources.MemoryMiB%256 != 0 {
		add(prefix+".resources.memory_mib", "must be at least 1024 and divisible by 256")
	}
	if pool.Resources.DiskGiB < 10 || pool.Resources.DiskGiB > 100 {
		add(prefix+".resources.disk_gib", "must be between 10 and 100 GiB")
	}
	reservation := pool.EffectiveReservation()
	if reservation.CPUUnits < 1 || reservation.CPUUnits > pool.Resources.VCPU {
		add(prefix+".reservation.cpu_units", "must be positive and no greater than the hard vCPU limit")
	}
	if reservation.MemoryMiB < 256 || reservation.MemoryMiB%256 != 0 ||
		reservation.MemoryMiB > pool.Resources.MemoryMiB {
		add(prefix+".reservation.memory_mib", "must be at least 256 MiB, divisible by 256 and no greater than the hard memory limit")
	}
	if pool.MaxRunning < 1 {
		add(prefix+".max_running", "must be positive")
	}
	if pool.Warm.TargetReady < 0 || pool.Warm.MaxReady < 0 {
		add(prefix+".warm", "values cannot be negative")
	}
	if pool.Warm.TargetReady > pool.Warm.MaxReady {
		add(prefix+".warm.target_ready", "cannot exceed max_ready")
	}
	if pool.Warm.MaxReady > pool.MaxRunning {
		add(prefix+".warm.max_ready", "cannot exceed max_running")
	}
	if !oneOf(pool.Trust, "trusted", "untrusted", "release") {
		add(prefix+".trust", "must be trusted, untrusted, or release")
	}
	if !oneOf(pool.Capabilities.Credentials, "none", "repository", "oidc-only") {
		add(prefix+".capabilities.credentials", "must be none, repository, or oidc-only")
	}
	if !oneOf(pool.Capabilities.NetworkPolicy, "github-cache-only", "public-internet", "release-allowlist") {
		add(prefix+".capabilities.network_policy", "uses an unsupported network policy")
	}
	if !oneOf(pool.Capabilities.CacheWriteScope, "none", "isolated", "trusted") {
		add(prefix+".capabilities.cache_write_scope", "must be none, isolated, or trusted")
	}

	if pool.Trust == "untrusted" {
		if pool.Capabilities.Credentials != "none" {
			add(prefix+".capabilities.credentials", "untrusted pools cannot receive credentials")
		}
		if pool.Capabilities.CacheWriteScope != "isolated" && pool.Capabilities.CacheWriteScope != "none" {
			add(prefix+".capabilities.cache_write_scope", "untrusted pools need isolated or disabled writes")
		}
		if pool.Capabilities.NetworkPolicy != "public-internet" {
			add(prefix+".capabilities.network_policy", "untrusted pools require public-internet isolation")
		}
	}
	if pool.Trust == "release" {
		if pool.Capabilities.Credentials != "oidc-only" {
			add(prefix+".capabilities.credentials", "release pools require oidc-only credentials")
		}
		if pool.Capabilities.CacheWriteScope != "none" {
			add(prefix+".capabilities.cache_write_scope", "release pools cannot write shared caches")
		}
		if pool.Capabilities.NetworkPolicy != "release-allowlist" {
			add(prefix+".capabilities.network_policy", "release pools require an explicit allowlist")
		}
		if pool.Warm.TargetReady != 0 || pool.Warm.MaxReady != 0 {
			add(prefix+".warm", "release pools must stay cold")
		}
	}
	validateEgressAllowlist(add, prefix, pool)
}

// validateEgressAllowlist refuses anything the egress ACL would then have to
// argue with. A declared destination must be one exact public unicast address,
// so it can never overlap a range the ACL rejects and the two can never depend
// on which rule an implementation evaluates first.
func validateEgressAllowlist(add func(string, string), prefix string, pool Pool) {
	field := prefix + ".capabilities.egress_allowlist"
	if pool.Capabilities.NetworkPolicy != "release-allowlist" {
		if len(pool.Capabilities.EgressAllowlist) > 0 {
			add(field, "only a release-allowlist pool may declare egress destinations")
		}
		return
	}
	// An empty allowlist is legal and leaves only the explicitly rendered local
	// DNS, worker-gateway and read-only cache endpoints. That is the honest state
	// of a release pool nobody has yet given a public destination; requiring one
	// here would only invite an invented address to satisfy a validator.
	if len(pool.Capabilities.EgressAllowlist) == 0 {
		return
	}
	reserved := make([]netip.Prefix, 0, len(ReservedEgressDestinations()))
	for _, text := range ReservedEgressDestinations() {
		prefix, err := netip.ParsePrefix(text)
		if err != nil {
			add(field, "reserved destination table is unparseable")
			return
		}
		reserved = append(reserved, prefix)
	}
	seen := make(map[string]struct{}, len(pool.Capabilities.EgressAllowlist))
	for index, destination := range pool.Capabilities.EgressAllowlist {
		entry := fmt.Sprintf("%s[%d]", field, index)
		validateEgressDestination(add, entry, destination, reserved, seen)
	}
}

func validateEgressDestination(
	add func(string, string),
	entry string,
	destination EgressDestination,
	reserved []netip.Prefix,
	seen map[string]struct{},
) {
	if strings.TrimSpace(destination.Purpose) == "" {
		add(entry+".purpose", "must state why this destination is reachable")
	}
	if destination.Protocol != "tcp" {
		add(entry+".protocol", "must be tcp")
	}
	network, err := netip.ParsePrefix(destination.Destination)
	switch {
	case err != nil:
		add(entry+".destination", "must be one exact IPv4 address in CIDR form")
		return
	case !network.Addr().Is4() || network.Bits() != 32:
		// A range would let one reviewed address become a subnet later without
		// the review that the address itself received.
		add(entry+".destination", "must be a single IPv4 /32")
		return
	case network.Masked() != network:
		add(entry+".destination", "must be the network address of its prefix")
		return
	}
	for _, blocked := range reserved {
		if blocked.Overlaps(network) {
			add(entry+".destination", "falls inside a range every pool is denied: "+blocked.String())
			return
		}
	}
	if _, duplicate := seen[network.String()]; duplicate {
		add(entry+".destination", "is declared more than once")
		return
	}
	seen[network.String()] = struct{}{}
	validateEgressPorts(add, entry, destination.Ports)
}

func validateEgressPorts(add func(string, string), entry, ports string) {
	if ports == "" {
		add(entry+".ports", "must name at least one port")
		return
	}
	for _, port := range strings.Split(ports, ",") {
		// A range is refused for the same reason a subnet is: the reviewed
		// thing must be the thing that is opened.
		number, err := strconv.Atoi(port)
		if err != nil || strconv.Itoa(number) != port || number < 1 || number > 65535 {
			add(entry+".ports", "must be comma-separated exact ports in 1..65535")
			return
		}
	}
}

func requireEqual(add func(string, string), path, actual, expected string) {
	if actual != expected {
		add(path, "must be "+expected)
	}
}

func validateVersion(add func(string, string), path, value string) {
	if !versionPattern.MatchString(value) {
		add(path, "must be an exact vMAJOR.MINOR.PATCH version")
	}
}

func validateEnv(add func(string, string), path, value string) {
	if !envPattern.MatchString(value) {
		add(path, "must name an environment variable, not contain a secret")
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validateCluster(add func(string, string), cluster Cluster) {
	if !cluster.Enabled {
		if cluster.MemberName != "" || cluster.APIAddress != "" || cluster.Members != 0 {
			add("incus.cluster", "must be empty unless clustering is enabled")
		}
		return
	}
	if cluster.MemberName == "" || strings.ContainsAny(cluster.MemberName, " /") {
		add("incus.cluster.member_name", "must name this Incus cluster member")
	}
	address, err := netip.ParseAddrPort(cluster.APIAddress)
	switch {
	case err != nil || address.Port() != 8443:
		add("incus.cluster.api_address", "must be an address:8443 endpoint")
	case address.Addr().IsLoopback():
		add("incus.cluster.api_address", "must not be loopback; a cluster member's peers have to reach it")
	case !address.Addr().IsPrivate() || !address.Addr().Is4():
		add("incus.cluster.api_address", "must be a private IPv4 address on port 8443")
	}
	// Four hosts is what this fleet has; the bound exists so a typo cannot
	// multiply the project quota into capacity no set of hosts can serve.
	if cluster.Members < 1 || cluster.Members > 8 {
		add("incus.cluster.members", "must be between 1 and 8")
	}
}
