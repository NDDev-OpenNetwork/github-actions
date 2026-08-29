package memberdrain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

type fakeClient struct {
	batches [][]api.Instance
	calls   int
}

func (f *fakeClient) GetInstances(api.InstanceType) ([]api.Instance, error) {
	if f.calls >= len(f.batches) {
		f.calls++
		return f.batches[len(f.batches)-1], nil
	}
	batch := f.batches[f.calls]
	f.calls++
	return batch, nil
}

type fakeUnits struct {
	events  *[]string
	active  bool
	stopErr error
}

func (f *fakeUnits) Stop(context.Context, string) error {
	if f.stopErr != nil {
		return f.stopErr
	}
	*f.events = append(*f.events, "timer-stop")
	return nil
}

func (f *fakeUnits) Start(context.Context, string) error {
	*f.events = append(*f.events, "timer-start")
	return nil
}

func (f *fakeUnits) IsActive(context.Context, string) (bool, error) { return f.active, nil }

type fakeGate struct {
	events *[]string
	reason string
}

func (f *fakeGate) ForceClose(_ context.Context, reason string) (string, error) {
	f.reason = reason
	*f.events = append(*f.events, "gate-close")
	return "manual", nil
}

func (f *fakeGate) Reopen(context.Context) (string, error) {
	*f.events = append(*f.events, "gate-reopen")
	// Hysteresis holds a just-closed member shut, so a restore reports the
	// value the publisher actually wrote, not the one it is heading for.
	return "manual", nil
}

func instance(name, member, status string) api.Instance {
	return api.Instance{Name: name, Location: member, Status: status}
}

func TestOccupancyCountsOnlyThisMemberAndIgnoresStopped(t *testing.T) {
	client := &fakeClient{batches: [][]api.Instance{{
		instance("worker-a", "gha-runner-3", "Running"),
		instance("worker-b", "gha-runner-4", "Running"),
		instance("worker-c", "gha-runner-3", "Stopped"),
		instance("worker-d", "gha-runner-3", "Starting"),
	}}}
	occupants, err := Occupancy(client, "gha-runner-3")
	if err != nil {
		t.Fatalf("occupancy: %v", err)
	}
	if len(occupants) != 2 {
		t.Fatalf("expected two occupants, got %#v", occupants)
	}
	// A starting container is about to run a job. Treating it as empty would
	// hand back a member with work landing on it.
	if occupants[0].Name != "worker-a" || occupants[1].Name != "worker-d" {
		t.Fatalf("unexpected occupants: %#v", occupants)
	}
}

