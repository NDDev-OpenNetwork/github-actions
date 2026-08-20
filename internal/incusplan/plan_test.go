package incusplan

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

func TestStandardPilotPlanHasBoundedIsolation(t *testing.T) {
	t.Parallel()

	plan := loadPlan(t, "nddev-linux-standard")
	cfg := loadConfig(t)
	// The host configuration this plan is built from declares cluster
	// membership, so the API address is the member's private endpoint rather
	// than the standalone loopback one. A cluster member cannot serve its
	// peers on loopback, and rewriting it to loopback partitions the member.
	if plan.Version != "v6.0.6" || plan.APIAddress != "10.200.0.5:8443" {
		t.Fatalf("unexpected Incus baseline: %#v", plan)
	}
	if plan.Storage.Driver != "lvm" || plan.Storage.Config["size"] != "200GiB" || plan.Storage.Config["lvm.use_thinpool"] != "true" {
		t.Fatalf("unexpected storage policy: %#v", plan.Storage)
	}
	if plan.Project.Config["restricted"] != "true" || plan.Project.Config["limits.containers"] != "32" || plan.Project.Config["limits.virtual-machines"] != "0" || plan.Project.Config["restricted.containers.lowlevel"] != "block" || plan.Project.Config["restricted.containers.nesting"] != "allow" || plan.Project.Config["restricted.virtual-machines.lowlevel"] != "block" {
		t.Fatalf("project is not restricted: %#v", plan.Project.Config)
	}
	if _, unsupported := plan.Project.Config["restricted.virtual-machines.nesting"]; unsupported {
		t.Fatalf("Incus 6.0 does not support a project-level VM nesting key: %#v", plan.Project.Config)
	}
	// This exact allowlist comes from Incus v6.0.0 project validation at
	// commit 714bcc5e42b189f54025b8567df1f3408a1cae2c. It prevents config keys
	// added in newer documentation from silently entering the LTS plan.
	incus60ProjectKeys := map[string]bool{
		"features.images":                      true,
		"features.networks":                    true,
		"features.networks.zones":              true,
		"features.profiles":                    true,
		"features.storage.buckets":             true,
		"features.storage.volumes":             true,
		"images.auto_update_interval":          true,
		"limits.containers":                    true,
		"limits.disk":                          true,
		"limits.instances":                     true,
		"limits.memory":                        true,
		"limits.networks":                      true,
		"limits.virtual-machines":              true,
		"restricted":                           true,
		"restricted.backups":                   true,
		"restricted.containers.lowlevel":       true,
		"restricted.containers.nesting":        true,
		"restricted.devices.disk":              true,
		"restricted.devices.nic":               true,
		"restricted.networks.access":           true,
		"restricted.snapshots":                 true,
		"restricted.virtual-machines.lowlevel": true,
	}
	for key := range plan.Project.Config {
		if !incus60ProjectKeys[key] {
			t.Errorf("project key %q is not in the Incus 6.0 contract", key)
		}
	}
	for key := range plan.Network.Config {
		if strings.HasPrefix(key, "security.acls") {
			t.Fatalf("bridge must not attach a fleet-wide ACL: %#v", plan.Network.Config)
		}
	}
	if len(plan.Profiles) != 1 || plan.Profiles[0].Name != "nddev-linux-standard" {
		t.Fatalf("unexpected selected profiles: %#v", plan.Profiles)
	}
	profile := plan.Profiles[0]
	if profile.Config["limits.cpu"] != "" || profile.Config["limits.cpu.allowance"] != "200%" || profile.Config["limits.memory"] != "4096MiB" || profile.Config["security.nesting"] != "false" || profile.Devices["root"]["size"] != "30GiB" {
		t.Fatalf("unexpected standard profile resources: %#v", profile)
	}
	if profile.Devices["eth0"]["security.port_isolation"] != "true" || profile.Devices["eth0"]["security.ipv4_filtering"] != "true" || profile.Devices["eth0"]["security.mac_filtering"] != "true" {
		t.Fatalf("profile NIC isolation is incomplete: %#v", profile.Devices["eth0"])
	}
	incus60BridgeNICKeys := map[string]bool{
		"name":                    true,
		"network":                 true,
		"security.ipv4_filtering": true,
		"security.mac_filtering":  true,
		"security.port_isolation": true,
		"security.acls":           true,
		"type":                    true,
	}
	if profile.Devices["eth0"]["security.acls"] != plan.ACL.Name {
		t.Fatalf("standard NIC carries %q, want %q", profile.Devices["eth0"]["security.acls"], plan.ACL.Name)
	}
	for key := range profile.Devices["eth0"] {
		if !incus60BridgeNICKeys[key] {
			t.Errorf("bridged NIC key %q is not in the Incus 6.0 contract", key)
		}
	}

	if len(plan.ACL.Ingress) != 0 || len(plan.ACL.Egress) == 0 {
		t.Fatalf("unexpected ACL policy: ingress=%#v egress=%#v", plan.ACL.Ingress, plan.ACL.Egress)
	}
	if plan.HostFirewall.Backend != "ufw" || plan.HostFirewall.RequiredStatus != "active" || plan.HostFirewall.RequiredDefault != "deny (incoming), allow (outgoing), deny (routed)" {
		t.Fatalf("unsafe host firewall preconditions: %#v", plan.HostFirewall)
	}
	if len(plan.HostFirewall.Rules) != 11 {
		t.Fatalf("unexpected host firewall rules: %#v", plan.HostFirewall.Rules)
	}
	var dhcp, publicHTTP, publicHTTPS, rustfs, servicesRustFS, garmGateway, declaroSSH, almatyStagingSSH bool
	for _, rule := range plan.HostFirewall.Rules {
		command := strings.Join(rule.Args, " ")
		switch rule.Name {
		case "dhcp":
			dhcp = strings.Contains(command, "allow in on gha0 to any port 67 proto udp comment gha-fleet-dhcp-v2")
		case "public-http":
			publicHTTP = strings.Contains(command, "route allow in on gha0 from 198.51.100.0/24 to any port 80 proto tcp")
		case "public-https":
			publicHTTPS = strings.Contains(command, "route allow in on gha0 from 198.51.100.0/24 to any port 443 proto tcp")
		case "rustfs":
			rustfs = strings.Contains(command, "allow in on gha0 to 198.51.100.1 port 9002 proto tcp")
		case "services-rustfs-diagnostics":
			servicesRustFS = strings.Contains(command, "allow in on eth1 from 10.200.0.7 to 198.51.100.1 port 9002 proto tcp")
		case "garm-gateway":
			garmGateway = strings.Contains(command, "allow in on gha0 to 198.51.100.1 port 9443 proto tcp")
		case "release-egress-1", "release-egress-2":
			declaroSSH = declaroSSH || strings.Contains(command, "route allow in on gha0 from 198.51.100.0/24 to 203.0.113.20/32 port 22 proto tcp")
			almatyStagingSSH = almatyStagingSSH || strings.Contains(command, "route allow in on gha0 from 198.51.100.0/24 to 203.0.113.21/32 port 22 proto tcp")
		}
	}
	if !dhcp || !publicHTTP || !publicHTTPS || !rustfs || !servicesRustFS || !garmGateway || !declaroSSH || !almatyStagingSSH {
		t.Fatalf("host firewall invariants missing: %#v", plan.HostFirewall.Rules)
	}

	var privateReject, hostServiceReject, cacheAllow, gatewayAllow bool
	for _, rule := range plan.ACL.Egress {
		switch rule.Description {
		case "Block private, metadata, multicast and host public ranges":
			// The host's own public address is rejected alongside the private
			// ranges, and which address that is comes from the configuration
			// rather than from whichever host this test happened to load.
			privateReject = rule.Action == "reject" && strings.Contains(rule.Destination, "10.0.0.0/8") &&
				strings.Contains(rule.Destination, cfg.Incus.PublicHostAddress+"/32")
			for _, address := range cfg.Incus.EstatePublicHostAddresses {
				if !strings.Contains(rule.Destination, address+"/32") {
					t.Errorf("ACL private reject omits estate host %s: %s", address, rule.Destination)
				}
			}
		case "Block sensitive host bridge services":
			hostServiceReject = rule.Action == "reject" && rule.Destination == "198.51.100.1/32" && strings.Contains(rule.DestinationPort, "8443")
		case "Allow scoped local cache endpoints":
			cacheAllow = rule.Action == "allow" && rule.Destination == "198.51.100.1/32" && rule.DestinationPort == "5001,9002"
		case "Allow the restricted GARM worker gateway":
			gatewayAllow = rule.Action == "allow" && rule.Destination == "198.51.100.1/32" && rule.DestinationPort == "9443"
		}
	}
	if !privateReject || !hostServiceReject || !cacheAllow || !gatewayAllow {
		t.Fatalf("ACL invariants missing: %#v", plan.ACL.Egress)
	}
}

