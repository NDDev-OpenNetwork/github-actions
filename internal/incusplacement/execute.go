package incusplacement

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/lxc/incus/v7/shared/api"
	apiScriptlet "github.com/lxc/incus/v7/shared/api/scriptlet"
	"github.com/lxc/incus/v7/shared/scriptlet"
	"go.starlark.net/starlark"
)

// The compile-time names Incus v6.0.6 / v7 InstancePlacementCompile injects.
// get_instances does not take pending; get_instances_count does.
var placementPredeclared = []string{
	"log_info",
	"log_warn",
	"log_error",
	"set_target",
	"get_cluster_member_resources",
	"get_cluster_member_state",
	"get_instance_resources",
	"get_instances",
	"get_instances_count",
	"get_cluster_members",
	"get_project",
}

// ClusterSnapshot is the Incus view the scriptlet reads. MemoryTotalBytes is
// RAM only; swap is not a schedulable placement input.
type ClusterSnapshot struct {
	PoolName string
	Members  []MemberSnapshot
}

type MemberSnapshot struct {
	Name             string
	Config           map[string]string
	MemoryTotalBytes uint64
	CPUTotal         uint64
	LoadAverage      float64
	PoolTotalBytes   uint64
	PoolUsedBytes    uint64
	Instances        []InstanceSnapshot
	PendingCount     int
}

type InstanceSnapshot struct {
	Name           string
	MemoryLimitMiB int
}

type PlacementRequest struct {
	Project      string
	Name         string
	MemorySize   uint64
	RootDiskSize uint64
}

type PlacementOutcome struct {
	Target      string
	Failed      bool
	FailMessage string
	Logs        []string
}