func TestDrainStopsTheTimerBeforeClosingTheGate(t *testing.T) {
	// The publisher reasserts the gate every eleven seconds, so a gate closed
	// while it is still running is reopened on the next cycle. Order is the
	// whole correctness of this operation.
	var events []string
	client := &fakeClient{batches: [][]api.Instance{{}}}
	deps := Deps{
		Client: client,
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
		Now:    func() time.Time { return time.Unix(0, 0).UTC() },
	}
	result, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "kernel slab reboot", Apply: true,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 2 || events[0] != "timer-stop" || events[1] != "gate-close" {
		t.Fatalf("expected the timer to stop before the gate closed, got %v", events)
	}
	if !result.Drained || !result.TimerStopped || !result.GateClosed {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestDrainWaitsForRunningWorkToFinish(t *testing.T) {
	var events []string
	client := &fakeClient{batches: [][]api.Instance{
		{instance("worker-a", "gha-runner-3", "Running")},
		{instance("worker-a", "gha-runner-3", "Running")},
		{},
	}}
	clock := time.Unix(0, 0).UTC()
	slept := 0
	deps := Deps{
		Client: client,
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
		Now:    func() time.Time { return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			slept++
			clock = clock.Add(d)
			return nil
		},
	}
	result, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "slab reboot", Apply: true,
		Poll: 5 * time.Second, Timeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !result.Drained || result.TimedOut {
		t.Fatalf("expected a completed drain, got %#v", result)
	}
	if slept != 2 {
		t.Fatalf("expected to wait twice, waited %d times", slept)
	}
	if result.WaitedSecs != 10 {
		t.Fatalf("expected ten seconds of waiting, got %d", result.WaitedSecs)
	}
}

func TestDrainTimesOutWithoutEndingAnybodysJob(t *testing.T) {
	var events []string
	client := &fakeClient{batches: [][]api.Instance{
		{instance("worker-a", "gha-runner-3", "Running")},
	}}
	clock := time.Unix(0, 0).UTC()
	deps := Deps{
		Client: client,
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
		Now:    func() time.Time { return clock },
		Sleep: func(_ context.Context, d time.Duration) error {
			clock = clock.Add(d)
			return nil
		},
	}
	result, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "slab reboot", Apply: true,
		Poll: 10 * time.Second, Timeout: 20 * time.Second,
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if !result.TimedOut || result.Drained {
		t.Fatalf("expected a timed-out drain, got %#v", result)
	}
	if len(result.Occupants) != 1 || result.Occupants[0].Name != "worker-a" {
		t.Fatalf("a timed-out drain must name what is still there, got %#v", result.Occupants)
	}
	// The gate stays closed and the job stays running: nothing in the event log
	// stops an instance.
	for _, event := range events {
		if event == "instance-stop" {
			t.Fatal("a drain must never end a running job")
		}
	}
}

func TestDrainWithoutApplyChangesNothing(t *testing.T) {
	var events []string
	client := &fakeClient{batches: [][]api.Instance{
		{instance("worker-a", "gha-runner-3", "Running")},
	}}
	deps := Deps{
		Client: client,
		Units:  &fakeUnits{events: &events, active: true},
		Gate:   &fakeGate{events: &events},
	}
	result, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "dry run",
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("a drain without --apply must change nothing, did %v", events)
	}
	if result.Applied || result.TimerStopped || result.GateClosed {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !result.TimerRunning || len(result.Occupants) != 1 {
		t.Fatalf("a dry run must still report what it found: %#v", result)
	}
}

func TestDrainRequiresAReason(t *testing.T) {
	var events []string
	deps := Deps{
		Client: &fakeClient{batches: [][]api.Instance{{}}},
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
	}
	if _, err := Drain(context.Background(), deps, Options{MemberName: "gha-runner-3"}); err == nil {
		t.Fatal("expected a drain with no reason to be refused: the reason is published as the close reason")
	}
}

func TestDrainReportsAFailureToStopTheTimerRatherThanClosingAnyway(t *testing.T) {
	var events []string
	deps := Deps{
		Client: &fakeClient{batches: [][]api.Instance{{}}},
		Units:  &fakeUnits{events: &events, stopErr: errors.New("unit not found")},
		Gate:   &fakeGate{events: &events},
	}
	_, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "slab reboot", Apply: true,
	})
	if err == nil {
		t.Fatal("expected the drain to fail when the timer cannot be stopped")
	}
	// Closing the gate with the publisher still running would read as a drain
	// and be undone within eleven seconds.
	for _, event := range events {
		if event == "gate-close" {
			t.Fatal("the gate must not be closed when the timer is still running")
		}
	}
}

func TestRestoreRepublishesBeforeStartingTheTimer(t *testing.T) {
	var events []string
	deps := Deps{
		Client: &fakeClient{batches: [][]api.Instance{{}}},
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
	}
	result, err := Restore(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Apply: true,
	})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(events) != 2 || events[0] != "gate-reopen" || events[1] != "timer-start" {
		t.Fatalf("expected the gate republished before the timer started, got %v", events)
	}
	if !result.GateRepublished || !result.TimerRunning {
		t.Fatalf("unexpected result: %#v", result)
	}
	if result.SchedulerInstance != "manual" {
		t.Fatalf("restore must report the value the publisher wrote, got %q", result.SchedulerInstance)
	}
}

func TestPollLongerThanTheTimeoutIsRefused(t *testing.T) {
	var events []string
	deps := Deps{
		Client: &fakeClient{batches: [][]api.Instance{{}}},
		Units:  &fakeUnits{events: &events},
		Gate:   &fakeGate{events: &events},
	}
	_, err := Drain(context.Background(), deps, Options{
		MemberName: "gha-runner-3", Reason: "slab reboot",
		Poll: time.Hour, Timeout: time.Minute,
	})
	if err == nil {
		t.Fatal("expected a poll interval longer than the timeout to be refused")
	}
}