func TestContainerCanaryProfileIsUnprivilegedAndNonNested(t *testing.T) {
	t.Parallel()
	plan := loadPlan(t, "nddev-linux-container-canary")
	if len(plan.Profiles) != 1 {
		t.Fatalf("unexpected profiles: %#v", plan.Profiles)
	}
	profile := plan.Profiles[0]
	if profile.Config["limits.cpu.allowance"] != "200%" || profile.Config["limits.cpu"] != "" || profile.Config["security.idmap.isolated"] != "true" ||
		profile.Config["security.privileged"] != "false" || profile.Config["security.nesting"] != "false" ||
		profile.Config["security.syscalls.intercept.mknod"] != "false" ||
		profile.Config["security.syscalls.intercept.setxattr"] != "false" ||
		profile.Config["security.secureboot"] != "" || profile.Devices["root"]["size"] != "20GiB" {
		t.Fatalf("container profile is not fail-closed: %#v", profile)
	}
}

func TestDockerContainerCanaryProfileHasNestedRuntimeAndSoftCPUWeight(t *testing.T) {
	t.Parallel()
	plan := loadPlan(t, "nddev-linux-docker-container-canary")
	plannedProfile := plan.Profiles[0]
	if plannedProfile.Config["limits.cpu"] != "" || plannedProfile.Config["limits.cpu.allowance"] != "200%" ||
		plannedProfile.Config["security.idmap.isolated"] != "true" ||
		plannedProfile.Config["security.privileged"] != "false" || plannedProfile.Config["security.nesting"] != "true" {
		t.Fatalf("Docker container CPU/isolation contract is incomplete: %#v", plannedProfile.Config)
	}
	cfg := loadConfig(t)
	pool, _ := cfg.Pool("nddev-linux-docker-container-canary")
	pool.Resources.VCPU = 4
	if got := profile(cfg, pool).Config["limits.cpu.allowance"]; got != "400%" {
		t.Fatalf("four-vCPU container allowance = %q", got)
	}
}

