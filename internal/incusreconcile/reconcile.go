package incusreconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strings"

	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplan"
)

type Change struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action"`
}

type Result struct {
	Applied bool     `json:"applied"`
	Changes []Change `json:"changes"`
}

type Reconciler struct {
	Runner Runner
}

type serverState struct {
	APIExtensions []string          `json:"api_extensions"`
	Auth          string            `json:"auth"`
	Config        map[string]string `json:"config"`
	Environment   struct {
		Driver                  string `json:"driver"`
		Server                  string `json:"server"`
		ServerName              string `json:"server_name"`
		ServerClustered         bool   `json:"server_clustered"`
		ServerVersion           string `json:"server_version"`
		StorageSupportedDrivers []struct {
			Name string `json:"Name"`
		} `json:"storage_supported_drivers"`
	} `json:"environment"`
}

type storageState struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Driver      string            `json:"driver"`
	Config      map[string]string `json:"config"`
}

type networkState struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        string            `json:"type"`
	Managed     bool              `json:"managed"`
	Config      map[string]string `json:"config"`
}

type projectState struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Config      map[string]string `json:"config"`
}

type aclState struct {
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Config      map[string]string   `json:"config"`
	Ingress     []incusplan.ACLRule `json:"ingress"`
	Egress      []incusplan.ACLRule `json:"egress"`
}

type profileState struct {
	Name        string                       `json:"name"`
	Description string                       `json:"description"`
	Config      map[string]string            `json:"config"`
	Devices     map[string]map[string]string `json:"devices"`
}

var requiredExtensions = []string{
	"instance_nic_bridged_port_isolation",
	"network_acl",
	"network_bridge_acl",
	"projects_networks_restricted_access",
	"projects_restricted_backups_and_snapshots",
	"projects_restrictions",
	"storage_lvm_use_thinpool",
	"virtual-machines",
}