// Execute compiles and runs a rendered placement scriptlet against a mocked
// Incus cluster. Builtins match Incus instance-placement signatures so a
// strings.Contains test cannot hide a live call-shape or packing bug.
func Execute(script string, request PlacementRequest, cluster ClusterSnapshot) (PlacementOutcome, error) {
	membersByName := make(map[string]MemberSnapshot, len(cluster.Members))
	candidates := make([]*api.ClusterMember, 0, len(cluster.Members))
	for _, member := range cluster.Members {
		if member.Name == "" {
			return PlacementOutcome{}, fmt.Errorf("cluster snapshot member is missing a name")
		}
		if _, duplicate := membersByName[member.Name]; duplicate {
			return PlacementOutcome{}, fmt.Errorf("cluster snapshot repeats member %q", member.Name)
		}
		membersByName[member.Name] = member
		config := api.ConfigMap{}
		for key, value := range member.Config {
			config[key] = value
		}
		candidates = append(candidates, &api.ClusterMember{
			ServerName:       member.Name,
			ClusterMemberPut: api.ClusterMemberPut{Config: config},
		})
	}

	var outcome PlacementOutcome
	setTarget := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var memberName string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "member_name", &memberName); err != nil {
			return nil, err
		}
		if _, found := membersByName[memberName]; !found {
			return nil, fmt.Errorf("Invalid member name: %s", memberName)
		}
		outcome.Target = memberName
		return starlark.None, nil
	}
	getResources := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var memberName string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "member_name", &memberName); err != nil {
			return nil, err
		}
		member, err := lookupMember(membersByName, memberName)
		if err != nil {
			return nil, err
		}
		return scriptlet.StarlarkMarshal(api.Resources{
			CPU:    api.ResourcesCPU{Total: member.CPUTotal},
			Memory: api.ResourcesMemory{Total: member.MemoryTotalBytes},
		})
	}
	getState := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var memberName string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "member_name", &memberName); err != nil {
			return nil, err
		}
		member, err := lookupMember(membersByName, memberName)
		if err != nil {
			return nil, err
		}
		poolName := cluster.PoolName
		if poolName == "" {
			poolName = "gha-lvm"
		}
		return scriptlet.StarlarkMarshal(api.ClusterMemberState{
			SysInfo: api.ClusterMemberSysInfo{LoadAverages: []float64{member.LoadAverage}},
			StoragePools: map[string]api.StoragePoolState{
				poolName: {ResourcesStoragePool: api.ResourcesStoragePool{
					Space: api.ResourcesStoragePoolSpace{
						Used: member.PoolUsedBytes, Total: member.PoolTotalBytes,
					},
				}},
			},
		})
	}
	getInstanceResources := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		if err := starlark.UnpackArgs(b.Name(), args, kwargs); err != nil {
			return nil, err
		}
		return scriptlet.StarlarkMarshal(apiScriptlet.InstanceResources{
			MemorySize: request.MemorySize, RootDiskSize: request.RootDiskSize,
		})
	}
	getInstances := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var project, location string
		if err := starlark.UnpackArgs(b.Name(), args, kwargs, "project??", &project, "location??", &location); err != nil {
			return nil, err
		}
		member, err := lookupMember(membersByName, location)
		if err != nil {
			return nil, err
		}
		instances := make([]api.Instance, 0, len(member.Instances))
		for _, instance := range member.Instances {
			instances = append(instances, api.Instance{
				Name: instance.Name, Project: project, Location: location,
				ExpandedConfig: api.ConfigMap{
					"limits.memory": strconv.Itoa(instance.MemoryLimitMiB) + "MiB",
				},
			})
		}
		return scriptlet.StarlarkMarshal(instances)
	}
	getInstancesCount := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		var project, location string
		var includePending bool
		if err := starlark.UnpackArgs(
			b.Name(), args, kwargs, "project??", &project, "location??", &location, "pending??", &includePending,
		); err != nil {
			return nil, err
		}
		member, err := lookupMember(membersByName, location)
		if err != nil {
			return nil, err
		}
		count := len(member.Instances)
		if includePending {
			count = member.PendingCount
		}
		return scriptlet.StarlarkMarshal(count)
	}
	logger := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		var builder strings.Builder
		for _, arg := range args {
			text, err := strconv.Unquote(arg.String())
			if err != nil {
				text = arg.String()
			}
			builder.WriteString(text)
		}
		outcome.Logs = append(outcome.Logs, b.Name()+": "+builder.String())
		return starlark.None, nil
	}
	unsupported := func(_ *starlark.Thread, b *starlark.Builtin, args starlark.Tuple, kwargs []starlark.Tuple) (starlark.Value, error) {
		return nil, fmt.Errorf("%s is not part of the fleet placement harness", b.Name())
	}

	program, err := scriptlet.Compile("instance_placement", script, placementPredeclared)
	if err != nil {
		return PlacementOutcome{}, err
	}
	thread := &starlark.Thread{Name: "instance_placement"}
	globals, err := program.Init(thread, starlark.StringDict{
		"log_info":                     starlark.NewBuiltin("log_info", logger),
		"log_warn":                     starlark.NewBuiltin("log_warn", logger),
		"log_error":                    starlark.NewBuiltin("log_error", logger),
		"set_target":                   starlark.NewBuiltin("set_target", setTarget),
		"get_cluster_member_resources": starlark.NewBuiltin("get_cluster_member_resources", getResources),
		"get_cluster_member_state":     starlark.NewBuiltin("get_cluster_member_state", getState),
		"get_instance_resources":       starlark.NewBuiltin("get_instance_resources", getInstanceResources),
		"get_instances":                starlark.NewBuiltin("get_instances", getInstances),
		"get_instances_count":          starlark.NewBuiltin("get_instances_count", getInstancesCount),
		"get_cluster_members":          starlark.NewBuiltin("get_cluster_members", unsupported),
		"get_project":                  starlark.NewBuiltin("get_project", unsupported),
	})
	if err != nil {
		return PlacementOutcome{}, fmt.Errorf("Failed initializing: %w", err)
	}
	placement := globals["instance_placement"]
	if placement == nil {
		return PlacementOutcome{}, errors.New("Scriptlet missing instance_placement function")
	}
	requestValue, err := scriptlet.StarlarkMarshal(apiScriptlet.InstancePlacement{
		InstancesPost: api.InstancesPost{Name: request.Name},
		Project:       request.Project,
	})
	if err != nil {
		return PlacementOutcome{}, err
	}
	candidatesValue, err := scriptlet.StarlarkMarshal(candidates)
	if err != nil {
		return PlacementOutcome{}, err
	}
	returned, err := starlark.Call(thread, placement, nil, []starlark.Tuple{
		{starlark.String("request"), requestValue},
		{starlark.String("candidate_members"), candidatesValue},
	})
	if err != nil {
		var evalErr *starlark.EvalError
		if errors.As(err, &evalErr) {
			outcome.Failed = true
			outcome.FailMessage = evalErr.Error()
			return outcome, nil
		}
		return outcome, err
	}
	if returned.Type() != "NoneType" {
		return PlacementOutcome{}, fmt.Errorf("Failed with unexpected return value: %v", returned)
	}
	return outcome, nil
}

func lookupMember(members map[string]MemberSnapshot, name string) (MemberSnapshot, error) {
	member, found := members[name]
	if !found {
		return MemberSnapshot{}, fmt.Errorf("Invalid member name: %s", name)
	}
	return member, nil
}
