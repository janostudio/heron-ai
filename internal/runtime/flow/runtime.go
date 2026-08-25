package flow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Runtime owns FlowSession and FlowTurn lifecycle for the new Flow/Team
// runtime. It does not know about Stage or Task.
type Runtime struct {
	definitions *types.Definitions
	teams       types.TeamRuntime
	sessions    storage.SessionWriter
	evidence    storage.EvidenceStore
	workspace   string
	limits      types.RuntimeLimits
}

func NewRuntime(
	definitions *types.Definitions,
	teamRuntime types.TeamRuntime,
	sessionWriter storage.SessionWriter,
	evidenceStore storage.EvidenceStore,
	workspaceRoot string,
) *Runtime {
	return &Runtime{
		definitions: definitions,
		teams:       teamRuntime,
		sessions:    sessionWriter,
		evidence:    evidenceStore,
		workspace:   workspaceRoot,
		limits:      types.RuntimeLimits{}.WithDefaults(),
	}
}

func (r *Runtime) SetLimits(limits types.RuntimeLimits) {
	r.limits = limits.WithDefaults()
}

func (r *Runtime) Start(ctx context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	if err := r.validateDependencies(req.FlowID); err != nil {
		return types.FlowTurnResult{}, err
	}
	if strings.TrimSpace(req.FlowID) == "" {
		req.FlowID = r.definitions.Flow.ID
	}
	session := types.FlowSession{
		ID:        newID("fs"),
		FlowID:    req.FlowID,
		Status:    types.SessionCreated,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventFlowSessionCreated,
		FlowSessionID: session.ID,
		Payload:       map[string]any{"session": session},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}
	return r.runTurn(ctx, session, req.Input)
}

func (r *Runtime) HandleInput(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status == types.SessionCompleted ||
		session.Status == types.SessionFailed ||
		session.Status == types.SessionCancelled ||
		session.Status == types.SessionInterrupted {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is already %s", session.ID, session.Status)
	}
	if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has unfinished execution; use recovery status first", sessionID)
	}
	return r.runTurn(ctx, session, input)
}

func (r *Runtime) Resume(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status != types.SessionWaitingInput {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is not waiting for input", session.ID)
	}
	if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has unfinished execution; use recovery status first", sessionID)
	}
	return r.runTurn(ctx, session, input)
}

func (r *Runtime) RecoveryStatus(ctx context.Context, sessionID string) (types.RecoveryStatus, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.RecoveryStatus{}, err
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.RecoveryStatus{}, err
	}
	interrupted := interruptedExecutions(replay.Events)
	history := recoveryHistory(replay.Events)
	if len(interrupted) > 0 &&
		session.Status != types.SessionCompleted &&
		session.Status != types.SessionFailed &&
		session.Status != types.SessionCancelled {
		session.Status = types.SessionInterrupted
	}
	return types.RecoveryStatus{
		Session:         session,
		Interrupted:     interrupted,
		RecoveryHistory: history,
	}, nil
}