func TestUnimplementedNetworkPolicyIsStillRejected(t *testing.T) {
	t.Parallel()

	// The rejection is what stops a pool being planned onto a bridge that does
	// not implement the policy it declares. github-cache-only is still such a
	// policy: no bridge implements it. Release is no longer one -- it is
	// implemented as the pool's own NIC ACL below -- and fast stopped being one
	// when it declared the egress it can actually be given.
	// The fast pool is the one that can carry this mutation: config validation
	// binds a release pool to release-allowlist, so the unimplemented policy
	// has to be tried somewhere it is permitted, and fast is exactly where it
	// used to sit.
	cfg := loadConfig(t)
	for index := range cfg.Pools {
		if cfg.Pools[index].Name == "nddev-linux-fast" {
			cfg.Pools[index].Capabilities.NetworkPolicy = "github-cache-only"
		}
	}
	_, err := Build(cfg, []string{"nddev-linux-fast"})
	if err == nil || !strings.Contains(err.Error(), "cannot use the public-egress pilot bridge") {
		t.Fatalf("expected a network-policy rejection, got %v", err)
	}
	for _, name := range []string{"nddev-linux-fast", "nddev-linux-release"} {
		if _, err := Build(loadConfig(t), []string{name}); err != nil {
			t.Fatalf("pool %q is meant to be plannable now: %v", name, err)
		}
	}
}

