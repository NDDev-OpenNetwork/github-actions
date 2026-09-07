package scaleset

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudbase/garm/params"
)

type demandReaderFunc func(context.Context, int) (params.RunnerScaleSet, error)

func (f demandReaderFunc) GetRunnerScaleSetByID(ctx context.Context, id int) (params.RunnerScaleSet, error) {
	return f(ctx, id)
}

func demandFixture() (params.ScaleSet, params.RunnerScaleSet) {
	enabled := true
	local := params.ScaleSet{ScaleSetID: 5, Name: "example-integration", Enabled: true, MaxRunners: 8, DesiredRunnerCount: 0}
	remote := params.RunnerScaleSet{ID: 5, Name: local.Name, Enabled: &enabled, Statistics: &params.RunnerScaleSetStatistic{TotalAssignedJobs: 3}}
	return local, remote
}

func TestConfirmedDemandRetainedWaiterCannotCreateAgainstFreshZero(t *testing.T) {
	local, remote := demandFixture()
	remote.Statistics.TotalAssignedJobs = 0
	now := time.Now()
	var gate confirmedDemandGate
	reads := 0
	reader := demandReaderFunc(func(ctx context.Context, id int) (params.RunnerScaleSet, error) {
		reads++
		if id != local.ScaleSetID {
			t.Fatalf("wrong scale-set read: %d", id)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > demandReadTimeout {
			t.Fatal("demand read must have a bounded deadline")
		}
		return remote, nil
	})
	for _, elapsed := range []time.Duration{0, time.Second, 5 * time.Second, 29 * time.Second} {
		target, err := gate.target(context.Background(), now.Add(elapsed), reader, local, 0, 4)
		if err != nil || target != 0 {
			t.Fatalf("retained waiters allocated capacity: target=%d err=%v", target, err)
		}
	}
	if reads != 1 {
		t.Fatalf("zero-demand read storm: %d reads", reads)
	}
	remote.Statistics.TotalAssignedJobs = 2
	target, err := gate.target(context.Background(), now.Add(demandReadBackoff), reader, local, 0, 4)
	if err != nil || target != 2 || reads != 2 {
		t.Fatalf("new positive demand did not reopen: target=%d reads=%d err=%v", target, reads, err)
	}
}

func TestConfirmedDemandRefreshesStaleZeroAndDoesNotCachePositive(t *testing.T) {
	local, remote := demandFixture()
	var gate confirmedDemandGate
	reads := 0
	reader := demandReaderFunc(func(context.Context, int) (params.RunnerScaleSet, error) { reads++; return remote, nil })
	target, err := gate.target(context.Background(), time.Now(), reader, local, 0, 5)
	if err != nil || target != 3 {
		t.Fatalf("stale local zero stranded current work: target=%d err=%v", target, err)
	}
	remote.Statistics.TotalAssignedJobs = 0
	target, err = gate.target(context.Background(), time.Now(), reader, local, 1, 5)
	if err != nil || target != 1 || reads != 2 {
		t.Fatalf("stale positive demand permitted another create: target=%d reads=%d err=%v", target, reads, err)
	}
}

func TestConfirmedDemandPreservesAdmissionAndMaximum(t *testing.T) {
	for _, tc := range []struct{ admitted, maximum, current, want int }{{2, 8, 0, 2}, {5, 1, 0, 1}, {0, 8, 0, 0}, {2, 8, 2, 2}, {4, 8, 5, 5}} {
		local, remote := demandFixture()
		local.MaxRunners = uint(tc.maximum)
		var gate confirmedDemandGate
		target, err := gate.target(context.Background(), time.Now(), demandReaderFunc(func(context.Context, int) (params.RunnerScaleSet, error) { return remote, nil }), local, tc.current, tc.admitted)
		if err != nil || target != tc.want {
			t.Fatalf("%+v: target=%d err=%v", tc, target, err)
		}
	}
}

func TestConfirmedDemandUnknownOrWrongIdentityFailsClosed(t *testing.T) {
	for _, kind := range []string{"read-error", "missing-stats", "wrong-id", "wrong-name", "disabled", "unknown-enabled", "negative", "running-exceeds-assigned"} {
		t.Run(kind, func(t *testing.T) {
			local, remote := demandFixture()
			var readErr error
			switch kind {
			case "read-error":
				readErr = errors.New("unavailable")
			case "missing-stats":
				remote.Statistics = nil
			case "wrong-id":
				remote.ID++
			case "wrong-name":
				remote.Name = "example-other"
			case "disabled":
				*remote.Enabled = false
			case "unknown-enabled":
				remote.Enabled = nil
			case "negative":
				remote.Statistics.TotalAssignedJobs = -1
			case "running-exceeds-assigned":
				remote.Statistics.TotalRunningJobs = 4
			}
			var gate confirmedDemandGate
			reads := 0
			now := time.Now()
			reader := demandReaderFunc(func(context.Context, int) (params.RunnerScaleSet, error) { reads++; return remote, readErr })
			target, err := gate.target(context.Background(), now, reader, local, 1, 4)
			if err == nil || target != 1 {
				t.Fatalf("unverified demand permitted create: target=%d err=%v", target, err)
			}
			_, _ = gate.target(context.Background(), now.Add(time.Second), reader, local, 1, 4)
			if reads != 1 {
				t.Fatal("failed read was not bounded")
			}
		})
	}
}