func (r *Runtime) Recover(ctx context.Context, sessionID string, req types.RecoveryRequest) (types.FlowTurnResult, error) {
	status, err := r.RecoveryStatus(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if req.Action == types.RecoveryInspect {
		return types.FlowTurnResult{
			Session: status.Session,
			Error:   recoverySummary(status.Interrupted),
		}, nil
	}
	if req.Action != types.RecoveryWait &&
		req.Action != types.RecoveryCoordinate &&
		req.Action != types.RecoveryRetry {
		return types.FlowTurnResult{}, fmt.Errorf("unsupported recovery action %q", req.Action)
	}
	if len(status.Interrupted) == 0 {
		return types.FlowTurnResult{Session: status.Session}, nil
	}

	target, err := selectInterrupted(status.Interrupted, req.TargetTurnID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if err := r.appendSessionEvent(ctx, sessionID, types.SessionEvent{
		Type:          types.EventRecoveryRequested,
		FlowSessionID: sessionID,
		TeamID:        target.TeamID,
		TeamTurnID:    target.TeamTurnID,
		MemberID:      target.MemberID,
		MemberTurnID:  target.MemberTurnID,
		Attempt:       target.Attempt + 1,
		RecoveryOf:    recoveryTargetID(target),
		Payload:       map[string]any{"request": req, "target": target},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}

	switch req.Action {
	case types.RecoveryWait:
		session, err := r.loadSession(ctx, sessionID)
		if err != nil {
			return types.FlowTurnResult{}, err
		}
		session.Status = types.SessionInterrupted
		session.UpdatedAt = time.Now().UTC()
		if err := r.appendSessionEvent(ctx, sessionID, types.SessionEvent{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: sessionID,
			Payload:       map[string]any{"session": session},
		}); err != nil {
			return types.FlowTurnResult{}, err
		}
		return types.FlowTurnResult{Session: session, Error: target.RetryReason}, nil
	case types.RecoveryCoordinate:
		if target.TeamID == "" {
			return types.FlowTurnResult{}, fmt.Errorf("interrupted %s cannot coordinate because no Team was recorded", target.Kind)
		}
		result, runErr := r.runTurnWithActivations(ctx, status.Session, recoveryInput(req, target), []activation{{
			teamID:     coordinatorID(r.definitions.Flow),
			callerTeam: target.CallerTeam,
			force:      true,
			attempt:    target.Attempt + 1,
			recoveryOf: recoveryTargetID(target),
		}})
		if runErr != nil {
			return result, runErr
		}
		return result, r.appendRecoveryCompleted(ctx, sessionID, target, req)
	case types.RecoveryRetry:
		if !req.AllowSideEffectReplay {
			return types.FlowTurnResult{}, fmt.Errorf("retry requires allow_side_effect_replay=true; replay may repeat workspace or external side effects")
		}
		if target.TeamID == "" {
			return types.FlowTurnResult{}, fmt.Errorf("interrupted %s cannot be retried because no Team was recorded", target.Kind)
		}
		result, runErr := r.runTurnWithActivations(ctx, status.Session, recoveryInput(req, target), []activation{{
			teamID:     target.TeamID,
			callerTeam: target.CallerTeam,
			force:      true,
			attempt:    target.Attempt + 1,
			recoveryOf: recoveryTargetID(target),
		}})
		if runErr != nil {
			return result, runErr
		}
		return result, r.appendRecoveryCompleted(ctx, sessionID, target, req)
	default:
		return types.FlowTurnResult{}, fmt.Errorf("unsupported recovery action %q", req.Action)
	}
}

func (r *Runtime) Cancel(ctx context.Context, sessionID string) error {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if session.Status == types.SessionCompleted ||
		session.Status == types.SessionFailed ||
		session.Status == types.SessionCancelled {
		return nil
	}
	session.Status = types.SessionCancelled
	session.UpdatedAt = time.Now().UTC()
	return r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventFlowSessionUpdated,
		FlowSessionID: session.ID,
		Payload:       map[string]any{"session": session},
	})
}

func (r *Runtime) Status(ctx context.Context, sessionID string) (types.FlowSession, error) {
	status, err := r.RecoveryStatus(ctx, sessionID)
	if err != nil {
		return types.FlowSession{}, err
	}
	return status.Session, nil
}

func (r *Runtime) runTurn(ctx context.Context, session types.FlowSession, input string) (types.FlowTurnResult, error) {
	return r.runTurnWithActivations(ctx, session, input, []activation{
		{teamID: r.definitions.Flow.EntryTeamID},
	})
}

func (r *Runtime) runTurnWithActivations(
	ctx context.Context,
	session types.FlowSession,
	input string,
	initial []activation,
) (types.FlowTurnResult, error) {
	if err := r.validateDependencies(session.FlowID); err != nil {
		return types.FlowTurnResult{}, err
	}
	if r.teams == nil {
		return types.FlowTurnResult{}, errors.New("team runtime is nil")
	}

	session.Status = types.SessionRunning
	session.UpdatedAt = time.Now().UTC()
	flowAttempt := 1
	recoveryOf := ""
	for _, initialActivation := range initial {
		if initialActivation.attempt > flowAttempt {
			flowAttempt = initialActivation.attempt
		}
		if recoveryOf == "" {
			recoveryOf = initialActivation.recoveryOf
		}
	}
	turn := types.FlowTurn{
		ID:            newID("ft"),
		FlowSessionID: session.ID,
		Attempt:       flowAttempt,
		RecoveryOf:    recoveryOf,
		Input:         input,
		Status:        types.TurnRunning,
		StartedAt:     time.Now().UTC(),
	}
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventFlowSessionUpdated,
		FlowSessionID: session.ID,
		FlowTurnID:    turn.ID,
		Payload:       map[string]any{"session": session},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventFlowTurnStarted,
		FlowSessionID: session.ID,
		FlowTurnID:    turn.ID,
		Attempt:       turn.Attempt,
		RecoveryOf:    turn.RecoveryOf,
		Payload: map[string]any{
			"turn":  turn,
			"input": input,
		},
	}); err != nil {
		return types.FlowTurnResult{}, err
	}

	result := types.FlowTurnResult{
		Session: session,
		Turn:    turn,
	}
	attempts := make(map[string]int)
	queue := make([]activation, 0, len(initial))
	enqueue := func(next activation) {
		if next.attempt <= 0 {
			attempts[next.teamID]++
			next.attempt = attempts[next.teamID]
		} else if next.attempt > attempts[next.teamID] {
			attempts[next.teamID] = next.attempt
		}
		queue = append(queue, next)
	}
	for _, activation := range initial {
		enqueue(activation)
	}
	completedTeams := make(map[string]bool)
	allRecords, err := r.loadHistoricalFlowRecords(ctx, session.ID)
	if err != nil {
		return r.finishTurn(ctx, result, types.SessionFailed, err)
	}
	teamTurns := 0

	for len(queue) > 0 {
		if err := contextErr(ctx); err != nil {
			return r.finishTurn(ctx, result, types.SessionFailed, err)
		}

		ready, blocked := takeReadyActivations(queue, completedTeams, r.definitions.Flow)
		ready, deferred := limitActivations(ready, r.limits.WithDefaults().MaxParallelTeams)
		queue = append(deferred, blocked...)
		if len(ready) == 0 {
			return r.finishTurn(ctx, result, types.SessionFailed, errors.New("flow activation graph cannot make progress"))
		}
		if teamTurns+len(ready) > r.limits.MaxTeamTurns {
			return r.finishTurn(ctx, result, types.SessionFailed, fmt.Errorf("flow turn exceeded max team turns: %d", r.limits.MaxTeamTurns))
		}
		teamTurns += len(ready)

		executions := make([]teamExecution, len(ready))
		var wg sync.WaitGroup
		var firstErr error
		var errMu sync.Mutex
		for i, current := range ready {
			wg.Add(1)
			go func(index int, current activation) {
				defer wg.Done()
				execution, executionErr := r.executeTeamTurn(
					ctx,
					session,
					turn,
					current,
					input,
					allRecords,
				)
				executions[index] = execution
				if executionErr != nil {
					errMu.Lock()
					if firstErr == nil {
						firstErr = executionErr
					}
					errMu.Unlock()
				}
			}(i, current)
		}
		wg.Wait()
		if firstErr != nil {
			return r.finishTurn(ctx, result, types.SessionFailed, firstErr)
		}

		for _, execution := range executions {
			teamResult := execution.result
			result.TeamResults = append(result.TeamResults, teamResult)
			result.Reply = joinReply(result.Reply, teamResult.Reply)
			allRecords = append(allRecords, teamResult.Records...)
			result.Records = append(result.Records, teamResult.Records...)
			completedTeams[execution.activation.teamID] = teamResult.Error == ""
		}

		// Resolve all routes after the batch has finished. A parallel batch
		// must not terminate on the first branch's complete/wait decision and
		// accidentally discard sibling results.
		hasWaitInput := false
		hasComplete := false
		var routeFailure error
		var resolvedRoute *types.Route
		for _, execution := range executions {
			next := execution.next
			if next == nil {
				next = &types.Route{Action: types.NextProceed}
			}
			resolvedRoute = next
			switch next.Action {
			case types.NextProceed:
				targets := fixedTargets(execution.binding)
				if len(targets) == 0 {
					if execution.activation.teamID == coordinatorID(r.definitions.Flow) {
						hasWaitInput = true
						continue
					}
					targets = []string{coordinatorID(r.definitions.Flow)}
				}
				for _, target := range targets {
					enqueue(activation{teamID: target, callerTeam: execution.activation.teamID})
				}
			case types.NextActivate:
				if !execution.binding.Coordinator {
					routeFailure = fmt.Errorf("team %q cannot activate other teams", execution.activation.teamID)
					continue
				}
				if err := validateActivation(execution.binding, r.definitions.Flow, next.Teams); err != nil {
					routeFailure = err
					continue
				}
				for _, target := range next.Teams {
					enqueue(activation{teamID: target, callerTeam: execution.activation.teamID})
				}
			case types.NextReturn:
				target := next.CallerTeam
				if target == "" {
					target = execution.activation.callerTeam
				}
				if target == "" {
					target = coordinatorID(r.definitions.Flow)
				}
				enqueue(activation{teamID: target, callerTeam: execution.activation.teamID})
			case types.NextCoordinate:
				enqueue(activation{teamID: coordinatorID(r.definitions.Flow), callerTeam: execution.activation.teamID})
			case types.NextWaitInput:
				hasWaitInput = true
			case types.NextComplete:
				hasComplete = true
			case types.NextFail:
				if routeFailure == nil {
					routeFailure = errors.New(next.Reason)
				}
			default:
				if routeFailure == nil {
					routeFailure = fmt.Errorf("unsupported next action %q", next.Action)
				}
			}
		}
		if routeFailure != nil {
			return r.finishTurn(ctx, result, types.SessionFailed, routeFailure)
		}
		result.Turn.Next = resolvedRoute
		if len(queue) == 0 {
			result.Turn.RecordIDs = recordIDs(result.Records)
			switch {
			case hasWaitInput:
				return r.finishTurn(ctx, result, types.SessionWaitingInput, nil)
			case hasComplete:
				return r.finishTurn(ctx, result, types.SessionCompleted, nil)
			}
		}
	}

	result.Turn.RecordIDs = recordIDs(result.Records)
	return r.finishTurn(ctx, result, types.SessionCompleted, nil)
}