// A release pool's destination has to reach that pool and nothing else. Every
// NIC gets exactly one ACL: ordinary pools get public egress while release gets
// its narrower allowlist. The bridge itself carries no fleet-wide ACL.
func TestReleaseAllowlistReachesOnlyTheDeclaringPool(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t)
	for index := range cfg.Pools {
		if cfg.Pools[index].Name != "nddev-linux-release" {
			continue
		}
		cfg.Pools[index].Capabilities.EgressAllowlist = []config.EgressDestination{{
			Destination: "203.0.113.7/32",
			Protocol:    "tcp",
			Ports:       "22",
			Purpose:     "Reach the staging jump host over SSH",
		}}
	}
	plan, err := Build(cfg, []string{"nddev-linux-standard", "nddev-linux-release"})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}

	if len(plan.PoolACLs) != 1 {
		t.Fatalf("expected exactly one pool ACL, got %d", len(plan.PoolACLs))
	}
	acl := plan.PoolACLs[0]
	if acl.Name != "gha-public-egress-nddev-linux-release" {
		t.Fatalf("pool ACL is named %q", acl.Name)
	}
	var reject, publicHTTPS, allow bool
	for _, rule := range acl.Egress {
		if rule.Action == "reject" && strings.Contains(rule.Destination, "169.254.0.0/16") {
			reject = true
		}
		if rule.Action == "allow" && rule.Destination == "203.0.113.7/32" &&
			rule.Protocol == "tcp" && rule.DestinationPort == "22" {
			allow = true
		}
		if rule.Action == "allow" && rule.Destination == "" &&
			rule.Protocol == "tcp" && rule.DestinationPort == "443" {
			publicHTTPS = true
		}
	}
	if !reject || !publicHTTPS || !allow {
		t.Fatalf("pool ACL is not deny-shaped around its allow: %#v", acl.Egress)
	}

	for _, profile := range plan.Profiles {
		attached := profile.Devices["eth0"]["security.acls"]
		switch profile.Name {
		case "nddev-linux-release":
			if attached != acl.Name {
				t.Fatalf("release NIC carries %q, want %q", attached, acl.Name)
			}
		default:
			if attached != plan.ACL.Name {
				t.Fatalf("pool %q carries %q, want public ACL %q", profile.Name, attached, plan.ACL.Name)
			}
		}
	}

	// The shared bridge ACL must be exactly what it was: this widened one pool.
	base := loadPlan(t, "nddev-linux-standard")
	if !slices.Equal(base.ACL.Egress, plan.ACL.Egress) {
		t.Fatal("declaring a release destination changed the egress every pool shares")
	}
}

func TestDockerIntegrationProfileIsExplicitAndBounded(t *testing.T) {
	t.Parallel()

	plan := loadPlan(t, "nddev-linux-standard", "nddev-linux-integration")
	if len(plan.Profiles) != 2 {
		t.Fatalf("profile count = %d, want 2", len(plan.Profiles))
	}
	var integration *Profile
	for index := range plan.Profiles {
		if plan.Profiles[index].Name == "nddev-linux-integration" {
			integration = &plan.Profiles[index]
		}
	}
	if integration == nil {
		t.Fatal("integration profile is absent")
	}
	if integration.Config["limits.cpu"] != "" || integration.Config["limits.cpu.allowance"] != "400%" ||
		integration.Config["limits.memory"] != "6144MiB" || integration.Config["security.idmap.isolated"] != "true" ||
		integration.Config["security.nesting"] != "true" || integration.Config["security.privileged"] != "false" {
		t.Fatalf("unexpected integration limits: %#v", integration.Config)
	}
	if integration.Devices["root"]["size"] != "50GiB" || integration.Devices["root"]["pool"] != "gha-lvm" {
		t.Fatalf("unexpected integration root device: %#v", integration.Devices["root"])
	}
}

