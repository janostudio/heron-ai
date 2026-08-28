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
	tasks       types.ToolTaskStore
	media       types.MediaStore
	resumeMu    sync.Mutex
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

func (r *Runtime) SetTaskStore(tasks types.ToolTaskStore) {
	r.tasks = tasks
}

func (r *Runtime) SetMediaStore(store types.MediaStore) {
	r.media = store
}

func (r *Runtime) persistContextBlocks(ctx context.Context, blocks []types.ContextBlock) ([]types.ContextBlock, error) {
	if len(blocks) == 0 || r.media == nil {
		return blocks, nil
	}
	result := make([]types.ContextBlock, len(blocks))
	for i, block := range blocks {
		result[i] = block
		if len(block.Parts) == 0 {
			continue
		}
		result[i].Parts = make([]types.ContentPart, len(block.Parts))
		for j, part := range block.Parts {
			result[i].Parts[j] = part
			if part.Media == nil {
				continue
			}
			stored, err := r.media.Store(ctx, *part.Media)
			if err != nil {
				return nil, err
			}
			result[i].Parts[j].Media = &stored
		}
	}
	return result, nil
}

func (r *Runtime) Start(ctx context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	if err := r.validateDependencies(req.FlowID); err != nil {
		return types.FlowTurnResult{}, err
	}
	if strings.TrimSpace(req.FlowID) == "" {
		req.FlowID = r.definitions.Flow.ID
	}
	blocks, err := r.persistContextBlocks(ctx, req.ContextBlocks)
	if err != nil {
		return types.FlowTurnResult{}, err
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
	return r.runTurnWithContext(ctx, session, req.Input, blocks)
}

func (r *Runtime) HandleInput(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	return r.HandleInputWithContext(ctx, sessionID, input, nil)
}

func (r *Runtime) HandleInputWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	// Completed is deliberately absent from the rejection list: it only
	// exists in sessions recorded before the lifecycle refactor, and those
	// legacy sessions stay continuable with the same session_id. A normally
	// finished turn is always left in waiting_input.
	if session.Status == types.SessionFailed ||
		session.Status == types.SessionCancelled ||
		session.Status == types.SessionInterrupted ||
		session.Status == types.SessionWaitingApproval {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is already %s", session.ID, session.Status)
	}
	if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has unfinished execution; use recovery status first", sessionID)
	}
	persisted, err := r.persistContextBlocks(ctx, blocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	return r.runTurnWithContext(ctx, session, input, persisted)
}

func (r *Runtime) Resume(ctx context.Context, sessionID string, input string) (types.FlowTurnResult, error) {
	return r.ResumeWithContext(ctx, sessionID, input, nil)
}

func (r *Runtime) ResumeWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status == types.SessionWaitingApproval {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is waiting for approval; approval resume is not configured", session.ID)
	}
	if session.Status != types.SessionWaitingInput &&
		session.Status != types.SessionWaitingTool {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is not waiting for input or tool", session.ID)
	}
	if interrupted, err := r.RecoveryStatus(ctx, sessionID); err == nil && len(interrupted.Interrupted) > 0 {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has unfinished execution; use recovery status first", sessionID)
	}
	persistedBlocks, err := r.persistContextBlocks(ctx, blocks)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	pending, ok := pendingAgentResume(replay.Events)
	if pendingTeam, teamOK := pendingTeamResume(replay.Events); teamOK {
		if pendingTeam.FlowTurnID != "" {
			if ready, err := r.pendingTasksReady(ctx, pendingTeam.PendingToolTasks); err != nil {
				return types.FlowTurnResult{}, err
			} else if !ready {
				return types.FlowTurnResult{
					Session:          session,
					PendingToolTasks: pendingTeam.PendingToolTasks,
					Turn: types.FlowTurn{
						FlowSessionID: session.ID,
						Status:        types.TurnWaitingTool,
					},
				}, nil
			}
		}
		resumeResults := make(map[string]types.CallResult)
		for callID, callResult := range pendingTeam.CallResults {
			if callResult.Status == types.TurnCompleted {
				resumeResults[callID] = callResult
			}
		}
		resumeCalls := make(map[string]types.TeamCallResume, len(pendingTeam.PendingToolTasks))
		for _, pendingTask := range pendingTeam.PendingToolTasks {
			resumeCalls[pendingTask.CallID] = types.TeamCallResume{
				CallTurnID:   pendingTask.CallTurnID,
				CheckpointID: pendingTask.CheckpointID,
				TaskID:       pendingTask.TaskID,
			}
		}
		return r.runTurnWithActivationsAndContext(ctx, session, input, persistedBlocks, []activation{{
			teamID:        pendingTeam.TeamID,
			callerTeam:    pendingTeam.CallerTeam,
			force:         true,
			attempt:       pendingTeam.Attempt + 1,
			resumeResults: resumeResults,
			resumeCalls:   resumeCalls,
		}})
	}
	if !ok {
		return r.runTurnWithContext(ctx, session, input, persistedBlocks)
	}
	return r.runTurnWithActivationsAndContext(ctx, session, input, persistedBlocks, []activation{{
		teamID:             pending.TeamID,
		callerTeam:         pending.CallerTeam,
		force:              true,
		attempt:            pending.Attempt + 1,
		resumeCallID:       pending.CallID,
		resumeCheckpointID: pending.CheckpointID,
		resumeInput:        input,
		resumeCallTurnID:   pending.CallTurnID,
		resumeTaskID:       pending.TaskID,
	}})
}

