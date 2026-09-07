package scaleset

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	runnerErrors "github.com/cloudbase/garm-provider-common/errors"
	commonParams "github.com/cloudbase/garm-provider-common/params"
	"github.com/cloudbase/garm/locking"
	"github.com/cloudbase/garm/params"
	"github.com/google/go-github/v84/github"
)

const (
	idleRetirementMinAge       = 30 * time.Minute
	idleRetirementConfirmAfter = 30 * time.Second
	idleRetirementReadTimeout  = 20 * time.Second
)

// idleRetirementGate remembers the first eligible observation per agent ID so
// a later cycle can require two matching observations before RemoveRunner.
type idleRetirementGate struct {
	seen map[int64]idleObservation
}

type idleObservation struct {
	AgentID    int64
	Name       string
	ScaleSetID int
	FirstSeen  time.Time
}

type restBusyState int

const (
	restBusyUnknown restBusyState = iota
	restBusyFalse
	restBusyTrue
)

func actionsStatusOnline(status string) bool {
	return status == "online"
}

func actionsStatusString(status any) string {
	text, ok := status.(string)
	if !ok {
		return ""
	}
	return text
}

func restBusyFromPointer(busy *bool) restBusyState {
	if busy == nil {
		return restBusyUnknown
	}
	if *busy {
		return restBusyTrue
	}
	return restBusyFalse
}

// actionsListBusy is the GARM RunnerReference.Busy bool. Encoding Busy as a
// non-pointer means a missing Actions-service field unmarshals to false. That
// default is not proof the runner is idle.
func actionsListBusyUntrusted(_ bool) {}

type idleRetirementDecision struct {
	Eligible bool
	Ready    bool
	Reason   string
}

type idleRetirementEvidence struct {
	AgentID            int64
	Name               string
	ScaleSetID         int
	LocalStatus        params.RunnerStatus
	CreatedAt          time.Time
	MinIdle            uint
	IdleCount          int
	Now                time.Time
	RESTID             int64
	RESTName           string
	RESTStatus         string
	RESTBusy           *bool
	ActionsID          int64
	ActionsName        string
	ActionsScaleSetID  int
	ActionsEphemeral   bool
	ActionsEnabled     bool
	ActionsState       string
	ActionsStatus      string
	AssignedJobs       int
	AcquiredJobs       int
	AvailableJobs      int
	RunningJobs        int
	BusyRunners        int
	StatisticsPresent  bool
	MessageDemandKnown bool
	DemandSessionID    string
	DemandMessageID    int64
	DemandGeneration   uint64
}

func evaluateIdleRetirement(ev idleRetirementEvidence) idleRetirementDecision {
	if ev.AgentID <= 0 || ev.Name == "" || ev.ScaleSetID <= 0 {
		return idleRetirementDecision{Reason: "missing-identity"}
	}
	switch ev.LocalStatus {
	case params.RunnerPending, params.RunnerIdle:
	default:
		return idleRetirementDecision{Reason: "local-not-idle-or-pending"}
	}
	if ev.Now.Sub(ev.CreatedAt) < idleRetirementMinAge {
		return idleRetirementDecision{Reason: "too-young"}
	}
	if ev.IdleCount <= int(ev.MinIdle) {
		return idleRetirementDecision{Reason: "at-or-below-min-idle"}
	}
	if !ev.MessageDemandKnown {
		return idleRetirementDecision{Reason: "message-demand-unknown"}
	}
	if !ev.StatisticsPresent {
		return idleRetirementDecision{Reason: "scale-set-statistics-absent"}
	}
	if ev.AssignedJobs < 0 || ev.AcquiredJobs < 0 || ev.AvailableJobs < 0 || ev.RunningJobs < 0 || ev.BusyRunners < 0 {
		return idleRetirementDecision{Reason: "scale-set-statistics-invalid"}
	}
	if ev.AssignedJobs > 0 || ev.AcquiredJobs > 0 || ev.AvailableJobs > 0 || ev.RunningJobs > 0 || ev.BusyRunners > 0 {
		return idleRetirementDecision{Reason: "scale-set-has-demand"}
	}
	if ev.RESTID != ev.AgentID || ev.ActionsID != ev.AgentID ||
		ev.RESTName != ev.Name || ev.ActionsName != ev.Name ||
		ev.ActionsScaleSetID != ev.ScaleSetID {
		return idleRetirementDecision{Reason: "identity-mismatch"}
	}
	if !ev.ActionsEphemeral || !ev.ActionsEnabled || ev.ActionsState != "Provisioned" {
		return idleRetirementDecision{Reason: "not-provisioned-ephemeral"}
	}
	if ev.RESTStatus != "online" || !actionsStatusOnline(ev.ActionsStatus) {
		return idleRetirementDecision{Reason: "not-online"}
	}
	switch restBusyFromPointer(ev.RESTBusy) {
	case restBusyUnknown:
		return idleRetirementDecision{Reason: "rest-busy-omitted"}
	case restBusyTrue:
		return idleRetirementDecision{Reason: "rest-busy"}
	case restBusyFalse:
	default:
		return idleRetirementDecision{Reason: "rest-busy-omitted"}
	}
	return idleRetirementDecision{Eligible: true, Reason: "eligible"}
}

