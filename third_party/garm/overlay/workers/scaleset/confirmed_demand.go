package scaleset

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudbase/garm/params"
)

const (
	demandReadTimeout = 5 * time.Second
	demandReadBackoff = 30 * time.Second
)

type scaleSetDemandReader interface {
	GetRunnerScaleSetByID(context.Context, int) (params.RunnerScaleSet, error)
}

// confirmedDemandGate bounds new JIT registrations by GitHub's current demand.
// Retained JobAssigned records preserve audit/FIFO identity, but cannot prove
// that GitHub still assigns that work to this scale set. Conversely, the last
// message's persisted count may be stale-low. Read the authoritative snapshot
// before allocating; keep every existing job and runner untouched on refusal.
// The owning Worker's mutex serializes access to nextRead.
type confirmedDemandGate struct {
	nextRead time.Time
}

func (g *confirmedDemandGate) target(ctx context.Context, now time.Time, reader scaleSetDemandReader, scaleSet params.ScaleSet, current, admitted int) (int, error) {
	if !scaleSet.Enabled || admitted <= current || admitted < 1 || scaleSet.MaxRunners < 1 {
		return current, nil
	}
	if now.Before(g.nextRead) {
		return current, nil
	}
	// Failed, missing and zero-demand observations all have bounded read cost.
	// A successful positive observation is never cached across a new create.
	g.nextRead = now.Add(demandReadBackoff)
	readCtx, cancel := context.WithTimeout(ctx, demandReadTimeout)
	defer cancel()
	remote, err := reader.GetRunnerScaleSetByID(readCtx, scaleSet.ScaleSetID)
	if err != nil {
		return current, fmt.Errorf("read current scale-set demand: %w", err)
	}
	if remote.ID != scaleSet.ScaleSetID || remote.Name != scaleSet.Name || remote.Enabled == nil || !*remote.Enabled || remote.Statistics == nil {
		return current, fmt.Errorf("current scale-set demand has missing or mismatched identity, enabled state or statistics")
	}
	desired := remote.Statistics.TotalAssignedJobs
	if desired < 0 || remote.Statistics.TotalRunningJobs < 0 || remote.Statistics.TotalRunningJobs > desired {
		return current, fmt.Errorf("current scale-set demand has invalid job counts")
	}
	target := min(admitted, desired, int(scaleSet.MaxRunners))
	if target <= current {
		return current, nil
	}
	g.nextRead = time.Time{}
	return target, nil
}
