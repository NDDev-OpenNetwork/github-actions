// Package memberdrain turns taking an Incus cluster member out of service from
// a sequence somebody remembers into a single operation.
//
// The member's admission gate is owned by the pressure publisher, which
// reasserts it every eleven seconds. A drain used to stop
// gha-pressure-gate.timer so its closed gate would hold -- and the silenced
// publisher then aged into compute_pressure_state_stale, which paged every
// ten minutes for the whole window: twenty-eight hours of noise across one
// night's image builds, all of it self-inflicted. A reboot mid-drain also
// restarted the timer and quietly reopened the member.
//
// The drain now leaves the timer running and writes a marker beside the gate
// state instead. The publisher honours the marker on every cycle: it keeps
// publishing a fresh, closed gate carrying the drain reason, so freshness
// never lapses, the alert stays quiet, and the drain survives a reboot.
// Restore removes the marker and republishes from live pressure.
//
// A drain never stops a running worker. It closes the gate so no new work is
// placed, then waits for the jobs already there to finish on their own --
// except warm instances, which are ready-unregistered by definition, hold
// nobody's job, and are recycled rather than waited out: one held a reboot
// hostage for a full forty-five-minute timeout. If real jobs outlast the
// deadline the drain reports that it is still occupied and by what; it does
// not decide to end someone's build.
package memberdrain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lxc/incus/v7/shared/api"
)

// Client lists the instances the cluster is carrying. Scoped to the worker
// project by the caller, so what comes back is workers rather than the whole
// cluster's containers.
type Client interface {
	GetInstances(api.InstanceType) ([]api.Instance, error)
	// DeleteInstance removes one instance. The drain uses it only for warm
	// instances, which carry no job by construction.
	DeleteInstance(name string) error
}

// Units is the systemd control a drain needs. Stopping the pressure timer is
// what makes the closed gate hold; starting it again is what hands the member
// back to its owner.
type Units interface {
	Stop(ctx context.Context, unit string) error
	Start(ctx context.Context, unit string) error
	IsActive(ctx context.Context, unit string) (bool, error)
}

// Marker is the durable statement that this member is drained. The pressure
// publisher reads it on every cycle and keeps the gate closed and fresh while
// it exists, which is what lets the drain leave the timer running.
type Marker interface {
	Set(reason string) error
	Clear() error
}

// Gate closes and reopens the member's admission gate through the component
// that owns it, rather than around it.
type Gate interface {
	// Each returns the scheduler value the member now carries, so the drain
	// reports what is true rather than what it asked for.
	ForceClose(ctx context.Context, reason string) (string, error)
	Reopen(ctx context.Context) (string, error)
}

// Deps are the three effects a drain has, injected so the decisions above can
// be tested without a cluster.
type Deps struct {
	Client Client
	Units  Units
	Gate   Gate
	Marker Marker
	Sleep  func(context.Context, time.Duration) error
	Now    func() time.Time
}

// Options describe one drain or restore.
type Options struct {
	MemberName string
	Reason     string
	TimerUnit  string
	Timeout    time.Duration
	Poll       time.Duration
	Apply      bool
}

// Occupant is one instance still held by the member.
type Occupant struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Result is what the operation did and what it found, in the shape the other
// fleet commands report.
type Result struct {
	MemberName   string `json:"member_name"`
	Action       string `json:"action"`
	Reason       string `json:"reason,omitempty"`
	TimerUnit    string `json:"timer_unit"`
	TimerStopped bool   `json:"timer_stopped"`
	TimerRunning bool   `json:"timer_running"`
	GateClosed   bool   `json:"gate_closed"`
	// Republished says the gate was handed back to its owner. It is not a
	// promise that the gate is open: hysteresis holds a just-closed member shut
	// until recovery has lasted, so a restore normally reports scheduler
	// "manual" and the publisher opens it a cycle or two later. Observed live:
	// republished at t+0, open at t+45s.
	GateRepublished   bool       `json:"gate_republished"`
	SchedulerInstance string     `json:"scheduler_instance,omitempty"`
	Occupants         []Occupant `json:"occupants"`
	RecycledWarm      []string   `json:"recycled_warm,omitempty"`
	WaitedSecs        int        `json:"waited_seconds"`
	Drained           bool       `json:"drained"`
	TimedOut          bool       `json:"timed_out"`
	Applied           bool       `json:"applied"`
}

const (
	// DefaultTimerUnit owns the member's gate.
	DefaultTimerUnit = "gha-pressure-gate.timer"
	// DefaultTimeout is generous on purpose. A drain that gives up early is
	// worse than one that takes a while: the operator's alternative is to stop
	// a job that was going to finish.
	DefaultTimeout = 45 * time.Minute
	// DefaultPoll is well under the publisher's eleven-second cycle, so the
	// member is seen to empty about as soon as it does.
	DefaultPoll = 5 * time.Second
)

// Occupancy reports the instances the member is still carrying. Anything not
// stopped counts: a container that is starting is about to run a job, and a
// drain that ignored it would hand back a member with work landing on it.
func Occupancy(client Client, memberName string) ([]Occupant, error) {
	if client == nil || memberName == "" {
		return nil, fmt.Errorf("member occupancy requires a client and a member name")
	}
	instances, err := client.GetInstances(api.InstanceTypeAny)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}
	occupants := make([]Occupant, 0, len(instances))
	for _, instance := range instances {
		if instance.Location != memberName {
			continue
		}
		if instance.Status == "Stopped" {
			continue
		}
		occupants = append(occupants, Occupant{Name: instance.Name, Status: instance.Status})
	}
	sort.Slice(occupants, func(i, j int) bool { return occupants[i].Name < occupants[j].Name })
	return occupants, nil
}