func TestUnknownAndDuplicatePoolsAreRejected(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t)
	if _, err := Build(cfg, []string{"missing"}); err == nil || !strings.Contains(err.Error(), "unknown pools") {
		t.Fatalf("expected unknown-pool rejection, got %v", err)
	}
	if _, err := Build(cfg, []string{"nddev-linux-standard", "nddev-linux-standard"}); err == nil || !strings.Contains(err.Error(), "selected more than once") {
		t.Fatalf("expected duplicate-pool rejection, got %v", err)
	}
}

func loadPlan(t *testing.T, pools ...string) Plan {
	t.Helper()
	plan, err := Build(loadConfig(t), pools)
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return plan
}

func loadConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

// A pool that declares warm capacity is a pool somebody expects to run. If the
// planner cannot build it, that expectation is silently false: the fast pool
// asked for github-cache-only, no bridge implements it, and the pool sat at
// zero warm everywhere for so long that the zero looked like caution.
//
// A pool with no warm capacity may still name a policy nothing implements yet.
// That is an intent, and release does exactly this while it waits for its
// allowlist. The difference this test draws is between an intent and a claim.
func TestEveryPoolWithWarmCapacityCanBePlanned(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"../../config/example-runner-1.yaml",
		"../../config/example-runner-1.yaml",
		"../../config/example-runner-2.yaml",
		"../../config/example-runner-3.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			t.Parallel()
			loaded, err := config.Load(path)
			if err != nil {
				t.Fatal(err)
			}
			planned := 0
			for _, pool := range loaded.Pools {
				if pool.Warm.MaxReady == 0 {
					continue
				}
				if _, err := Build(loaded, []string{pool.Name}); err != nil {
					t.Errorf("pool %q declares warm capacity %d but cannot be planned: %v",
						pool.Name, pool.Warm.MaxReady, err)
				}
				planned++
			}
			if planned == 0 {
				t.Skip("host declares no warm capacity")
			}
		})
	}
}

// The project quota Incus enforces in a cluster is one fleet-wide total, not a
// per-member share, so the plan multiplies the declared per-host ceiling by the
// member count. Getting this wrong in either direction is a live defect: too
// low and three of four hosts sit idle behind a quota sized for one; too high
// and the quota stops being a ceiling at all.
func TestClusterProjectQuotaIsThePerHostCeilingTimesTheMembers(t *testing.T) {
	t.Parallel()

	cfg := loadConfig(t)
	if !cfg.Incus.Cluster.Enabled {
		t.Skip("host is not declared as a cluster member")
	}
	plan, err := Build(cfg, []string{"nddev-linux-standard"})
	if err != nil {
		t.Fatal(err)
	}
	members := cfg.Incus.ClusterMembers()
	for key, want := range map[string]string{
		"limits.instances":        strconv.Itoa(cfg.Incus.ProjectMaxInstances * members),
		"limits.virtual-machines": "0",
		"limits.memory":           strconv.Itoa(cfg.Incus.ProjectMaxMemoryMiB*members) + "MiB",
		"limits.disk":             strconv.Itoa(cfg.Incus.ProjectDiskLimitGiB*members) + "GiB",
	} {
		if got := plan.Project.Config[key]; got != want {
			t.Errorf("%s = %q across %d members, want %q", key, got, members, want)
		}
	}
	if _, pinned := plan.Project.Config["limits.cpu"]; pinned {
		t.Fatal("container-only project must not enforce aggregate configured cpusets")
	}
}