func (g *idleRetirementGate) confirm(ev idleRetirementEvidence) idleRetirementDecision {
	decision := evaluateIdleRetirement(ev)
	if !decision.Eligible {
		if g.seen != nil {
			delete(g.seen, ev.AgentID)
		}
		return decision
	}
	if g.seen == nil {
		g.seen = map[int64]idleObservation{}
	}
	prev, ok := g.seen[ev.AgentID]
	if !ok || prev.Name != ev.Name || prev.ScaleSetID != ev.ScaleSetID {
		g.seen[ev.AgentID] = idleObservation{
			AgentID: ev.AgentID, Name: ev.Name, ScaleSetID: ev.ScaleSetID, FirstSeen: ev.Now,
		}
		decision.Reason = "first-observation"
		return decision
	}
	if ev.Now.Sub(prev.FirstSeen) < idleRetirementConfirmAfter {
		decision.Reason = "confirmation-pending"
		return decision
	}
	decision.Ready = true
	decision.Reason = "confirmed"
	return decision
}

func (g *idleRetirementGate) forget(agentID int64) {
	if g.seen != nil {
		delete(g.seen, agentID)
	}
}

func (g *idleRetirementGate) prune(live map[int64]struct{}) {
	if g.seen == nil {
		return
	}
	for agentID := range g.seen {
		if _, ok := live[agentID]; !ok {
			delete(g.seen, agentID)
		}
	}
}

func idleRetirementLocalPlan(runners map[string]params.Instance, minIdle uint, now time.Time) (idleCount int, aged []params.Instance) {
	for _, runner := range runners {
		if providerRemovalProtected(runner.Status) || runner.AgentID <= 0 {
			continue
		}
		switch runner.RunnerStatus {
		case params.RunnerPending, params.RunnerIdle:
			idleCount++
			if now.Sub(runner.CreatedAt) >= idleRetirementMinAge {
				aged = append(aged, runner)
			}
		}
	}
	if idleCount <= int(minIdle) {
		return idleCount, nil
	}
	return idleCount, aged
}

func warnIdleRetirement(ctx context.Context, err error) {
	if err == nil {
		return
	}
	slog.WarnContext(ctx, "idle retirement failed; continuing runner consolidation", "error", err, "error_class", "idle-retirement")
}

type idleActionsClient interface {
	RemoveRunner(context.Context, int64) error
	GetRunner(context.Context, int64) (params.RunnerReference, error)
}

type idleRemoveOutcome string

const (
	idleRemoveMarkAbsent idleRemoveOutcome = "mark-absent"
	idleRemoveConflict   idleRemoveOutcome = "conflict"
	idleRemoveNone       idleRemoveOutcome = "none"
)

func decideIdleRemoveRunner(removeErr error, getErr error, getFound bool) (idleRemoveOutcome, error) {
	if removeErr != nil {
		if errors.Is(removeErr, runnerErrors.ErrNotFound) {
			return idleRemoveMarkAbsent, nil
		}
		var conflict interface{ Conflict() bool }
		if errors.As(removeErr, &conflict) || isConflictError(removeErr) {
			return idleRemoveConflict, nil
		}
		return idleRemoveNone, fmt.Errorf("Actions RemoveRunner: %w", removeErr)
	}
	if getFound {
		return idleRemoveNone, fmt.Errorf("Actions runner still present after RemoveRunner")
	}
	if getErr != nil && !errors.Is(getErr, runnerErrors.ErrNotFound) {
		return idleRemoveNone, fmt.Errorf("read back Actions runner: %w", getErr)
	}
	return idleRemoveMarkAbsent, nil
}

func applyIdleRemoveRunner(ctx context.Context, cli idleActionsClient, runnerID int64) (idleRemoveOutcome, error) {
	removeErr := cli.RemoveRunner(ctx, runnerID)
	getFound := false
	var getErr error
	if removeErr == nil {
		_, getErr = cli.GetRunner(ctx, runnerID)
		getFound = getErr == nil
	}
	return decideIdleRemoveRunner(removeErr, getErr, getFound)
}