func (d Deps) valid() error {
	if d.Client == nil || d.Units == nil || d.Gate == nil || d.Marker == nil {
		return fmt.Errorf("drain requires a client, unit control, a gate and a drain marker")
	}
	return nil
}

func (o Options) normalised() (Options, error) {
	if o.MemberName == "" {
		return Options{}, fmt.Errorf("drain requires a member name")
	}
	if o.TimerUnit == "" {
		o.TimerUnit = DefaultTimerUnit
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	if o.Poll > o.Timeout {
		return Options{}, fmt.Errorf("drain poll interval %s exceeds its timeout %s", o.Poll, o.Timeout)
	}
	return o, nil
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

func (d Deps) sleep(ctx context.Context, interval time.Duration) error {
	if d.Sleep != nil {
		return d.Sleep(ctx, interval)
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Drain closes the member to new work and waits for what is already running to
// finish. Without Apply it reports what it would do and what the member is
// carrying, and changes nothing.
func Drain(ctx context.Context, deps Deps, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := deps.valid(); err != nil {
		return Result{}, err
	}
	options, err := options.normalised()
	if err != nil {
		return Result{}, err
	}
	if options.Reason == "" {
		return Result{}, fmt.Errorf("drain requires a reason, which is published as the gate's close reason")
	}
	result := Result{
		MemberName: options.MemberName, Action: "drain", Reason: options.Reason,
		TimerUnit: options.TimerUnit, Applied: options.Apply,
	}
	if !options.Apply {
		occupants, err := Occupancy(deps.Client, options.MemberName)
		if err != nil {
			return Result{}, err
		}
		running, err := deps.Units.IsActive(ctx, options.TimerUnit)
		if err != nil {
			return Result{}, err
		}
		result.Occupants = occupants
		result.Drained = len(occupants) == 0
		result.TimerRunning = running
		return result, nil
	}

	// Order matters. The marker goes down first: from this point every
	// publisher cycle keeps the gate closed and fresh, so the close below can
	// never be undone by the next cycle, the staleness alert never fires, and
	// a reboot mid-drain comes back still drained.
	if err := deps.Marker.Set(options.Reason); err != nil {
		return Result{}, fmt.Errorf("set the drain marker on %s: %w", options.MemberName, err)
	}
	scheduler, err := deps.Gate.ForceClose(ctx, options.Reason)
	if err != nil {
		return Result{}, fmt.Errorf("close the gate on %s: %w", options.MemberName, err)
	}
	result.GateClosed = true
	result.SchedulerInstance = scheduler

	started := deps.now()
	deadline := started.Add(options.Timeout)
	for {
		occupants, err := Occupancy(deps.Client, options.MemberName)
		if err != nil {
			return Result{}, err
		}
		// A warm instance is ready-unregistered by definition: it holds no
		// job, and the maintainer refills it on an open member. Waiting for
		// one is waiting for nothing -- one held a reboot hostage for the
		// full timeout -- so warm occupants are recycled, not waited out.
		remaining := occupants[:0]
		for _, occupant := range occupants {
			if strings.HasPrefix(occupant.Name, "warm-") {
				if err := deps.Client.DeleteInstance(occupant.Name); err != nil {
					return Result{}, fmt.Errorf("recycle warm occupant %s: %w", occupant.Name, err)
				}
				result.RecycledWarm = append(result.RecycledWarm, occupant.Name)
				continue
			}
			remaining = append(remaining, occupant)
		}
		occupants = remaining
		result.Occupants = occupants
		result.WaitedSecs = int(deps.now().Sub(started) / time.Second)
		if len(occupants) == 0 {
			result.Drained = true
			return result, nil
		}
		if !deps.now().Before(deadline) {
			// The member is closed and no new work lands on it. What is left is
			// somebody's build, and ending it is not this command's decision.
			result.TimedOut = true
			return result, nil
		}
		if err := deps.sleep(ctx, options.Poll); err != nil {
			return Result{}, err
		}
	}
}

// Restore hands the member back: the gate is published from live pressure again
// and the timer that owns it is started, in that order, so the member is never
// left open with nothing maintaining it.
func Restore(ctx context.Context, deps Deps, options Options) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := deps.valid(); err != nil {
		return Result{}, err
	}
	options, err := options.normalised()
	if err != nil {
		return Result{}, err
	}
	result := Result{
		MemberName: options.MemberName, Action: "restore",
		TimerUnit: options.TimerUnit, Applied: options.Apply,
	}
	running, err := deps.Units.IsActive(ctx, options.TimerUnit)
	if err != nil {
		return Result{}, err
	}
	result.TimerRunning = running
	if !options.Apply {
		return result, nil
	}
	// The marker clears first, or the next publisher cycle would immediately
	// re-close what Reopen just published.
	if err := deps.Marker.Clear(); err != nil {
		return Result{}, fmt.Errorf("clear the drain marker on %s: %w", options.MemberName, err)
	}
	scheduler, err := deps.Gate.Reopen(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("republish the gate on %s: %w", options.MemberName, err)
	}
	result.GateRepublished = true
	result.SchedulerInstance = scheduler
	// Marker-era drains leave the timer running; this start also heals a
	// member drained by the old stop-the-timer flow or by hand.
	if err := deps.Units.Start(ctx, options.TimerUnit); err != nil {
		return Result{}, fmt.Errorf("start %s: %w", options.TimerUnit, err)
	}
	result.TimerRunning = true
	return result, nil
}