func (r Reconciler) Apply(ctx context.Context, plan incusplan.Plan) (Result, error) {
	if r.Runner == nil {
		return Result{}, fmt.Errorf("incus runner is required")
	}
	result := Result{Applied: true, Changes: []Change{}}

	server, err := get[serverState](ctx, r.Runner, "/1.0")
	if err != nil {
		return Result{}, fmt.Errorf("inspect Incus server: %w", err)
	}
	if err := verifyServer(server, plan.Version); err != nil {
		return Result{}, err
	}
	if server.Config == nil {
		server.Config = map[string]string{}
	}
	// A cluster member's address is how its peers reach it. Rewriting it to
	// the standalone endpoint partitions the member from the cluster, which is
	// exactly what an earlier run of this reconciler did to gha-runner-4: the
	// daemon came back on 127.0.0.1:8443 and every peer got connection refused.
	// The plan already carries the effective address, so a disagreement here is
	// a configuration error to report rather than an address to overwrite.
	if server.Config["core.https_address"] != plan.APIAddress {
		if server.Environment.ServerClustered {
			return Result{}, fmt.Errorf(
				"clustered member %q is bound to %q but this configuration declares %q; "+
					"change incus.cluster.api_address rather than rebinding a live cluster member",
				server.Environment.ServerName, server.Config["core.https_address"], plan.APIAddress)
		}
		server.Config["core.https_address"] = plan.APIAddress
		if err := put(ctx, r.Runner, "/1.0", map[string]any{"config": server.Config}); err != nil {
			return Result{}, fmt.Errorf("bind Incus API to its declared endpoint: %w", err)
		}
		result.Changes = append(result.Changes, Change{"server", "local", "update"})
	}

	storages, err := get[[]storageState](ctx, r.Runner, "/1.0/storage-pools?recursion=1")
	if err != nil {
		return Result{}, fmt.Errorf("list Incus storage pools: %w", err)
	}
	// In a cluster, source, size, lvm.vg_name and lvm.thinpool_name are
	// member-specific. The recursive list reports the cluster-wide view, which
	// omits them, so comparing against it reads a correctly configured pool as
	// drifted and refuses. Only the single-pool endpoint honours ?target, so
	// the declared pool is re-read for this member before it is compared.
	if server.Environment.ServerClustered {
		if server.Environment.ServerName == "" {
			return Result{}, fmt.Errorf("clustered Incus did not report its member name")
		}
		member, err := get[storageState](ctx, r.Runner,
			"/1.0/storage-pools/"+url.PathEscape(plan.Storage.Name)+"?target="+url.QueryEscape(server.Environment.ServerName))
		if err != nil {
			return Result{}, fmt.Errorf("read storage pool %q on member %q: %w", plan.Storage.Name, server.Environment.ServerName, err)
		}
		for index := range storages {
			if storages[index].Name == plan.Storage.Name {
				storages[index] = member
			}
		}
	}
	if err := r.ensureStorage(ctx, plan.Storage, storages, &result); err != nil {
		return Result{}, err
	}

	acls, err := get[[]aclState](ctx, r.Runner, "/1.0/network-acls?recursion=1")
	if err != nil {
		return Result{}, fmt.Errorf("list Incus network ACLs: %w", err)
	}
	if err := r.ensureACL(ctx, plan.ACL, acls, &result); err != nil {
		return Result{}, err
	}
	// Before the profiles below, which name these by ACL name on a NIC. Incus
	// refuses a NIC that references an ACL that does not exist, so creating
	// them here is what makes a release pool's profile appliable at all.
	for _, acl := range plan.PoolACLs {
		if err := r.ensureACL(ctx, acl, acls, &result); err != nil {
			return Result{}, err
		}
	}

	networks, err := get[[]networkState](ctx, r.Runner, "/1.0/networks?recursion=1")
	if err != nil {
		return Result{}, fmt.Errorf("list Incus networks: %w", err)
	}
	if err := r.ensureNetwork(ctx, plan.Network, networks, &result); err != nil {
		return Result{}, err
	}

	projects, err := get[[]projectState](ctx, r.Runner, "/1.0/projects?recursion=1")
	if err != nil {
		return Result{}, fmt.Errorf("list Incus projects: %w", err)
	}
	if err := r.ensureProject(ctx, plan.Project, projects, &result); err != nil {
		return Result{}, err
	}

	profilesPath := "/1.0/profiles?project=" + url.QueryEscape(plan.Project.Name) + "&recursion=1"
	profiles, err := get[[]profileState](ctx, r.Runner, profilesPath)
	if err != nil {
		return Result{}, fmt.Errorf("list Incus profiles: %w", err)
	}
	if err := r.ensureProfiles(ctx, plan.Project.Name, plan.Profiles, profiles, &result); err != nil {
		return Result{}, err
	}

	return result, nil
}

