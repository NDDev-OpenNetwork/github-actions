package incusplan

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
)

type Plan struct {
	Version      string       `json:"version" yaml:"version"`
	APIAddress   string       `json:"api_address" yaml:"api_address"`
	HostFirewall HostFirewall `json:"host_firewall" yaml:"host_firewall"`
	Storage      Storage      `json:"storage" yaml:"storage"`
	Network      Network      `json:"network" yaml:"network"`
	Project      Project      `json:"project" yaml:"project"`
	ACL          ACL          `json:"acl" yaml:"acl"`
	// PoolACLs are the additional ACLs a pool receives on its own NIC. The
	// bridge ACL above is what every pool shares; anything wider than that is
	// declared by exactly one pool and reaches only that pool's workers.
	PoolACLs []ACL     `json:"pool_acls" yaml:"pool_acls"`
	Profiles []Profile `json:"profiles" yaml:"profiles"`
}

type HostFirewall struct {
	Backend         string             `json:"backend" yaml:"backend"`
	RequiredStatus  string             `json:"required_status" yaml:"required_status"`
	RequiredDefault string             `json:"required_default" yaml:"required_default"`
	Rules           []HostFirewallRule `json:"rules" yaml:"rules"`
}

type HostFirewallRule struct {
	Name string   `json:"name" yaml:"name"`
	Args []string `json:"args" yaml:"args"`
}

type Storage struct {
	Name   string            `json:"name" yaml:"name"`
	Driver string            `json:"driver" yaml:"driver"`
	Config map[string]string `json:"config" yaml:"config"`
}

type Network struct {
	Name   string            `json:"name" yaml:"name"`
	Type   string            `json:"type" yaml:"type"`
	Config map[string]string `json:"config" yaml:"config"`
}

type Project struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description" yaml:"description"`
	Config      map[string]string `json:"config" yaml:"config"`
}

type ACL struct {
	Name        string            `json:"name" yaml:"name"`
	Description string            `json:"description" yaml:"description"`
	Config      map[string]string `json:"config" yaml:"config"`
	Ingress     []ACLRule         `json:"ingress" yaml:"ingress"`
	Egress      []ACLRule         `json:"egress" yaml:"egress"`
}