func (w *Worker) restRunner(ctx context.Context, runnerID int64) (*github.Runner, error) {
	cli, err := w.GetScaleSetClient()
	if err != nil {
		return nil, err
	}
	ghCli, err := cli.GetGithubClient()
	if err != nil {
		return nil, err
	}
	entity := ghCli.GetEntity()
	type repoGetter interface {
		GetRunner(context.Context, string, string, int64) (*github.Runner, *github.Response, error)
	}
	type orgGetter interface {
		GetOrganizationRunner(context.Context, string, int64) (*github.Runner, *github.Response, error)
	}
	switch entity.EntityType {
	case params.ForgeEntityTypeRepository:
		getter, ok := ghCli.(repoGetter)
		if !ok {
			return nil, fmt.Errorf("github client does not expose repository GetRunner")
		}
		runner, _, err := getter.GetRunner(ctx, entity.Owner, entity.Name, runnerID)
		return runner, err
	case params.ForgeEntityTypeOrganization:
		getter, ok := ghCli.(orgGetter)
		if !ok {
			return nil, fmt.Errorf("github client does not expose organization GetRunner")
		}
		runner, _, err := getter.GetOrganizationRunner(ctx, entity.Owner, runnerID)
		return runner, err
	default:
		return nil, fmt.Errorf("entity type %s has no REST runner getter", entity.EntityType)
	}
}

func (w *Worker) retireExcessIdleCapacity() error {
	now := time.Now().UTC()
	live := make(map[int64]struct{}, len(w.runners))
	for _, runner := range w.runners {
		if runner.AgentID > 0 {
			live[runner.AgentID] = struct{}{}
		}
	}
	w.idleRetire.prune(live)
	idleCount, aged := idleRetirementLocalPlan(w.runners, w.scaleSet.MinIdleRunners, now)
	if len(aged) == 0 {
		return nil
	}
	cli, err := w.GetScaleSetClient()
	if err != nil {
		w.resetIdleObservations(aged)
		return fmt.Errorf("getting scale set client: %w", err)
	}
	readCtx, cancel := context.WithTimeout(w.ctx, idleRetirementReadTimeout)
	defer cancel()
	remote, err := cli.GetRunnerScaleSetByID(readCtx, w.scaleSet.ScaleSetID)
	if err != nil {
		w.resetIdleObservations(aged)
		return fmt.Errorf("read scale set for idle retirement: %w", err)
	}
	if remote.ID != w.scaleSet.ScaleSetID || remote.Name != w.scaleSet.Name ||
		remote.Enabled == nil || !*remote.Enabled || remote.Statistics == nil {
		w.resetIdleObservations(aged)
		return fmt.Errorf("idle retirement scale-set identity, enabled state or statistics missing")
	}

	for _, runner := range aged {
		actionsRunner, err := cli.GetRunner(readCtx, runner.AgentID)
		if err != nil {
			w.idleRetire.forget(runner.AgentID)
			if errors.Is(err, runnerErrors.ErrNotFound) {
				continue
			}
			slog.WarnContext(w.ctx, "idle retirement skipped candidate after Actions read failure", "runner_name", runner.Name, "agent_id", runner.AgentID, "error", err, "error_class", "idle-retirement")
			continue
		}
		restRunner, err := w.restRunner(readCtx, runner.AgentID)
		if err != nil {
			w.idleRetire.forget(runner.AgentID)
			slog.WarnContext(w.ctx, "idle retirement skipped candidate after REST read failure", "runner_name", runner.Name, "agent_id", runner.AgentID, "error", err, "error_class", "idle-retirement")
			continue
		}
		if restRunner == nil {
			w.idleRetire.forget(runner.AgentID)
			slog.WarnContext(w.ctx, "idle retirement skipped candidate after empty REST payload", "runner_name", runner.Name, "agent_id", runner.AgentID, "error_class", "idle-retirement")
			continue
		}
		actionsListBusyUntrusted(actionsRunner.Busy)
		nowUTC := now
		// Assigned demand for idle retirement comes from a recent MESSAGE
		// observation. GetRunnerScaleSetByID statistics remain fail-closed
		// corroboration of identity/enabled state, not assigned-demand authority.
		demand := w.liveMessageDemand()
		evidence := idleRetirementEvidence{
			AgentID:            runner.AgentID,
			Name:               runner.Name,
			ScaleSetID:         w.scaleSet.ScaleSetID,
			LocalStatus:        runner.RunnerStatus,
			CreatedAt:          runner.CreatedAt,
			MinIdle:            w.scaleSet.MinIdleRunners,
			IdleCount:          idleCount,
			Now:                now,
			RESTID:             restRunner.GetID(),
			RESTName:           restRunner.GetName(),
			RESTStatus:         restRunner.GetStatus(),
			RESTBusy:           restRunner.Busy,
			ActionsID:          actionsRunner.ID,
			ActionsName:        actionsRunner.Name,
			ActionsScaleSetID:  actionsRunner.RunnerScaleSetID,
			ActionsEphemeral:   actionsRunner.Ephemeral,
			ActionsEnabled:     actionsRunner.Enabled,
			ActionsState:       actionsRunner.ProvisioningState,
			ActionsStatus:      actionsStatusString(actionsRunner.Status),
			AssignedJobs:       demand.assigned,
			AcquiredJobs:       remote.Statistics.TotalAcquiredJobs,
			AvailableJobs:      remote.Statistics.TotalAvailableJobs,
			RunningJobs:        remote.Statistics.TotalRunningJobs,
			BusyRunners:        remote.Statistics.TotalBusyRunners,
			StatisticsPresent:  true,
			MessageDemandKnown: demand.idleFresh(nowUTC),
			DemandSessionID:    demand.sessionID,
			DemandMessageID:    demand.messageID,
			DemandGeneration:   demand.generation,
		}
		decision := w.idleRetire.confirm(evidence)
		if !decision.Eligible {
			slog.DebugContext(w.ctx, "idle retirement refused", "runner_name", runner.Name, "reason", decision.Reason)
			continue
		}
		if !decision.Ready {
			slog.InfoContext(w.ctx, "idle retirement first observation", "runner_name", runner.Name, "agent_id", runner.AgentID, "reason", decision.Reason)
			continue
		}
		if ok := locking.TryLock(runner.Name, w.consumerID); !ok {
			continue
		}
		lockedName := runner.Name
		unlockRunner := func() {
			if lockedName == "" {
				return
			}
			locking.Unlock(lockedName, false)
			lockedName = ""
		}
		latest := w.liveMessageDemand()
		if !latest.unchangedIdleZero(demand, time.Now().UTC()) {
			unlockRunner()
			w.idleRetire.forget(runner.AgentID)
			slog.InfoContext(w.ctx, "idle retirement skipped; live message demand changed before removal",
				"runner_name", runner.Name, "agent_id", runner.AgentID)
			continue
		}
		err = w.removeIdleRunnerAfterConfirmation(cli, runner)
		unlockRunner()
		if err != nil {
			w.idleRetire.forget(runner.AgentID)
			return err
		}
		w.idleRetire.forget(runner.AgentID)
		return nil
	}
	return nil
}

