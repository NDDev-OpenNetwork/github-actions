package incusreconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplan"
)

type fakeRunner struct {
	responses map[string][]byte
	calls     [][]string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, slices.Clone(args))
	path := args[len(args)-1]
	if contains(args, "--request") {
		return []byte(`{}`), nil
	}
	response, exists := f.responses[path]
	if !exists {
		return nil, fmt.Errorf("unexpected path %q", path)
	}
	return response, nil
}

func TestVersionMismatchStopsBeforeMutation(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	runner := &fakeRunner{responses: map[string][]byte{"/1.0": mustJSON(t, compatibleServer("6.1.0", plan.APIAddress))}}
	_, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), `does not match pinned "6.0.6"`) {
		t.Fatalf("expected pinned-version failure, got %v", err)
	}
	if len(runner.calls) != 1 || contains(runner.calls[0], "--request") {
		t.Fatalf("unexpected calls after compatibility failure: %#v", runner.calls)
	}
}

func TestApplyIsNoopWhenDesiredStateExists(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	runner := &fakeRunner{responses: desiredResponses(t, plan)}
	result, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("apply desired state: %v", err)
	}
	if !result.Applied || len(result.Changes) != 0 {
		t.Fatalf("expected zero-change apply, got %#v", result)
	}
	for _, call := range runner.calls {
		if contains(call, "--request") {
			t.Fatalf("no-op reconciliation mutated state: %#v", call)
		}
	}
}

func TestCreatePlanNeverIssuesDelete(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	runner := &fakeRunner{responses: map[string][]byte{
		"/1.0":                                         mustJSON(t, compatibleServer("6.0.6", plan.APIAddress)),
		"/1.0/storage-pools?recursion=1":               {},
		"/1.0/network-acls?recursion=1":                {},
		"/1.0/networks?recursion=1":                    {},
		"/1.0/projects?recursion=1":                    {},
		"/1.0/profiles?project=gha-fleet&recursion=1":  {},
		"/1.0/instances?project=gha-fleet&recursion=1": {},
	}}
	for path := range runner.responses {
		if path != "/1.0" {
			runner.responses[path] = []byte(`[]`)
		}
	}
	result, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatalf("create desired state: %v", err)
	}
	if len(result.Changes) != 6 {
		t.Fatalf("changes = %#v, want storage, ACL, network, project, profile and server placement", result.Changes)
	}
	for _, call := range runner.calls {
		if contains(call, "DELETE") {
			t.Fatalf("reconciler issued destructive request: %#v", call)
		}
		if contains(call, "--request") && (!contains(call, "--force-local") || !contains(call, "--wait")) {
			t.Fatalf("mutation is not local and bounded: %#v", call)
		}
	}
}

func TestProfileUpdateIsBlockedWhileAnInstanceUsesIt(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	responses := desiredResponses(t, plan)
	profilesPath := "/1.0/profiles?project=gha-fleet&recursion=1"
	var profiles []profileState
	if err := json.Unmarshal(responses[profilesPath], &profiles); err != nil {
		t.Fatal(err)
	}
	for index := range profiles {
		if profiles[index].Name == plan.Profiles[0].Name {
			profiles[index].Config = cloneMap(profiles[index].Config)
			profiles[index].Config["limits.cpu.allowance"] = "400%"
		}
	}
	responses[profilesPath] = mustJSON(t, profiles)
	responses["/1.0/instances?project=gha-fleet&recursion=1"] = mustJSON(t, []instanceState{{
		Name: "example-running-worker", Profiles: []string{plan.Profiles[0].Name},
	}})
	runner := &fakeRunner{responses: responses}
	_, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "refusing live update until the profile drains") {
		t.Fatalf("expected in-use profile failure, got %v", err)
	}
	for _, call := range runner.calls {
		if contains(call, "--request") {
			t.Fatalf("blocked profile update mutated Incus: %#v", call)
		}
	}
}

func TestStorageDriftIsFailClosed(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	storage := storageState{Name: plan.Storage.Name, Driver: "dir", Config: map[string]string{}}
	runner := &fakeRunner{responses: map[string][]byte{
		"/1.0":                           mustJSON(t, serverWithDesiredConfig(compatibleServer("6.0.6", plan.APIAddress), plan)),
		"/1.0/storage-pools?recursion=1": mustJSON(t, []storageState{storage}),
	}}
	_, err := (Reconciler{Runner: runner}).Apply(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "refusing") && !strings.Contains(err.Error(), "expected \"lvm\"") {
		t.Fatalf("expected fail-closed storage drift, got %v", err)
	}
	for _, call := range runner.calls {
		if contains(call, "--request") {
			t.Fatalf("storage drift triggered mutation: %#v", call)
		}
	}
}