type ACLRule struct {
	Action          string `json:"action" yaml:"action"`
	State           string `json:"state" yaml:"state"`
	Description     string `json:"description" yaml:"description"`
	Source          string `json:"source,omitempty" yaml:"source,omitempty"`
	Destination     string `json:"destination,omitempty" yaml:"destination,omitempty"`
	Protocol        string `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	SourcePort      string `json:"source_port,omitempty" yaml:"source_port,omitempty"`
	DestinationPort string `json:"destination_port,omitempty" yaml:"destination_port,omitempty"`
}

type Profile struct {
	Name        string                       `json:"name" yaml:"name"`
	Description string                       `json:"description" yaml:"description"`
	Config      map[string]string            `json:"config" yaml:"config"`
	Devices     map[string]map[string]string `json:"devices" yaml:"devices"`
}

// Build derives every Incus object from the versioned platform policy. The
// selected pools are explicit so a cold pilot cannot accidentally materialize
// a release or otherwise unrequested profile.
func Build(cfg config.Config, selectedPools []string) (Plan, error) {
	if err := cfg.Validate(); err != nil {
		return Plan{}, err
	}
	if len(selectedPools) == 0 {
		return Plan{}, fmt.Errorf("at least one pool must be selected")
	}
	selected := make(map[string]struct{}, len(selectedPools))
	for _, name := range selectedPools {
		if _, duplicate := selected[name]; duplicate {
			return Plan{}, fmt.Errorf("pool %q is selected more than once", name)
		}
		selected[name] = struct{}{}
	}

	bridgePrefix, err := netip.ParsePrefix(cfg.Incus.NetworkCIDR)
	if err != nil {
		return Plan{}, fmt.Errorf("parse Incus network: %w", err)
	}
	bridgeAddress := bridgePrefix.Addr().String()
	bridgeSubnet := bridgePrefix.Masked().String()
	profiles := make([]Profile, 0, len(selected))
	poolACLs := make([]ACL, 0, len(selected))
	for _, pool := range cfg.Pools {
		if _, wanted := selected[pool.Name]; !wanted {
			continue
		}
		switch pool.Capabilities.NetworkPolicy {
		case "public-internet":
		case "release-allowlist":
			// A release pool reaches the same bounded public egress every pool
			// gets, plus the destinations it declares -- on its own NIC, so
			// opening one does not open it for the other pools on this bridge.
			if acl, declared := poolEgressACL(cfg, pool, bridgeAddress); declared {
				poolACLs = append(poolACLs, acl)
			}
		default:
			return Plan{}, fmt.Errorf("pool %q requires %q networking and cannot use the public-egress pilot bridge", pool.Name, pool.Capabilities.NetworkPolicy)
		}
		profiles = append(profiles, profile(cfg, pool))
		delete(selected, pool.Name)
	}
	if len(selected) > 0 {
		missing := make([]string, 0, len(selected))
		for name := range selected {
			missing = append(missing, name)
		}
		slices.Sort(missing)
		return Plan{}, fmt.Errorf("unknown pools: %v", missing)
	}
	containerLimit := "0"
	virtualMachineLimit := "0"
	virtualMachineLowLevel := "block"
	for _, backend := range cfg.Backends {
		if backend.Implementation == "incus-container" {
			containerLimit = strconv.Itoa(cfg.Incus.FleetMaxInstances())
		}
		if backend.Implementation == "incus-vm" {
			virtualMachineLimit = strconv.Itoa(cfg.Incus.FleetMaxInstances())
			virtualMachineLowLevel = "allow"
		}
	}

	return Plan{
		Version:      cfg.Incus.Version,
		APIAddress:   cfg.Incus.EffectiveAPIAddress(),
		HostFirewall: hostFirewall(cfg, bridgeAddress, bridgeSubnet),
		Storage: Storage{
			Name:   cfg.Incus.StoragePool,
			Driver: cfg.Incus.StorageDriver,
			Config: map[string]string{
				"lvm.thinpool_name": "gha-thin",
				"lvm.use_thinpool":  "true",
				"size":              strconv.Itoa(cfg.Incus.StorageSizeGiB) + "GiB",
			},
		},
		Network: Network{
			Name: cfg.Incus.Network,
			Type: "bridge",
			Config: map[string]string{
				"dns.domain":   "gha.internal",
				"dns.mode":     "managed",
				"ipv4.address": cfg.Incus.NetworkCIDR,
				"ipv4.dhcp":    "true",
				"ipv4.nat":     "true",
				"ipv6.address": "none",
			},
		},
		Project: Project{
			Name:        cfg.Incus.Project,
			Description: "Disposable NDDev GitHub Actions workers",
			Config: map[string]string{
				"features.images":                "true",
				"features.networks":              "false",
				"features.networks.zones":        "false",
				"features.profiles":              "true",
				"features.storage.buckets":       "false",
				"features.storage.volumes":       "true",
				"images.auto_update_interval":    "0",
				"limits.containers":              containerLimit,
				"limits.disk":                    strconv.Itoa(cfg.Incus.FleetDiskLimitGiB()) + "GiB",
				"limits.instances":               strconv.Itoa(cfg.Incus.FleetMaxInstances()),
				"limits.memory":                  strconv.Itoa(cfg.Incus.FleetMaxMemoryMiB()) + "MiB",
				"limits.networks":                "0",
				"limits.virtual-machines":        virtualMachineLimit,
				"restricted":                     "true",
				"restricted.backups":             "block",
				"restricted.containers.lowlevel": "block",
				// Allow only the high-level security.nesting switch. Low-level
				// raw LXC, mount and syscall-intercept options remain blocked, so a
				// Docker canary cannot escape the reviewed unprivileged profile.
				"restricted.containers.nesting": "allow",
				"restricted.devices.disk":       "managed",
				"restricted.devices.nic":        "managed",
				"restricted.networks.access":    cfg.Incus.Network,
				"restricted.snapshots":          "block",
				// The desired fleet is container-only. Keep this blocked and the
				// VM count at zero unless a future reviewed configuration declares
				// an explicit incus-vm backend.
				"restricted.virtual-machines.lowlevel": virtualMachineLowLevel,
			},
		},
		ACL:      publicEgressACL(cfg, bridgeAddress),
		PoolACLs: poolACLs,
		Profiles: profiles,
	}, nil
}

func profile(cfg config.Config, pool config.Pool) Profile {
	nic := map[string]string{
		"name":                    "eth0",
		"network":                 cfg.Incus.Network,
		"security.ipv4_filtering": "true",
		"security.mac_filtering":  "true",
		"security.port_isolation": "true",
		"type":                    "nic",
	}
	// Apply exactly one egress policy to each NIC. Keeping the public ACL off
	// the bridge is what makes a release allowlist narrower than ordinary jobs.
	if pool.Capabilities.NetworkPolicy == "release-allowlist" {
		nic["security.acls"] = cfg.Incus.EgressACL + "-" + pool.Name
	} else {
		nic["security.acls"] = cfg.Incus.EgressACL
	}
	backend, _ := cfg.Backend(pool.Backend)
	description := "Managed full-VM profile for " + pool.Name
	instanceConfig := map[string]string{
		"boot.autostart":       "false",
		"limits.cpu":           strconv.Itoa(pool.Resources.VCPU),
		"limits.memory":        strconv.Itoa(pool.Resources.MemoryMiB) + "MiB",
		"security.nesting":     "false",
		"security.secureboot":  "true",
		"user.nddev.pool":      pool.Name,
		"user.nddev.scale_set": pool.ScaleSetName,
		"user.nddev.trust":     pool.Trust,
	}
	if backend.Implementation == "incus-container" {
		description = "Managed unprivileged system-container profile for " + pool.Name
		delete(instanceConfig, "security.secureboot")
		// A numeric limits.cpu creates a cpuset. Two workers then own disjoint
		// cores and cannot borrow an idle sibling's CPUs. Containers expose the
		// whole host set instead; percentage allowance compiles to cpu.weight,
		// not cpu.max, and therefore preserves the requested 2:4 relative shape
		// while remaining work-conserving. The provider ledger and live-load
		// placement retain the bounded admission contract.
		delete(instanceConfig, "limits.cpu")
		instanceConfig["limits.cpu.allowance"] = fmt.Sprintf("%d%%", pool.Resources.VCPU*100)
		instanceConfig["security.idmap.isolated"] = "true"
		instanceConfig["security.privileged"] = "false"
		instanceConfig["security.nesting"] = strconv.FormatBool(pool.Capabilities.Docker)
		instanceConfig["security.syscalls.intercept.mknod"] = "false"
		instanceConfig["security.syscalls.intercept.setxattr"] = "false"
	}
	return Profile{
		Name:        pool.Name,
		Description: description,
		Config:      instanceConfig,
		Devices: map[string]map[string]string{
			"eth0": nic,
			"root": {
				"path": "/",
				"pool": cfg.Incus.StoragePool,
				"size": strconv.Itoa(pool.Resources.DiskGiB) + "GiB",
				"type": "disk",
			},
		},
	}
}

// reservedDestinations is the range set no worker may reach, taken from the one
// declaration config validation refuses pool allowlists against, plus this
// host's own public address. Reading them from the same place is what lets a
// declared destination be proven not to overlap a rejected one.
func reservedDestinations(cfg config.Config) []string {
	hosts := cfg.Incus.EstatePublicHostAddresses
	if len(hosts) == 0 {
		hosts = []string{cfg.Incus.PublicHostAddress}
	}
	result := append([]string{}, config.ReservedEgressDestinations()...)
	for _, host := range hosts {
		result = append(result, host+"/32")
	}
	return result
}

// poolEgressACL renders one pool's declared destinations as an ACL for its own
// NIC. It repeats the common rejects, permits public TCP/443 required by the
// GitHub Actions broker and OIDC endpoints, and then adds local services plus
// explicit deployment destinations. Private, metadata and fleet-host ranges
// are rejected first; all other unmatched egress hits Incus' default reject.
func poolEgressACL(cfg config.Config, pool config.Pool, bridgeAddress string) (ACL, bool) {
	if pool.Capabilities.NetworkPolicy != "release-allowlist" {
		return ACL{}, false
	}
	egress := append(baseEgressRejects(cfg, bridgeAddress), ACLRule{
		Action:          "allow",
		State:           "enabled",
		Description:     "Allow public HTTPS for GitHub Actions control plane and OIDC",
		Protocol:        "tcp",
		DestinationPort: "443",
	})
	egress = append(egress, localServiceAllows(cfg, bridgeAddress, false)...)
	for _, destination := range pool.Capabilities.EgressAllowlist {
		egress = append(egress, ACLRule{
			Action:          "allow",
			State:           "enabled",
			Description:     destination.Purpose,
			Destination:     destination.Destination,
			Protocol:        destination.Protocol,
			DestinationPort: destination.Ports,
		})
	}
	return ACL{
		Name:        cfg.Incus.EgressACL + "-" + pool.Name,
		Description: "Reviewed release egress for " + pool.Name,
		Config:      map[string]string{"user.nddev.policy": "release-allowlist-v1"},
		Ingress:     []ACLRule{},
		Egress:      egress,
	}, true
}

func localServiceAllows(cfg config.Config, bridgeAddress string, registry bool) []ACLRule {
	cachePorts := strconv.Itoa(cfg.Incus.RustFSPort)
	if registry {
		cachePorts = strconv.Itoa(cfg.Incus.RegistryPort) + "," + cachePorts
	}
	return []ACLRule{
		{Action: "allow", State: "enabled", Description: "Allow scoped local cache endpoints", Destination: bridgeAddress + "/32", Protocol: "tcp", DestinationPort: cachePorts},
		{Action: "allow", State: "enabled", Description: "Allow the restricted GARM worker gateway", Destination: bridgeAddress + "/32", Protocol: "tcp", DestinationPort: strconv.Itoa(cfg.Incus.GARMGatewayPort)},
		{Action: "allow", State: "enabled", Description: "Allow DNS to the managed bridge", Destination: bridgeAddress + "/32", Protocol: "udp", DestinationPort: "53"},
		{Action: "allow", State: "enabled", Description: "Allow TCP DNS fallback to the managed bridge", Destination: bridgeAddress + "/32", Protocol: "tcp", DestinationPort: "53"},
	}
}

func baseEgressRejects(cfg config.Config, bridgeAddress string) []ACLRule {
	return []ACLRule{
		{
			Action:      "reject",
			State:       "enabled",
			Description: "Block private, metadata, multicast and host public ranges",
			Destination: join(reservedDestinations(cfg)),
		},
		{
			Action:          "reject",
			State:           "enabled",
			Description:     "Block sensitive host bridge services",
			Destination:     bridgeAddress + "/32",
			Protocol:        "tcp",
			DestinationPort: "22,80,443,8443,8444,9003",
		},
	}
}

func publicEgressACL(cfg config.Config, bridgeAddress string) ACL {
	rejectDestinations := reservedDestinations(cfg)
	return ACL{
		Name:        cfg.Incus.EgressACL,
		Description: "Deny host and private networks; allow bounded public pilot egress",
		Config:      map[string]string{"user.nddev.policy": "public-internet-v1"},
		// Incus installs non-overridable baseline DHCP/DNS service rules on
		// managed bridges before the custom ACL chain. Host UFW policy is
		// reconciled separately because an accept in one nftables base chain
		// cannot override a drop in another one.
		Ingress: []ACLRule{},
		Egress: append([]ACLRule{
			{
				Action:      "reject",
				State:       "enabled",
				Description: "Block private, metadata, multicast and host public ranges",
				Destination: join(rejectDestinations),
			},
			{
				Action:          "reject",
				State:           "enabled",
				Description:     "Block sensitive host bridge services",
				Destination:     bridgeAddress + "/32",
				Protocol:        "tcp",
				DestinationPort: "22,80,443,8443,8444,9003",
			},
			{
				Action:          "allow",
				State:           "enabled",
				Description:     "Allow public HTTP and HTTPS for the standard pilot",
				Protocol:        "tcp",
				DestinationPort: "80,443",
			},
		}, localServiceAllows(cfg, bridgeAddress, true)...),
	}
}

func hostFirewall(cfg config.Config, bridgeAddress, bridgeSubnet string) HostFirewall {
	result := HostFirewall{
		Backend:         "ufw",
		RequiredStatus:  "active",
		RequiredDefault: "deny (incoming), allow (outgoing), deny (routed)",
		Rules: []HostFirewallRule{
			{
				Name: "dhcp",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", "any", "port", "67", "proto", "udp", "comment", "gha-fleet-dhcp-v2"},
			},
			{
				Name: "dns-udp",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", bridgeAddress, "port", "53", "proto", "udp", "comment", "gha-fleet-dns-udp-v1"},
			},
			{
				Name: "dns-tcp",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", bridgeAddress, "port", "53", "proto", "tcp", "comment", "gha-fleet-dns-tcp-v1"},
			},
			{
				Name: "registry",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", bridgeAddress, "port", strconv.Itoa(cfg.Incus.RegistryPort), "proto", "tcp", "comment", "gha-fleet-registry-v1"},
			},
			{
				Name: "rustfs",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", bridgeAddress, "port", strconv.Itoa(cfg.Incus.RustFSPort), "proto", "tcp", "comment", "gha-fleet-rustfs-v1"},
			},
			{
				Name: "services-rustfs-diagnostics",
				Args: []string{"allow", "in", "on", "eth1", "from", "10.200.0.7", "to", bridgeAddress, "port", strconv.Itoa(cfg.Incus.RustFSPort), "proto", "tcp", "comment", "gha-services-rustfs-diagnostics-v1"},
			},
			{
				Name: "garm-gateway",
				Args: []string{"allow", "in", "on", cfg.Incus.Network, "to", bridgeAddress, "port", strconv.Itoa(cfg.Incus.GARMGatewayPort), "proto", "tcp", "comment", "gha-fleet-garm-gateway-v1"},
			},
			{
				Name: "public-http",
				Args: []string{"route", "allow", "in", "on", cfg.Incus.Network, "from", bridgeSubnet, "to", "any", "port", "80", "proto", "tcp", "comment", "gha-fleet-public-http-v1"},
			},
			{
				Name: "public-https",
				Args: []string{"route", "allow", "in", "on", cfg.Incus.Network, "from", bridgeSubnet, "to", "any", "port", "443", "proto", "tcp", "comment", "gha-fleet-public-https-v1"},
			},
		},
	}
	seen := map[string]struct{}{}
	for _, pool := range cfg.Pools {
		if pool.Capabilities.NetworkPolicy != "release-allowlist" {
			continue
		}
		for _, destination := range pool.Capabilities.EgressAllowlist {
			key := destination.Destination + "\x00" + destination.Protocol + "\x00" + destination.Ports
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result.Rules = append(result.Rules, HostFirewallRule{
				Name: fmt.Sprintf("release-egress-%d", len(seen)),
				Args: []string{
					"route", "allow", "in", "on", cfg.Incus.Network,
					"from", bridgeSubnet, "to", destination.Destination,
					"port", destination.Ports, "proto", destination.Protocol,
					"comment", "gha-fleet-release-egress-v1",
				},
			})
		}
	}
	return result
}

func join(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}