func (r *Runtime) ResumeApproval(ctx context.Context, sessionID, approvalID string, approved bool, reason string) (types.FlowTurnResult, error) {
	return r.ResumeApprovalWithResponse(ctx, sessionID, types.HITLResponse{
		RequestID: approvalID,
		Approved:  approved,
		Reason:    reason,
		Channel:   "http",
	})
}

// ResumeApprovalWithResponse is the auditable approval entry point. Approval
// requests are durable and intentionally never expire; callers decide in
// order by passing the request ID found in the latest waiting state.
func (r *Runtime) ResumeApprovalWithResponse(ctx context.Context, sessionID string, decision types.HITLResponse) (types.FlowTurnResult, error) {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	if session.Status != types.SessionWaitingApproval {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q is not waiting for approval", sessionID)
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	pending, ok := pendingTeamResume(replay.Events)
	if !ok {
		return types.FlowTurnResult{}, fmt.Errorf("flow session %q has no pending approval", sessionID)
	}
	if len(pending.PendingApprovals) > 0 &&
		pending.PendingApprovals[0].RequestID != decision.RequestID {
		return types.FlowTurnResult{}, fmt.Errorf(
			"approval %q is not pending or is not next; process approval %q first",
			decision.RequestID, pending.PendingApprovals[0].RequestID,
		)
	}
	for callID, callResult := range pending.CallResults {
		if callResult.Status != types.TurnWaitingApproval ||
			callResult.PendingApproval == nil ||
			callResult.PendingApproval.RequestID != decision.RequestID {
			continue
		}
		resumeResults := make(map[string]types.CallResult)
		for siblingID, sibling := range pending.CallResults {
			if siblingID != callID && sibling.Status == types.TurnCompleted {
				resumeResults[siblingID] = sibling
			}
		}
		if decision.DecidedAt.IsZero() {
			decision.DecidedAt = time.Now().UTC()
		}
		return r.runTurnWithActivations(ctx, session, "", []activation{{
			teamID:             pending.TeamID,
			callerTeam:         pending.CallerTeam,
			force:              true,
			attempt:            pending.Attempt + 1,
			resumeCallID:       callID,
			resumeCheckpointID: callResult.CheckpointID,
			resumeCallTurnID:   callResult.CallTurnID,
			resumeApprovalID:   decision.RequestID,
			resumeApproval:     &decision,
			resumeResults:      resumeResults,
			resumeCalls: map[string]types.TeamCallResume{
				callID: {
					CallTurnID:   callResult.CallTurnID,
					CheckpointID: callResult.CheckpointID,
					ApprovalID:   decision.RequestID,
					Approval:     &decision,
				},
			},
		}})
	}
	return types.FlowTurnResult{}, fmt.Errorf("approval %q is not pending in flow session %q", decision.RequestID, sessionID)
}

func (r *Runtime) pendingTasksReady(ctx context.Context, pending []types.PendingToolTask) (bool, error) {
	if len(pending) == 0 {
		return true, nil
	}
	if r.tasks == nil {
		return false, errors.New("tool task store is not configured")
	}
	for _, item := range pending {
		if item.TaskID == "" {
			return false, fmt.Errorf("pending Tool task for call %q has no task id", item.CallID)
		}
		task, err := r.tasks.Load(ctx, item.TaskID)
		if err != nil {
			return false, err
		}
		if task.Status == types.ToolTaskQueued || task.Status == types.ToolTaskRunning {
			return false, nil
		}
	}
	return true, nil
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
		CallID:        target.CallID,
		CallTurnID:    target.CallTurnID,
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
	return r.runTurnWithContext(ctx, session, input, nil)
}

func (r *Runtime) runTurnWithContext(ctx context.Context, session types.FlowSession, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	return r.runTurnWithActivationsAndContext(ctx, session, input, blocks, []activation{
		{teamID: r.definitions.Flow.EntryTeamID},
	})
}

func (r *Runtime) runTurnWithActivations(
	ctx context.Context,
	session types.FlowSession,
	input string,
	initial []activation,
) (types.FlowTurnResult, error) {
	return r.runTurnWithActivationsAndContext(ctx, session, input, nil, initial)
}

func (r *Runtime) runTurnWithActivationsAndContext(
	ctx context.Context,
	session types.FlowSession,
	input string,
	blocks []types.ContextBlock,
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
		ContextBlocks: blocks,
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
					blocks,
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
			result.PendingToolTasks = append(result.PendingToolTasks, teamResult.PendingToolTasks...)
			result.PendingApprovals = append(result.PendingApprovals, teamResult.PendingApprovals...)
			result.Reply = joinReply(result.Reply, teamResult.Reply)
			allRecords = append(allRecords, teamResult.Records...)
			result.Records = append(result.Records, teamResult.Records...)
			completedTeams[execution.activation.teamID] = teamResult.Error == ""
		}

		// Resolve all routes after the batch has finished. A parallel batch
		// must not terminate on the first branch's wait decision and
		// accidentally discard sibling results.
		hasWaitInput := false
		hasWaitTool := false
		hasWaitApproval := false
		var routeFailure error
		var resolvedRoute *types.Route
		for _, execution := range executions {
			next := execution.next
			if next == nil {
				next = &types.Route{Action: types.NextProceed}
			}
			resolvedRoute = next
			if execution.result.Turn.Status == types.TurnWaitingInput {
				// The Team paused mid-execution for user input. Do not route
				// it onward; the FlowTurn ends resumable instead.
				hasWaitInput = true
				continue
			}
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
			case types.NextWaitTool:
				hasWaitTool = true
			case types.NextWaitApproval:
				hasWaitApproval = true
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
			case hasWaitApproval:
				return r.finishTurn(ctx, result, types.SessionWaitingApproval, nil)
			case hasWaitTool:
				return r.finishTurn(ctx, result, types.SessionWaitingTool, nil)
			case hasWaitInput:
				return r.finishTurn(ctx, result, types.SessionWaitingInput, nil)
			}
		}
	}

	result.Turn.RecordIDs = recordIDs(result.Records)
	// A normally finished FlowTurn stays continuable: the session lifecycle
	// is a runtime decision, not a Flow configuration.
	return r.finishTurn(ctx, result, types.SessionWaitingInput, nil)
}