type activation struct {
	teamID     string
	callerTeam string
	force      bool
	attempt    int
	recoveryOf string
}

type teamExecution struct {
	activation activation
	binding    types.FlowTeamBinding
	result     types.TeamTurnResult
	next       *types.Route
}

func takeReadyActivations(
	queue []activation,
	completed map[string]bool,
	flow types.Flow,
) ([]activation, []activation) {
	ready := make([]activation, 0, len(queue))
	blocked := make([]activation, 0, len(queue))
	for _, current := range queue {
		binding, ok := flow.Teams[current.teamID]
		if current.force || !ok || dependenciesCompleted(binding.DependsOn, completed) {
			ready = append(ready, current)
			continue
		}
		blocked = append(blocked, current)
	}
	return ready, blocked
}

func limitActivations(items []activation, max int) ([]activation, []activation) {
	if max <= 0 || len(items) <= max {
		return items, nil
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].teamID == items[j].teamID {
			return items[i].attempt < items[j].attempt
		}
		return items[i].teamID < items[j].teamID
	})
	return items[:max], items[max:]
}

func (r *Runtime) executeTeamTurn(
	ctx context.Context,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	current activation,
	input string,
	records []types.SharedRecord,
) (teamExecution, error) {
	execution := teamExecution{activation: current}

	binding, ok := r.definitions.Flow.Teams[current.teamID]
	if !ok {
		return execution, fmt.Errorf("team %q is not configured in flow", current.teamID)
	}
	execution.binding = binding

	team, ok := r.definitions.Teams[binding.TeamID]
	if !ok {
		return execution, fmt.Errorf("team definition %q not found", binding.TeamID)
	}

	teamSession := types.TeamSession{
		ID:            fmt.Sprintf("%s:%s", session.ID, current.teamID),
		FlowSessionID: session.ID,
		TeamID:        current.teamID,
		Status:        types.SessionRunning,
		CreatedAt:     session.CreatedAt,
		UpdatedAt:     time.Now().UTC(),
	}
	teamTurn := types.TeamTurn{
		ID:            newID("tt"),
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamID:        current.teamID,
		Attempt:       current.attempt,
		RecoveryOf:    current.recoveryOf,
		CallerTeam:    current.callerTeam,
		Status:        types.TurnRunning,
		StartedAt:     time.Now().UTC(),
	}

	if err := r.appendTeamStartedEvents(ctx, session.ID, flowTurn, teamSession, teamTurn, team); err != nil {
		return execution, err
	}
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventTeamTurnStarted,
		FlowSessionID: session.ID,
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamTurnID:    teamTurn.ID,
		TeamID:        teamTurn.TeamID,
		Attempt:       teamTurn.Attempt,
		RecoveryOf:    teamTurn.RecoveryOf,
		Payload: map[string]any{
			"team_turn": teamTurn,
			"input":     teamInput(input, binding.Inputs),
		},
	}); err != nil {
		return execution, err
	}

	teamResult, runErr := r.teams.Run(ctx, types.TeamTurnRequest{
		FlowSession:   session,
		FlowTurn:      flowTurn,
		TeamSession:   teamSession,
		TeamTurn:      teamTurn,
		Binding:       binding,
		Team:          team,
		Input:         teamInput(input, binding.Inputs),
		Records:       selectFlowRecords(records, binding.Inputs),
		WorkspaceRoot: r.workspace,
		Limits:        r.limits,
	})
	if runErr != nil {
		teamResult.Error = runErr.Error()
		if teamResult.Next == nil {
			teamResult.Next = &types.Route{
				Action: types.NextCoordinate,
				Reason: runErr.Error(),
			}
		}
	}

	next := teamResult.Next
	if next == nil {
		next = &types.Route{Action: types.NextProceed}
	}
	if next.Action == types.NextProceed &&
		binding.OnProceed != nil &&
		binding.OnProceed.Action != "" &&
		binding.OnProceed.Action != types.NextProceed {
		next = binding.OnProceed
	}
	now := time.Now().UTC()
	teamResult.Next = next
	teamResult.Turn = teamTurn
	teamResult.Turn.Next = next
	teamResult.Turn.Status = types.TurnCompleted
	if teamResult.Error != "" {
		teamResult.Turn.Status = types.TurnFailed
	}
	teamResult.Turn.RecordIDs = recordIDs(teamResult.Records)
	teamResult.Turn.FinishedAt = &now

	for _, record := range teamResult.Records {
		if record.Scope == types.RecordScopeFlow && r.evidence != nil {
			if err := r.evidence.Publish(ctx, session.ID, record); err != nil {
				return execution, err
			}
		}
		if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
			Type:          types.EventSharedRecordPublished,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
			TeamSessionID: teamSession.ID,
			TeamTurnID:    teamTurn.ID,
			Payload:       map[string]any{"record": record},
		}); err != nil {
			return execution, err
		}
	}

	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventTeamTurnCompleted,
		FlowSessionID: session.ID,
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamTurnID:    teamTurn.ID,
		Payload:       map[string]any{"team_result": teamResult},
	}); err != nil {
		return execution, err
	}
	teamSession.Status = sessionStatusForTurn(teamResult.Turn.Status)
	teamSession.UpdatedAt = now
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          types.EventTeamSessionUpdated,
		FlowSessionID: session.ID,
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamTurnID:    teamTurn.ID,
		Attempt:       teamTurn.Attempt,
		RecoveryOf:    teamTurn.RecoveryOf,
		Payload:       map[string]any{"team_session": teamSession},
	}); err != nil {
		return execution, err
	}

	execution.result = teamResult
	execution.next = next
	// A Team-level execution error is represented in TeamTurnResult so the
	// coordinator can still receive a coordinate route. Only persistence or
	// runtime infrastructure errors abort the whole FlowTurn here.
	return execution, nil
}

