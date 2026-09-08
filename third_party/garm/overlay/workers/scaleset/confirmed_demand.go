package scaleset

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudbase/garm/params"
)

// liveMessageDemand is the last statistics.TotalAssignedJobs observed from the
// current MESSAGE session. It is not the database DesiredRunnerCount row:
// GORM may skip rewriting an unchanged value, UpdatedAt is not a heartbeat,
// and a zero persisted by a previous session is not a fresh observation.
type liveMessageDemand struct {
	sessionID  string
	assigned   int
	observed   bool
	messageID  int64
	observedAt time.Time
	generation uint64
}

const idleMessageDemandFreshFor = 2 * time.Minute

func (d liveMessageDemand) idleFresh(now time.Time) bool {
	if !d.observed || d.sessionID == "" || d.assigned < 0 || d.messageID <= 0 || d.observedAt.IsZero() {
		return false
	}
	return !now.Before(d.observedAt) && now.Sub(d.observedAt) <= idleMessageDemandFreshFor
}

// scalingKnown is the latest assigned counter from the current session. It is
// intentionally not age-gated: a long-poll 202/nil is not a heartbeat and must
// not expire legitimate in-progress scaling. Destructive idle retirement uses
// idleFresh instead.
func (d liveMessageDemand) scalingKnown() bool {
	return d.observed && d.sessionID != ""
}

func (d liveMessageDemand) unchangedIdleZero(previous liveMessageDemand, now time.Time) bool {
	if !d.idleFresh(now) || !previous.idleFresh(now) || d.assigned != 0 || previous.assigned != 0 {
		return false
	}
	return d.sessionID == previous.sessionID && d.messageID == previous.messageID && d.generation == previous.generation
}

// confirmedDemandGate bounds new JIT registrations by live MESSAGE statistics
// from the current listener session. Official scale-set scaling uses
// statistics.TotalAssignedJobs from session/message responses, not
// GetRunnerScaleSetByID metadata and not REST queued job counts.
//
// Unsupported freshness contract: a 202/nil long-poll does not carry
// statistics, so silence is not a heartbeat and not a fresh zero.
type confirmedDemandGate struct{}

func (g *confirmedDemandGate) target(_ context.Context, _ time.Time, demand liveMessageDemand, scaleSet params.ScaleSet, current, admitted int) (int, error) {
	if !scaleSet.Enabled || admitted <= current || admitted < 1 || scaleSet.MaxRunners < 1 {
		return current, nil
	}
	if !demand.scalingKnown() {
		return current, fmt.Errorf("current message demand is unknown")
	}
	if demand.assigned < 0 {
		return current, fmt.Errorf("current message demand is invalid")
	}
	target := min(admitted, demand.assigned, int(scaleSet.MaxRunners))
	if target <= current {
		return current, nil
	}
	return target, nil
}