func (w *Worker) resetIdleObservations(runners []params.Instance) {
	for _, runner := range runners {
		w.idleRetire.forget(runner.AgentID)
	}
}

func applyConfirmedIdleRemoval(
	ctx context.Context,
	cli idleActionsClient,
	runner params.Instance,
	markAbsent func(params.Instance) error,
) (idleRemoveOutcome, error) {
	if markAbsent == nil {
		return idleRemoveNone, fmt.Errorf("idle pending-delete writer is required")
	}
	outcome, err := applyIdleRemoveRunner(ctx, cli, runner.AgentID)
	if err != nil {
		return outcome, err
	}
	if outcome == idleRemoveMarkAbsent {
		return outcome, markAbsent(runner)
	}
	return outcome, nil
}

func (w *Worker) removeIdleRunnerAfterConfirmation(cli idleActionsClient, runner params.Instance) error {
	readCtx, cancel := context.WithTimeout(w.ctx, idleRetirementReadTimeout)
	defer cancel()
	outcome, err := applyConfirmedIdleRemoval(readCtx, cli, runner, w.markIdleRunnerAbsent)
	if err != nil {
		return err
	}
	switch outcome {
	case idleRemoveConflict:
		slog.InfoContext(w.ctx, "idle retirement refused; Actions service reported conflict", "runner_name", runner.Name, "agent_id", runner.AgentID)
		w.idleRetire.forget(runner.AgentID)
	case idleRemoveMarkAbsent:
		slog.InfoContext(w.ctx, "retired excess idle runner from Actions service", "runner_name", runner.Name, "agent_id", runner.AgentID)
	}
	return nil
}

func (w *Worker) markIdleRunnerAbsent(runner params.Instance) error {
	instance, err := w.setRunnerDBStatus(runner.Name, commonParams.InstancePendingDelete)
	if err != nil {
		if errors.Is(err, runnerErrors.ErrNotFound) {
			return nil
		}
		terminal, refreshErr := w.runnerRemovalAlreadyOwned(runner.Name)
		if refreshErr != nil {
			return errors.Join(err, refreshErr)
		}
		if terminal {
			return nil
		}
		return err
	}
	w.runners[instance.ID] = instance
	return nil
}

func isConflictError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "conflict") || strings.Contains(message, "jobstillrunningexception")
}