func (r *Runtime) finishTurn(
	ctx context.Context,
	result types.FlowTurnResult,
	status types.SessionStatus,
	runErr error,
) (types.FlowTurnResult, error) {
	now := time.Now().UTC()
	result.Session.Status = status
	result.Session.UpdatedAt = now
	result.Turn.Status = turnStatusForSession(status)
	result.Turn.FinishedAt = &now
	if runErr != nil {
		result.Error = runErr.Error()
	}
	if err := r.appendSessionEvent(ctx, result.Session.ID, types.SessionEvent{
		Type:          types.EventFlowTurnCompleted,
		FlowSessionID: result.Session.ID,
		FlowTurnID:    result.Turn.ID,
		Payload:       map[string]any{"session": result.Session, "turn": result.Turn},
	}); err != nil {
		return result, err
	}
	return result, runErr
}

func (r *Runtime) loadSession(ctx context.Context, sessionID string) (types.FlowSession, error) {
	if r.sessions == nil {
		return types.FlowSession{}, errors.New("session writer is nil")
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowSession{}, err
	}
	var session types.FlowSession
	for _, event := range replay.Events {
		if event.Payload == nil {
			continue
		}
		if raw, ok := event.Payload["session"]; ok {
			if err := decodeValue(raw, &session); err != nil {
				return types.FlowSession{}, err
			}
		}
	}
	if session.ID == "" {
		return types.FlowSession{}, fmt.Errorf("session %q has no session state", sessionID)
	}
	return session, nil
}

