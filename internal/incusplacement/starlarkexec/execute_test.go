package starlarkexec

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-actions/internal/config"
	"github.com/NDDev-OpenNetwork/github-actions/internal/incusplacement"
)

const (
	hostRAMBytes  = 16 * 1024 * 1024 * 1024
	eightGiBBytes = 8192 * 1024 * 1024
	rootDiskBytes = 50 * 1024 * 1024 * 1024
	poolBytes     = 200 * 1024 * 1024 * 1024
)

func TestRenderedPlacementCompilesAgainstIncusBuiltinNames(t *testing.T) {
	script := renderExample(t)
	outcome, err := Execute(script, eightGiBRequest(), fourEmptyMembers())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Target != "gha-runner-1" {
		t.Fatalf("empty cluster refused an 8GiB worker: %#v", outcome)
	}
}

func TestSpreadFourGiBWorkersLeaveNoEightGiBSlot(t *testing.T) {
	script := renderExample(t)
	// Four hosts, each holding 8 GiB of 4 GiB workers. Packing onto remaining
	// RAM is the live policy; spreading that occupancy is the counterexample
	// that used to starve Almaty 8 GiB jobs.
	outcome, err := Execute(script, eightGiBRequest(), membersWithFourGiBOccupancy([]int{8192, 8192, 8192, 8192}))
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Failed || !strings.Contains(outcome.FailMessage, "insufficient-memory") {
		t.Fatalf("spread 4GiB occupancy still placed an 8GiB worker: %#v", outcome)
	}
}

func TestPackedFourGiBWorkersLeaveAnEightGiBSlot(t *testing.T) {
	script := renderExample(t)
	outcome, err := Execute(script, eightGiBRequest(), membersWithFourGiBOccupancy([]int{12288, 12288, 8192, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Target != "gha-runner-4" {
		t.Fatalf("packed occupancy did not land the 8GiB worker on the empty member: %#v", outcome)
	}
}

func TestSysinfoSwapDoesNotBecomeSchedulableRAM(t *testing.T) {
	script := renderExample(t)
	cluster := membersWithFourGiBOccupancy([]int{8192, 8192, 8192, 8192})
	for i := range cluster.Members {
		cluster.Members[i].LoadAverage = 0
	}
	outcome, err := Execute(script, eightGiBRequest(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Failed {
		t.Fatalf("placement treated something other than resources.memory.total as RAM: %#v", outcome)
	}
}

func TestMaintenancePlacesOnClosedEmptyMember(t *testing.T) {
	script := renderExample(t)
	cluster := fourEmptyMembers()
	for i := range cluster.Members {
		cluster.Members[i].Config["user.gha_pressure.state"] = "closed"
	}
	job, err := Execute(script, eightGiBRequest(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if !job.Failed {
		t.Fatalf("closed gate still placed a job worker: %#v", job)
	}
	build, err := Execute(script, PlacementRequest{
		Project: "gha-fleet", Name: "gha-image-builder-stage",
		MemorySize: eightGiBBytes, RootDiskSize: rootDiskBytes,
	}, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if build.Failed || build.Target == "" {
		t.Fatalf("maintenance placement did not use the drained closed member: %#v", build)
	}
}

func TestPendingCreateWithoutRecordReservesMaxWorker(t *testing.T) {
	script := renderExample(t)
	cluster := fourEmptyMembers()
	cluster.Members[0].PendingCount = 1
	outcome, err := Execute(script, eightGiBRequest(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Target == "gha-runner-1" {
		t.Fatalf("pending create on runner-1 still took the 8GiB worker: %#v", outcome)
	}
}

func TestWrongProjectIsANoOp(t *testing.T) {
	script := renderExample(t)
	outcome, err := Execute(script, PlacementRequest{
		Project: "not-fleet", Name: "worker-1",
		MemorySize: eightGiBBytes, RootDiskSize: rootDiskBytes,
	}, fourEmptyMembers())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Target != "" {
		t.Fatalf("foreign project must return without set_target: %#v", outcome)
	}
}

func TestForeignProjectInstancesDoNotConsumeFleetRAM(t *testing.T) {
	script := renderExample(t)
	cluster := fourEmptyMembers()
	cluster.Members[0].Instances = []InstanceSnapshot{
		{Name: "other-1", Project: "not-fleet", MemoryLimitMiB: 8192},
		{Name: "other-2", Project: "not-fleet", MemoryLimitMiB: 8192},
	}
	cluster.Members[0].PendingCount = 0
	outcome, err := Execute(script, eightGiBRequest(), cluster)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Failed || outcome.Target != "gha-runner-1" {
		t.Fatalf("other-project occupancy filled gha-fleet RAM: %#v", outcome)
	}
}

func renderExample(t *testing.T) string {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	cfg, err := config.Load(filepath.Join(filepath.Dir(current), "../../../config/example-runner-1.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := incusplacement.Render(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

func eightGiBRequest() PlacementRequest {
	return PlacementRequest{
		Project: "gha-fleet", Name: "gha-job-almaty",
		MemorySize: eightGiBBytes, RootDiskSize: rootDiskBytes,
	}
}

func fourEmptyMembers() ClusterSnapshot {
	return membersWithFourGiBOccupancy([]int{0, 0, 0, 0})
}

func membersWithFourGiBOccupancy(committedMiB []int) ClusterSnapshot {
	members := make([]MemberSnapshot, len(committedMiB))
	for index, committed := range committedMiB {
		if committed%4096 != 0 {
			panic("occupancy must be a multiple of the 4GiB worker class")
		}
		count := committed / 4096
		instances := make([]InstanceSnapshot, 0, count)
		for n := 0; n < count; n++ {
			instances = append(instances, InstanceSnapshot{
				Name: fmt.Sprintf("gha-worker-%d-%d", index+1, n+1), MemoryLimitMiB: 4096,
			})
		}
		members[index] = MemberSnapshot{
			Name:             fmt.Sprintf("gha-runner-%d", index+1),
			Config:           pressureOpen(),
			MemoryTotalBytes: hostRAMBytes,
			CPUTotal:         8,
			PoolTotalBytes:   poolBytes,
			Instances:        instances,
			PendingCount:     count,
		}
	}
	return ClusterSnapshot{PoolName: "gha-lvm", Members: members}
}

func pressureOpen() map[string]string {
	return map[string]string{
		"user.gha_pressure.schema": "1",
		"user.gha_pressure.state":  "open",
	}
}
