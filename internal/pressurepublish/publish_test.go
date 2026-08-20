package pressurepublish

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-actions/internal/pressuregate"
	"github.com/lxc/incus/v7/shared/api"
)

type fakeClient struct {
	member  api.ClusterMember
	updates int
}

func (f *fakeClient) GetClusterMember(string) (*api.ClusterMember, string, error) {
	copy := f.member
	return &copy, "etag", nil
}
func (f *fakeClient) UpdateClusterMember(_ string, member api.ClusterMemberPut, _ string) error {
	f.member.ClusterMemberPut = member
	f.updates++
	return nil
}

func policy() pressuregate.Policy {
	return pressuregate.Policy{Required: true, StaleAfterSeconds: 90, HeartbeatSeconds: 30, MinimumClosedSeconds: 15,
		CPUSomeClose: 20, CPUSomeReopen: 10, MemoryFullClose: 1, MemoryFullReopen: .1,
		IOFullClose: 10, IOFullReopen: 5}
}

func TestReconcileStartsClosedThenPublishesOpenAfterMinimumPeriod(t *testing.T) {
	now := time.Now().UTC()
	client := &fakeClient{member: api.ClusterMember{ServerName: "example-runner-1"}}
	statePath := filepath.Join(t.TempDir(), "state.json")
	options := Options{MemberName: "example-runner-1", StatePath: statePath, Policy: policy(), Sample: pressuregate.Sample{ObservedAt: now}, Apply: true, Now: now}
	first, err := Reconcile(context.Background(), client, options)
	if err != nil || first.Scheduler != "manual" || !first.Applied {
		t.Fatalf("first reconcile = %#v err=%v", first, err)
	}
	options.Now = now.Add(16 * time.Second)
	options.Sample.ObservedAt = options.Now
	second, err := Reconcile(context.Background(), client, options)
	if err != nil || second.Scheduler != "all" || client.updates != 2 {
		t.Fatalf("second reconcile = %#v updates=%d err=%v", second, client.updates, err)
	}
	options.Now = now.Add(26 * time.Second)
	options.Sample.ObservedAt = options.Now
	third, err := Reconcile(context.Background(), client, options)
	if err != nil || third.Changed || client.updates != 2 {
		t.Fatalf("pre-heartbeat reconcile = %#v updates=%d err=%v", third, client.updates, err)
	}
}

func TestPlanDoesNotWriteMemberOrState(t *testing.T) {
	now := time.Now().UTC()
	client := &fakeClient{member: api.ClusterMember{ServerName: "example-runner-1"}}
	statePath := filepath.Join(t.TempDir(), "state.json")
	result, err := Reconcile(context.Background(), client, Options{MemberName: "example-runner-1", StatePath: statePath, Policy: policy(), Sample: pressuregate.Sample{ObservedAt: now, CPUSomeAvg10: 50}, Now: now})
	if err != nil || result.Scheduler != "manual" || result.Applied || client.updates != 0 {
		t.Fatalf("plan = %#v updates=%d err=%v", result, client.updates, err)
	}
}