func verifyServer(server serverState, wantedVersion string) error {
	wantedVersion = strings.TrimPrefix(wantedVersion, "v")
	if server.Auth != "trusted" {
		return fmt.Errorf("incus local API is not trusted")
	}
	if server.Environment.Server != "incus" {
		return fmt.Errorf("expected incus server, got %q", server.Environment.Server)
	}
	if server.Environment.ServerVersion != wantedVersion {
		return fmt.Errorf("incus server version %q does not match pinned %q", server.Environment.ServerVersion, wantedVersion)
	}
	if !strings.Contains(server.Environment.Driver, "qemu") {
		return fmt.Errorf("incus server does not advertise the QEMU VM driver")
	}
	storageDrivers := make([]string, 0, len(server.Environment.StorageSupportedDrivers))
	for _, driver := range server.Environment.StorageSupportedDrivers {
		storageDrivers = append(storageDrivers, driver.Name)
	}
	if !slices.Contains(storageDrivers, "lvm") {
		return fmt.Errorf("incus server does not advertise the LVM storage driver")
	}
	missing := make([]string, 0)
	for _, extension := range requiredExtensions {
		if !slices.Contains(server.APIExtensions, extension) {
			missing = append(missing, extension)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("incus server lacks required API extensions: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (r Reconciler) ensureStorage(ctx context.Context, desired incusplan.Storage, current []storageState, result *Result) error {
	for _, storage := range current {
		if storage.Name != desired.Name {
			continue
		}
		if storage.Driver != desired.Driver {
			return fmt.Errorf("storage pool %q already exists with driver %q, expected %q", desired.Name, storage.Driver, desired.Driver)
		}
		for key, value := range desired.Config {
			if storage.Config[key] != value {
				return fmt.Errorf("storage pool %q config %q is %q, expected %q; refusing in-place storage mutation", desired.Name, key, storage.Config[key], value)
			}
		}
		return nil
	}
	payload := map[string]any{
		"name":        desired.Name,
		"description": "Bounded loop-backed LVM thin pool for the disposable VM pilot",
		"driver":      desired.Driver,
		"config":      desired.Config,
	}
	if err := post(ctx, r.Runner, "/1.0/storage-pools", payload); err != nil {
		return fmt.Errorf("create storage pool %q: %w", desired.Name, err)
	}
	result.Changes = append(result.Changes, Change{"storage-pool", desired.Name, "create"})
	return nil
}

func (r Reconciler) ensureACL(ctx context.Context, desired incusplan.ACL, current []aclState, result *Result) error {
	for _, acl := range current {
		if acl.Name != desired.Name {
			continue
		}
		if acl.Description == desired.Description && reflect.DeepEqual(acl.Config, desired.Config) && slices.Equal(acl.Ingress, desired.Ingress) && slices.Equal(acl.Egress, desired.Egress) {
			return nil
		}
		payload := map[string]any{
			"description": desired.Description,
			"config":      desired.Config,
			"ingress":     desired.Ingress,
			"egress":      desired.Egress,
		}
		path := "/1.0/network-acls/" + url.PathEscape(desired.Name)
		if err := put(ctx, r.Runner, path, payload); err != nil {
			return fmt.Errorf("update network ACL %q: %w", desired.Name, err)
		}
		result.Changes = append(result.Changes, Change{"network-acl", desired.Name, "update"})
		return nil
	}
	payload := map[string]any{
		"name":        desired.Name,
		"description": desired.Description,
		"config":      desired.Config,
		"ingress":     desired.Ingress,
		"egress":      desired.Egress,
	}
	if err := post(ctx, r.Runner, "/1.0/network-acls", payload); err != nil {
		return fmt.Errorf("create network ACL %q: %w", desired.Name, err)
	}
	result.Changes = append(result.Changes, Change{"network-acl", desired.Name, "create"})
	return nil
}

func (r Reconciler) ensureNetwork(ctx context.Context, desired incusplan.Network, current []networkState, result *Result) error {
	for _, network := range current {
		if network.Name != desired.Name {
			continue
		}
		if !network.Managed {
			return fmt.Errorf("network name %q collides with an unmanaged host interface", desired.Name)
		}
		if network.Type != desired.Type {
			return fmt.Errorf("network %q already exists with type %q, expected %q", desired.Name, network.Type, desired.Type)
		}
		merged := cloneMap(network.Config)
		changed := network.Description != "Isolated public-egress bridge for disposable GitHub Actions VMs"
		for key, value := range desired.Config {
			if merged[key] != value {
				merged[key] = value
				changed = true
			}
		}
		if !changed {
			return nil
		}
		payload := map[string]any{
			"description": "Isolated public-egress bridge for disposable GitHub Actions VMs",
			"config":      merged,
		}
		path := "/1.0/networks/" + url.PathEscape(desired.Name)
		if err := put(ctx, r.Runner, path, payload); err != nil {
			return fmt.Errorf("update network %q: %w", desired.Name, err)
		}
		result.Changes = append(result.Changes, Change{"network", desired.Name, "update"})
		return nil
	}
	payload := map[string]any{
		"name":        desired.Name,
		"description": "Isolated public-egress bridge for disposable GitHub Actions VMs",
		"type":        desired.Type,
		"config":      desired.Config,
	}
	if err := post(ctx, r.Runner, "/1.0/networks", payload); err != nil {
		return fmt.Errorf("create network %q: %w", desired.Name, err)
	}
	result.Changes = append(result.Changes, Change{"network", desired.Name, "create"})
	return nil
}

func (r Reconciler) ensureProject(ctx context.Context, desired incusplan.Project, current []projectState, result *Result) error {
	for _, project := range current {
		if project.Name != desired.Name {
			continue
		}
		if project.Description == desired.Description && reflect.DeepEqual(project.Config, desired.Config) {
			return nil
		}
		payload := map[string]any{"description": desired.Description, "config": desired.Config}
		path := "/1.0/projects/" + url.PathEscape(desired.Name)
		if err := put(ctx, r.Runner, path, payload); err != nil {
			return fmt.Errorf("update project %q: %w", desired.Name, err)
		}
		result.Changes = append(result.Changes, Change{"project", desired.Name, "update"})
		return nil
	}
	payload := map[string]any{"name": desired.Name, "description": desired.Description, "config": desired.Config}
	if err := post(ctx, r.Runner, "/1.0/projects", payload); err != nil {
		return fmt.Errorf("create project %q: %w", desired.Name, err)
	}
	result.Changes = append(result.Changes, Change{"project", desired.Name, "create"})
	return nil
}

func (r Reconciler) ensureProfiles(ctx context.Context, project string, desired []incusplan.Profile, current []profileState, result *Result) error {
	byName := make(map[string]profileState, len(current))
	for _, profile := range current {
		byName[profile.Name] = profile
		if strings.HasPrefix(profile.Config["user.nddev.pool"], "nddev-") && !hasProfile(desired, profile.Name) {
			return fmt.Errorf("managed profile %q exists outside the selected pilot set; refusing implicit deletion", profile.Name)
		}
	}
	for _, profile := range desired {
		currentProfile, exists := byName[profile.Name]
		payload := map[string]any{
			"description": profile.Description,
			"config":      profile.Config,
			"devices":     profile.Devices,
		}
		if exists {
			if currentProfile.Description == profile.Description && reflect.DeepEqual(currentProfile.Config, profile.Config) && reflect.DeepEqual(currentProfile.Devices, profile.Devices) {
				continue
			}
			path := "/1.0/profiles/" + url.PathEscape(profile.Name) + "?project=" + url.QueryEscape(project)
			if err := put(ctx, r.Runner, path, payload); err != nil {
				return fmt.Errorf("update profile %q: %w", profile.Name, err)
			}
			result.Changes = append(result.Changes, Change{"profile", profile.Name, "update"})
			continue
		}
		payload["name"] = profile.Name
		path := "/1.0/profiles?project=" + url.QueryEscape(project)
		if err := post(ctx, r.Runner, path, payload); err != nil {
			return fmt.Errorf("create profile %q: %w", profile.Name, err)
		}
		result.Changes = append(result.Changes, Change{"profile", profile.Name, "create"})
	}
	return nil
}

func hasProfile(profiles []incusplan.Profile, name string) bool {
	for _, profile := range profiles {
		if profile.Name == name {
			return true
		}
	}
	return false
}

func cloneMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func get[T any](ctx context.Context, runner Runner, path string) (T, error) {
	var result T
	data, err := runner.Run(ctx, "--force-local", "query", path)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decode %s response: %w", path, err)
	}
	return result, nil
}

func post(ctx context.Context, runner Runner, path string, payload any) error {
	return mutate(ctx, runner, "POST", path, payload)
}

func put(ctx context.Context, runner Runner, path string, payload any) error {
	return mutate(ctx, runner, "PUT", path, payload)
}

func mutate(ctx context.Context, runner Runner, method, path string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s payload: %w", path, err)
	}
	_, err = runner.Run(ctx, "--force-local", "query", "--request", method, "--wait", "--data", string(data), path)
	return err
}