func (r *Runtime) appendSessionEvent(ctx context.Context, sessionID string, event types.SessionEvent) error {
	if r.sessions == nil {
		return errors.New("session writer is nil")
	}
	_, err := r.sessions.Append(ctx, sessionID, event)
	return err
}

func (r *Runtime) loadHistoricalFlowRecords(ctx context.Context, sessionID string) ([]types.SharedRecord, error) {
	if r.evidence == nil {
		return nil, nil
	}
	records, err := r.evidence.List(ctx, sessionID, types.RecordScopeFlow)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	return records, err
}

func (r *Runtime) appendTeamStartedEvents(
	ctx context.Context,
	sessionID string,
	flowTurn types.FlowTurn,
	teamSession types.TeamSession,
	teamTurn types.TeamTurn,
	team types.Team,
) error {
	if err := r.appendSessionEvent(ctx, sessionID, types.SessionEvent{
		Type:          types.EventTeamSessionCreated,
		FlowSessionID: flowTurn.FlowSessionID,
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamTurnID:    teamTurn.ID,
		Payload:       map[string]any{"team_session": teamSession},
	}); err != nil {
		return err
	}
	return nil
}

func sessionStatusForTurn(status types.TurnStatus) types.SessionStatus {
	switch status {
	case types.TurnCompleted:
		return types.SessionCompleted
	case types.TurnWaitingInput:
		return types.SessionWaitingInput
	case types.TurnCancelled:
		return types.SessionCancelled
	case types.TurnFailed:
		return types.SessionFailed
	case types.TurnWaitingApproval:
		return types.SessionInterrupted
	default:
		return types.SessionRunning
	}
}