type activation struct {
	teamID             string
	callerTeam         string
	force              bool
	attempt            int
	recoveryOf         string
	resumeCallID       string
	resumeCheckpointID string
	resumeInput        string
	resumeCallTurnID   string
	resumeTaskID       string
	resumeApprovalID   string
	resumeApproval     *types.HITLResponse
	resumeResults      map[string]types.CallResult
	resumeCalls        map[string]types.TeamCallResume
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
	blocks []types.ContextBlock,
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
		FlowSession:        session,
		FlowTurn:           flowTurn,
		TeamSession:        teamSession,
		TeamTurn:           teamTurn,
		Binding:            binding,
		Team:               team,
		Input:              teamInput(input, binding.Inputs),
		ContextBlocks:      blocks,
		Records:            selectFlowRecords(records, binding.Inputs),
		WorkspaceRoot:      r.workspace,
		Limits:             r.limits,
		ResumeCallID:       current.resumeCallID,
		ResumeCheckpointID: current.resumeCheckpointID,
		ResumeInput:        current.resumeInput,
		ResumeCallTurnID:   current.resumeCallTurnID,
		ResumeTaskID:       current.resumeTaskID,
		ResumeApprovalID:   current.resumeApprovalID,
		ResumeApproval:     current.resumeApproval,
		ResumeResults:      current.resumeResults,
		ResumeCalls:        current.resumeCalls,
	})
	if runErr != nil {
		teamResult.Error = runErr.Error()
		if teamResult.Next == nil {
			teamResult.Next = &types.Route{Action: types.NextCoordinate, Reason: runErr.Error()}
		}
		// A coordinator cannot coordinate itself after it has failed. Without
		// this guard a failed single-Team Flow loops until MaxTeamTurns.
		if teamResult.Next.Action == types.NextCoordinate &&
			current.teamID == coordinatorID(r.definitions.Flow) {
			teamResult.Next = &types.Route{Action: types.NextFail, Reason: runErr.Error()}
		}
		teamResult.Turn.Status = types.TurnFailed
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
	// Capture the Team runtime's turn status before replacing the TeamTurn
	// record below: the flow layer owns the record, but only the Team knows
	// whether it paused mid-execution (for example through the
	// AskUserQuestion Tool).
	teamTurnStatus := teamResult.Turn.Status
	teamResult.Next = next
	teamResult.Turn = teamTurn
	teamResult.Turn.Next = next
	waitingInput := teamTurnStatus == types.TurnWaitingInput
	waitingTool := teamTurnStatus == types.TurnWaitingTool ||
		next.Action == types.NextWaitTool
	waitingApproval := teamTurnStatus == types.TurnWaitingApproval ||
		next.Action == types.NextWaitApproval
	teamResult.Turn.Status = types.TurnCompleted
	if waitingApproval {
		teamResult.Turn.Status = types.TurnWaitingApproval
	} else if waitingTool {
		teamResult.Turn.Status = types.TurnWaitingTool
	} else if waitingInput {
		teamResult.Turn.Status = types.TurnWaitingInput
	}
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

	eventType := types.EventTeamTurnCompleted
	if waitingApproval && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingApproval
	} else if waitingTool && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingTool
	} else if waitingInput && teamResult.Error == "" {
		eventType = types.EventTeamTurnWaitingInput
	}
	if err := r.appendSessionEvent(ctx, session.ID, types.SessionEvent{
		Type:          eventType,
		FlowSessionID: session.ID,
		FlowTurnID:    flowTurn.ID,
		TeamSessionID: teamSession.ID,
		TeamTurnID:    teamTurn.ID,
		TeamID:        teamTurn.TeamID,
		Attempt:       teamTurn.Attempt,
		RecoveryOf:    teamTurn.RecoveryOf,
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
	eventType := types.EventFlowTurnCompleted
	switch status {
	case types.SessionWaitingInput:
		eventType = types.EventFlowTurnWaitingInput
	case types.SessionWaitingTool:
		eventType = types.EventFlowTurnWaitingTool
	case types.SessionWaitingApproval:
		eventType = types.EventFlowTurnWaitingApproval
	}
	if err := r.appendSessionEvent(ctx, result.Session.ID, types.SessionEvent{
		Type:          eventType,
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
	case types.TurnWaitingTool:
		return types.SessionWaitingTool
	case types.TurnCancelled:
		return types.SessionCancelled
	case types.TurnFailed:
		return types.SessionFailed
	case types.TurnWaitingApproval:
		return types.SessionWaitingApproval
	default:
		return types.SessionRunning
	}
}

func interruptedExecutions(events []types.SessionEvent) []types.InterruptedExecution {
	type started struct {
		event types.SessionEvent
		input string
	}
	startedCalls := make(map[string]started)
	startedTeams := make(map[string]started)
	startedFlows := make(map[string]started)
	completedCalls := make(map[string]bool)
	completedTeams := make(map[string]bool)
	completedFlows := make(map[string]bool)
	waitingCalls := make(map[string]bool)
	waitingTeams := make(map[string]bool)
	waitingFlows := make(map[string]bool)
	resolvedCalls := make(map[string]bool)
	resolvedTeams := make(map[string]bool)
	resolvedFlows := make(map[string]bool)

	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnStarted:
			startedFlows[event.FlowTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventFlowTurnCompleted:
			completedFlows[event.FlowTurnID] = true
		case types.EventFlowTurnWaitingInput, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
			// A FlowTurn that ended waiting is not interrupted; it stays
			// resumable by design.
			waitingFlows[event.FlowTurnID] = true
		case types.EventTeamTurnStarted:
			startedTeams[event.TeamTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventTeamTurnCompleted:
			completedTeams[event.TeamTurnID] = true
		case types.EventTeamTurnWaitingInput, types.EventTeamTurnWaitingTool, types.EventTeamTurnWaitingApproval:
			waitingTeams[event.TeamTurnID] = true
			waitingFlows[event.FlowTurnID] = true
		case types.EventAgentTurnStarted, types.EventCommandTurnStarted, types.EventWebhookTurnStarted:
			startedCalls[event.CallTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventAgentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted:
			completedCalls[event.CallTurnID] = true
		case types.EventAgentTurnWaitingInput, types.EventAgentTurnWaitingTool, types.EventAgentTurnWaitingApproval:
			waitingCalls[event.CallTurnID] = true
			waitingTeams[event.TeamTurnID] = true
			waitingFlows[event.FlowTurnID] = true
		case types.EventRecoveryCompleted:
			if event.CallTurnID != "" {
				resolvedCalls[event.CallTurnID] = true
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
	for turnID, item := range startedCalls {
		if completedCalls[turnID] ||
			waitingCalls[turnID] ||
			resolvedCalls[turnID] ||
			resolvedTeams[item.event.TeamTurnID] ||
			resolvedFlows[item.event.FlowTurnID] {
			continue
		}
		execution := interruptedFromEvent("call_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedTeamIDs[item.event.TeamTurnID] = true
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedTeams {
		if completedTeams[turnID] || waitingTeams[turnID] || resolvedTeams[turnID] || interruptedTeamIDs[turnID] {
			continue
		}
		execution := interruptedFromEvent("team_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedFlows {
		if completedFlows[turnID] || waitingFlows[turnID] || resolvedFlows[turnID] || interruptedFlowIDs[turnID] {
			continue
		}
		interrupted = append(interrupted, interruptedFromEvent("flow_turn", item.event, item.input))
	}
	sortInterrupted(interrupted)
	return interrupted
}

func interruptedFromEvent(kind string, event types.SessionEvent, input string) types.InterruptedExecution {
	safe := kind == "flow_turn" || kind == "team_turn"
	reason := "call execution may have side effects; inspect before retry"
	if safe {
		reason = "retrying the containing Team/Flow may repeat downstream work"
	}
	return types.InterruptedExecution{
		Kind:         kind,
		FlowTurnID:   event.FlowTurnID,
		TeamID:       event.TeamID,
		TeamTurnID:   event.TeamTurnID,
		CallID:       event.CallID,
		CallTurnID:   event.CallTurnID,
		CallType:     event.CallType,
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

type pendingAgentResumeInfo struct {
	FlowTurnID   string
	TeamID       string
	TeamTurnID   string
	CallID       string
	CallTurnID   string
	CallerTeam   string
	Attempt      int
	CheckpointID string
	TaskID       string
	Status       types.TurnStatus
}

type pendingTeamResumeInfo struct {
	FlowTurnID       string
	TeamID           string
	TeamTurnID       string
	CallerTeam       string
	Attempt          int
	CallResults      map[string]types.CallResult
	PendingToolTasks []types.PendingToolTask
	PendingApprovals []types.AgentPendingApproval
}

func pendingTeamResume(events []types.SessionEvent) (pendingTeamResumeInfo, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != types.EventTeamTurnWaitingTool &&
			event.Type != types.EventTeamTurnWaitingApproval {
			continue
		}
		info := pendingTeamResumeInfo{
			FlowTurnID:  event.FlowTurnID,
			TeamID:      event.TeamID,
			TeamTurnID:  event.TeamTurnID,
			CallerTeam:  payloadString(event.Payload, "caller_team"),
			Attempt:     event.Attempt,
			CallResults: make(map[string]types.CallResult),
		}
		if raw, ok := event.Payload["team_result"]; ok {
			var result types.TeamTurnResult
			if err := decodeValue(raw, &result); err == nil {
				info.TeamID = result.Turn.TeamID
				if info.TeamID == "" {
					info.TeamID = event.TeamID
				}
				if info.TeamTurnID == "" {
					info.TeamTurnID = result.Turn.ID
				}
				if info.FlowTurnID == "" {
					info.FlowTurnID = result.Turn.FlowTurnID
				}
				if info.Attempt == 0 {
					info.Attempt = result.Turn.Attempt
				}
				info.CallerTeam = result.Turn.CallerTeam
				info.CallResults = result.CallResults
				info.PendingToolTasks = append(info.PendingToolTasks, result.PendingToolTasks...)
				info.PendingApprovals = append(info.PendingApprovals, result.PendingApprovals...)
				for callID, callResult := range result.CallResults {
					if callResult.Status != types.TurnWaitingTool {
						continue
					}
					if hasPendingCall(info.PendingToolTasks, callID) {
						continue
					}
					info.PendingToolTasks = append(info.PendingToolTasks, types.PendingToolTask{
						CallID:       callID,
						CallTurnID:   callResult.CallTurnID,
						AgentID:      callResult.AgentID,
						TaskID:       callResult.TaskID,
						CheckpointID: callResult.CheckpointID,
						Status:       callResult.Status,
					})
				}
			}
		}
		hasApproval := false
		for _, callResult := range info.CallResults {
			if callResult.Status == types.TurnWaitingApproval &&
				callResult.PendingApproval != nil {
				hasApproval = true
				break
			}
		}
		if len(info.PendingToolTasks) == 0 && !hasApproval {
			continue
		}
		return info, true
	}
	return pendingTeamResumeInfo{}, false
}

func hasPendingCall(items []types.PendingToolTask, callID string) bool {
	for _, item := range items {
		if item.CallID == callID {
			return true
		}
	}
	return false
}

func pendingAgentResume(events []types.SessionEvent) (pendingAgentResumeInfo, bool) {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != types.EventAgentTurnWaitingInput &&
			event.Type != types.EventAgentTurnWaitingTool {
			continue
		}
		checkpointID := payloadString(event.Payload, "checkpoint_id")
		if raw, ok := event.Payload["call_result"]; ok {
			var result types.CallResult
			if err := decodeValue(raw, &result); err == nil {
				if checkpointID == "" {
					checkpointID = result.CheckpointID
				}
			}
		}
		if checkpointID == "" {
			continue
		}
		return pendingAgentResumeInfo{
			FlowTurnID:   event.FlowTurnID,
			TeamID:       event.TeamID,
			TeamTurnID:   event.TeamTurnID,
			CallID:       event.CallID,
			CallTurnID:   event.CallTurnID,
			CallerTeam:   payloadString(event.Payload, "caller_team"),
			Attempt:      event.Attempt,
			CheckpointID: checkpointID,
			TaskID:       payloadString(event.Payload, "task_id"),
			Status: func() types.TurnStatus {
				if event.Type == types.EventAgentTurnWaitingTool {
					return types.TurnWaitingTool
				}
				return types.TurnWaitingInput
			}(),
		}, true
	}
	return pendingAgentResumeInfo{}, false
}

func sortInterrupted(items []types.InterruptedExecution) {
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].CallTurnID+items[i].TeamTurnID+items[i].FlowTurnID <
			items[j].CallTurnID+items[j].TeamTurnID+items[j].FlowTurnID
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
	if target.CallTurnID != "" {
		return target.CallTurnID
	}
	if target.TeamTurnID != "" {
		return target.TeamTurnID
	}
	return target.FlowTurnID
}

func selectInterrupted(items []types.InterruptedExecution, target string) (types.InterruptedExecution, error) {
	if target != "" {
		for _, item := range items {
			if item.CallTurnID == target || item.TeamTurnID == target || item.FlowTurnID == target {
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
		CallID:        target.CallID,
		CallTurnID:    target.CallTurnID,
		CallType:      target.CallType,
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
	case types.SessionWaitingTool:
		return types.TurnWaitingTool
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