func TestNetworkMigrationRemovesBridgeWideACL(t *testing.T) {
	t.Parallel()

	plan := repositoryPlan(t)
	currentConfig := cloneMap(plan.Network.Config)
	currentConfig["security.acls"] = plan.ACL.Name
	currentConfig["security.acls.default.egress.action"] = "reject"
	currentConfig["security.acls.default.ingress.action"] = "reject"
	runner := &fakeRunner{responses: map[string][]byte{}}
	result := Result{}
	err := (Reconciler{Runner: runner}).ensureNetwork(context.Background(), plan.Network, []networkState{{
		Name: plan.Network.Name, Type: plan.Network.Type, Managed: true,
		Description: "Isolated public-egress bridge for disposable GitHub Actions VMs", Config: currentConfig,
	}}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Action != "update" || len(runner.calls) != 1 {
		t.Fatalf("bridge ACL migration = changes %#v calls %#v", result.Changes, runner.calls)
	}
	call := runner.calls[0]
	dataIndex := slices.Index(call, "--data")
	if dataIndex < 0 || dataIndex+1 >= len(call) {
		t.Fatalf("network update has no payload: %#v", call)
	}
	var payload struct {
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal([]byte(call[dataIndex+1]), &payload); err != nil {
		t.Fatal(err)
	}
	for key := range payload.Config {
		if strings.HasPrefix(key, "security.acls") {
			t.Fatalf("bridge ACL key survived migration: %s=%q", key, payload.Config[key])
		}
	}
}

func desiredResponses(t *testing.T, plan incusplan.Plan) map[string][]byte {
	t.Helper()
	profiles := make([]profileState, 0, len(plan.Profiles)+1)
	profiles = append(profiles, profileState{Name: "default", Config: map[string]string{}, Devices: map[string]map[string]string{}})
	for _, profile := range plan.Profiles {
		profiles = append(profiles, profileState(profile))
	}
	return map[string][]byte{
		"/1.0":                                         mustJSON(t, serverWithDesiredConfig(compatibleServer("6.0.6", plan.APIAddress), plan)),
		"/1.0/storage-pools?recursion=1":               mustJSON(t, []storageState{{Name: plan.Storage.Name, Driver: plan.Storage.Driver, Config: plan.Storage.Config}}),
		"/1.0/network-acls?recursion=1":                mustJSON(t, []aclState{{Name: plan.ACL.Name, Description: plan.ACL.Description, Config: plan.ACL.Config, Ingress: plan.ACL.Ingress, Egress: plan.ACL.Egress}}),
		"/1.0/networks?recursion=1":                    mustJSON(t, []networkState{{Name: plan.Network.Name, Description: "Isolated public-egress bridge for disposable GitHub Actions VMs", Type: plan.Network.Type, Managed: true, Config: plan.Network.Config}}),
		"/1.0/projects?recursion=1":                    mustJSON(t, []projectState{{Name: plan.Project.Name, Description: plan.Project.Description, Config: plan.Project.Config}}),
		"/1.0/profiles?project=gha-fleet&recursion=1":  mustJSON(t, profiles),
		"/1.0/instances?project=gha-fleet&recursion=1": mustJSON(t, []instanceState{}),
	}
}

func serverWithDesiredConfig(server serverState, plan incusplan.Plan) serverState {
	for key, value := range plan.ServerConfig {
		server.Config[key] = value
	}
	return server
}

func compatibleServer(version, address string) serverState {
	server := serverState{
		APIExtensions: slices.Clone(requiredExtensions),
		Auth:          "trusted",
		Config:        map[string]string{"core.https_address": address},
	}
	server.Environment.Driver = "lxc | qemu"
	server.Environment.Server = "incus"
	server.Environment.ServerVersion = version
	server.Environment.StorageSupportedDrivers = append(server.Environment.StorageSupportedDrivers, struct {
		Name string `json:"Name"`
	}{Name: "lvm"})
	return server
}

func repositoryPlan(t *testing.T) incusplan.Plan {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "config", "example-runner-1.yaml"))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	plan, err := incusplan.Build(cfg, []string{"nddev-linux-standard"})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	return plan
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return data
}

func contains(values []string, wanted string) bool {
	return slices.Contains(values, wanted)
}