func interruptedExecutions(events []types.SessionEvent) []types.InterruptedExecution {
	type started struct {
		event types.SessionEvent
		input string
	}
	startedMembers := make(map[string]started)
	startedTeams := make(map[string]started)
	startedFlows := make(map[string]started)
	completedMembers := make(map[string]bool)
	completedTeams := make(map[string]bool)
	completedFlows := make(map[string]bool)
	resolvedMembers := make(map[string]bool)
	resolvedTeams := make(map[string]bool)
	resolvedFlows := make(map[string]bool)

	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnStarted:
			startedFlows[event.FlowTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventFlowTurnCompleted:
			completedFlows[event.FlowTurnID] = true
		case types.EventTeamTurnStarted:
			startedTeams[event.TeamTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventTeamTurnCompleted:
			completedTeams[event.TeamTurnID] = true
		case types.EventSubagentTurnStarted, types.EventCommandTurnStarted, types.EventWebhookTurnStarted:
			startedMembers[event.MemberTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventSubagentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted:
			completedMembers[event.MemberTurnID] = true
		case types.EventRecoveryCompleted:
			if event.MemberTurnID != "" {
				resolvedMembers[event.MemberTurnID] = true
			}
			if event.TeamTurnID != "" {
				resolvedTeams[event.TeamTurnID] = true
			}
			if event.FlowTurnID != "" {
				resolvedFlows[event.FlowTurnID] = true
			}
		}
	}

	var interrupted []types.InterruptedExecution
	interruptedTeamIDs := make(map[string]bool)
	interruptedFlowIDs := make(map[string]bool)
	for turnID, item := range startedMembers {
		if completedMembers[turnID] ||
			resolvedMembers[turnID] ||
			resolvedTeams[item.event.TeamTurnID] ||
			resolvedFlows[item.event.FlowTurnID] {
			continue
		}
		execution := interruptedFromEvent("member_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedTeamIDs[item.event.TeamTurnID] = true
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedTeams {
		if completedTeams[turnID] || resolvedTeams[turnID] || interruptedTeamIDs[turnID] {
			continue
		}
		execution := interruptedFromEvent("team_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedFlows {
		if completedFlows[turnID] || resolvedFlows[turnID] || interruptedFlowIDs[turnID] {
			continue
		}
		interrupted = append(interrupted, interruptedFromEvent("flow_turn", item.event, item.input))
	}
	sortInterrupted(interrupted)
	return interrupted
}

func interruptedFromEvent(kind string, event types.SessionEvent, input string) types.InterruptedExecution {
	safe := kind == "flow_turn" || kind == "team_turn"
	reason := "member execution may have side effects; inspect before retry"
	if safe {
		reason = "retrying the containing Team/Flow may repeat downstream work"
	}
	return types.InterruptedExecution{
		Kind:         kind,
		FlowTurnID:   event.FlowTurnID,
		TeamID:       event.TeamID,
		TeamTurnID:   event.TeamTurnID,
		MemberID:     event.MemberID,
		MemberTurnID: event.MemberTurnID,
		MemberType:   event.MemberType,
		Attempt:      event.Attempt,
		RecoveryOf:   event.RecoveryOf,
		StartedSeq:   event.Seq,
		StartedAt:    event.CreatedAt,
		Input:        input,
		CallerTeam:   payloadString(event.Payload, "caller_team"),
		StartedEvent: event.Type,
		SafeToRetry:  safe,
		RetryReason:  reason,
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return text
}

func sortInterrupted(items []types.InterruptedExecution) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].MemberTurnID+items[i].TeamTurnID+items[i].FlowTurnID <
			items[j].MemberTurnID+items[j].TeamTurnID+items[j].FlowTurnID
	})
}

func recoveryHistory(events []types.SessionEvent) []types.RecoveryEvent {
	history := make([]types.RecoveryEvent, 0)
	for _, event := range events {
		var status string
		switch event.Type {
		case types.EventRecoveryRequested:
			status = "requested"
		case types.EventRecoveryCompleted:
			status = "completed"
		default:
			continue
		}
		recovery, _ := event.Payload["request"].(map[string]any)
		if recovery == nil {
			recovery = map[string]any{}
		}
		action, _ := recovery["action"].(string)
		target, _ := recovery["target_turn_id"].(string)
		reason, _ := recovery["reason"].(string)
		history = append(history, types.RecoveryEvent{
			Seq:          event.Seq,
			EventID:      event.EventID,
			Status:       status,
			Action:       types.RecoveryAction(action),
			TargetTurnID: target,
			Attempt:      event.Attempt,
			Reason:       reason,
			CreatedAt:    event.CreatedAt,
		})
	}
	return history
}

func recoveryTargetID(target types.InterruptedExecution) string {
	if target.MemberTurnID != "" {
		return target.MemberTurnID
	}
	if target.TeamTurnID != "" {
		return target.TeamTurnID
	}
	return target.FlowTurnID
}

func selectInterrupted(items []types.InterruptedExecution, target string) (types.InterruptedExecution, error) {
	if target != "" {
		for _, item := range items {
			if item.MemberTurnID == target || item.TeamTurnID == target || item.FlowTurnID == target {
				return item, nil
			}
		}
		return types.InterruptedExecution{}, fmt.Errorf("interrupted turn %q not found", target)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	return types.InterruptedExecution{}, fmt.Errorf("multiple interrupted executions; target_turn_id is required")
}

func recoveryInput(req types.RecoveryRequest, target types.InterruptedExecution) string {
	if strings.TrimSpace(req.Input) != "" {
		return req.Input
	}
	if strings.TrimSpace(req.Reason) != "" {
		return req.Reason
	}
	return target.Input
}

func recoverySummary(items []types.InterruptedExecution) string {
	if len(items) == 0 {
		return ""
	}
	return fmt.Sprintf("%d interrupted execution(s) require explicit recovery action", len(items))
}

func (r *Runtime) appendRecoveryCompleted(
	ctx context.Context,
	sessionID string,
	target types.InterruptedExecution,
	request types.RecoveryRequest,
) error {
	return r.appendSessionEvent(ctx, sessionID, types.SessionEvent{
		Type:          types.EventRecoveryCompleted,
		FlowSessionID: sessionID,
		FlowTurnID:    target.FlowTurnID,
		TeamID:        target.TeamID,
		TeamTurnID:    target.TeamTurnID,
		MemberID:      target.MemberID,
		MemberTurnID:  target.MemberTurnID,
		MemberType:    target.MemberType,
		Attempt:       target.Attempt + 1,
		RecoveryOf:    recoveryTargetID(target),
		Payload:       map[string]any{"request": request, "target": target},
	})
}

func (r *Runtime) validateDependencies(flowID string) error {
	if r.definitions == nil {
		return errors.New("definitions are nil")
	}
	if flowID != "" && flowID != r.definitions.Flow.ID {
		return fmt.Errorf("flow %q is not loaded", flowID)
	}
	return r.definitions.Flow.ValidateWithTeams(r.definitions.Teams)
}

func dependenciesCompleted(dependencies []string, completed map[string]bool) bool {
	for _, dependency := range dependencies {
		if !completed[dependency] {
			return false
		}
	}
	return true
}

func fixedTargets(binding types.FlowTeamBinding) []string {
	if binding.OnProceed == nil {
		return nil
	}
	return append([]string(nil), binding.OnProceed.Teams...)
}

func teamInput(input string, spec types.InputSpec) string {
	if spec.UserMessage {
		return input
	}
	return ""
}

func selectFlowRecords(records []types.SharedRecord, spec types.InputSpec) []types.SharedRecord {
	if spec.FlowRecords == nil && spec.Records == nil {
		return nil
	}

	allowed := make(map[string]struct{})
	for _, name := range spec.FlowRecords {
		allowed[name] = struct{}{}
	}
	for _, binding := range spec.Records {
		if binding.Record != "" {
			allowed[binding.Record] = struct{}{}
		}
	}

	selected := make([]types.SharedRecord, 0, len(records))
	for _, record := range records {
		if _, ok := allowed[record.Name]; ok {
			selected = append(selected, record)
		}
	}
	return selected
}

func coordinatorID(flow types.Flow) string {
	for name, binding := range flow.Teams {
		if binding.Coordinator {
			return name
		}
	}
	return ""
}

func recordIDs(records []types.SharedRecord) []string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if record.RecordID != "" {
			ids = append(ids, record.RecordID)
		}
	}
	return ids
}

func validateActivation(binding types.FlowTeamBinding, flow types.Flow, targets []string) error {
	allowed := make(map[string]struct{}, len(binding.CanActivate))
	for _, target := range binding.CanActivate {
		allowed[target] = struct{}{}
	}
	for _, target := range targets {
		if _, ok := flow.Teams[target]; !ok {
			return fmt.Errorf("team %q activation target %q is not in Flow", binding.ID, target)
		}
		if _, ok := allowed[target]; !ok {
			return fmt.Errorf("team %q cannot activate %q", binding.ID, target)
		}
	}
	return nil
}

func turnStatusForSession(status types.SessionStatus) types.TurnStatus {
	switch status {
	case types.SessionWaitingInput:
		return types.TurnWaitingInput
	case types.SessionCompleted:
		return types.TurnCompleted
	case types.SessionCancelled:
		return types.TurnCancelled
	case types.SessionFailed:
		return types.TurnFailed
	default:
		return types.TurnRunning
	}
}

func joinReply(current, next string) string {
	if strings.TrimSpace(current) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return current
	}
	return current + "\n\n" + next
}

func decodeValue(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func newID(prefix string) string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}
