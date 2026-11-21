package flow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// interruptedExecutions reports the executions the event stream shows as
// started but never finished. liveTeams names the Teams executing in this
// process right now: their team_turns (and the calls under them) look identical
// to a crash's leftovers — started with no completion — but they are still
// running, so they must not be reported as interrupted. Without this, a
// status query during a dispatched Team's round would mark its session
// interrupted and refuse further input.
// liveIndex answers "is this execution still live?" for both current events
// (keyed by TeamSessionID) and events written before instances existed (no
// session id, keyed by TeamID).
type liveIndex struct {
	sessions map[string]bool
	teams    map[string]bool
}

func (l liveIndex) running(event storage.SessionEvent) bool {
	if event.TeamSessionID != "" {
		return l.sessions[event.TeamSessionID]
	}
	return l.teams[event.TeamID]
}

func (r *Runtime) liveIndexFor(sessionID string) liveIndex {
	return liveIndex{
		sessions: r.liveTeamSessionsFor(sessionID),
		teams:    r.liveTeamsFor(sessionID),
	}
}

func interruptedExecutions(events []storage.SessionEvent, live liveIndex) []types.InterruptedExecution {
	type started struct {
		event storage.SessionEvent
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
		case types.EventFlowTurnEnded:
			// One terminal event: what happens next depends on whether it is
			// still waiting for someone, not on the event name.
			if payloadString(event.Payload, "awaiting") != "" {
				// A FlowTurn that ended waiting is not interrupted; it stays
				// resumable by design.
				waitingFlows[event.FlowTurnID] = true
			} else {
				completedFlows[event.FlowTurnID] = true
			}
		case types.EventTeamTurnStarted:
			startedTeams[event.TeamTurnID] = started{event: event, input: payloadString(event.Payload, "input")}
		case types.EventTeamTurnEnded:
			if payloadString(event.Payload, "awaiting") != "" {
				waitingTeams[event.TeamTurnID] = true
				waitingFlows[event.FlowTurnID] = true
			} else {
				completedTeams[event.TeamTurnID] = true
			}
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
			resolvedFlows[item.event.FlowTurnID] ||
			live.running(item.event) {
			continue
		}
		execution := interruptedFromEvent("call_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedTeamIDs[item.event.TeamTurnID] = true
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedTeams {
		if completedTeams[turnID] || waitingTeams[turnID] || resolvedTeams[turnID] ||
			interruptedTeamIDs[turnID] || live.running(item.event) {
			continue
		}
		execution := interruptedFromEvent("team_turn", item.event, item.input)
		interrupted = append(interrupted, execution)
		interruptedFlowIDs[item.event.FlowTurnID] = true
	}
	for turnID, item := range startedFlows {
		if completedFlows[turnID] || waitingFlows[turnID] || resolvedFlows[turnID] ||
			interruptedFlowIDs[turnID] {
			continue
		}
		interrupted = append(interrupted, interruptedFromEvent("flow_turn", item.event, item.input))
	}
	sortInterrupted(interrupted)
	return interrupted
}

func interruptedFromEvent(kind string, event storage.SessionEvent, input string) types.InterruptedExecution {
	safe := kind == "flow_turn" || kind == "team_turn"
	reason := "call execution may have side effects; inspect before retry"
	if safe {
		reason = "retrying the containing Team/Flow may repeat downstream work"
	}
	return types.InterruptedExecution{
		Kind:          kind,
		FlowTurnID:    event.FlowTurnID,
		TeamID:        event.TeamID,
		TeamSessionID: event.TeamSessionID,
		TeamTurnID:    event.TeamTurnID,
		CallID:        event.CallID,
		CallTurnID:    event.CallTurnID,
		CallType:      event.CallType,
		Attempt:       event.Attempt,
		RecoveryOf:    event.RecoveryOf,
		StartedSeq:    event.Seq,
		StartedAt:     event.CreatedAt,
		Input:         input,
		CallerTeam:    payloadString(event.Payload, "caller_team"),
		StartedEvent:  event.Type,
		SafeToRetry:   safe,
		RetryReason:   reason,
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

// pendingTeamResumes returns every Team waiting on a Tool or an approval, most
// recent first. It used to return a single Team, which was safe while only one
// Team could be paused at a time; with several Teams running at once more than
// one can be paused, and a caller that took only the first would silently drop
// the others. Callers therefore decide what to do with the whole set.
func pendingTeamResumes(events []storage.SessionEvent) []pendingTeamResumeInfo {
	var resumes []pendingTeamResumeInfo
	seen := make(map[string]bool)
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type != types.EventTeamTurnEnded {
			continue
		}
		if awaiting := payloadString(event.Payload, "awaiting"); awaiting != string(types.AwaitingTool) &&
			awaiting != string(types.AwaitingApproval) {
			continue
		}
		if event.TeamTurnID != "" && seen[event.TeamTurnID] {
			continue
		}
		info, ok := pendingTeamResumeFromEvent(event)
		if !ok {
			continue
		}
		if event.TeamTurnID != "" {
			seen[event.TeamTurnID] = true
		}
		resumes = append(resumes, info)
	}
	return resumes
}

// orphanedWaitingExecutions reports Teams paused on a Tool task that is no
// longer in the store.
//
// This is the one waiting state that cannot be resumed. pendingTasksReady loads
// the task to decide whether the Team may continue; a missing record makes it
// fail rather than wait. Since interruptedExecutions treats every waiting turn
// as resumable — correct until the thing being waited for is gone — such a
// session ends up with no path: Resume fails with "task not found", HandleInput
// refuses a session waiting on a Tool, and Recover reports nothing. Reporting
// it as interrupted is what turns a dead session into a recoverable one.
//
// Only a definite "not found" counts. Any other read error means the store
// could not answer, which is not evidence the task is gone: turning a transient
// IO failure into "your session is unrecoverable" is the worse mistake.
func (r *Runtime) orphanedWaitingExecutions(ctx context.Context, events []storage.SessionEvent) []types.InterruptedExecution {
	if r.tasks == nil {
		return nil
	}
	// pendingTeamResumeInfo carries no TeamSessionID, and recovery targets the
	// instance rather than the Team, so take it from the last event naming the
	// turn.
	lastEvent := make(map[string]storage.SessionEvent)
	for _, event := range events {
		if event.TeamTurnID != "" {
			lastEvent[event.TeamTurnID] = event
		}
	}

	var orphaned []types.InterruptedExecution
	for _, team := range pendingTeamResumes(events) {
		if len(team.PendingToolTasks) == 0 {
			continue
		}
		missing := ""
		for _, item := range team.PendingToolTasks {
			// An empty task id means the event did not record what the Team is
			// waiting for — a malformed or older event, not evidence the task is
			// gone. Only a definitive "not found" counts as orphaned; anything
			// else leaves the sitting state as it is.
			if item.TaskID == "" {
				continue
			}
			if _, err := r.tasks.Load(ctx, item.TaskID); errors.Is(err, types.ErrToolTaskNotFound) {
				missing = item.TaskID
				break
			}
		}
		if missing == "" {
			continue
		}
		event := lastEvent[team.TeamTurnID]
		orphaned = append(orphaned, types.InterruptedExecution{
			Kind:          "team_turn",
			FlowTurnID:    team.FlowTurnID,
			TeamID:        team.TeamID,
			TeamSessionID: event.TeamSessionID,
			TeamTurnID:    team.TeamTurnID,
			Attempt:       team.Attempt,
			CallerTeam:    team.CallerTeam,
			StartedEvent:  string(event.Type),
			SafeToRetry:   true,
			RetryReason: "the pending Tool task " + missing +
				" no longer exists, so this turn can never continue; retrying re-runs the Team turn from its start",
		})
	}
	return orphaned
}

// pendingTeamForApproval narrows a set of paused Teams to the one owning the
// given approval request. An approval is addressed by RequestID, so this is
// what keeps two Teams paused on approvals from being confused for each other.
func pendingTeamForApproval(teams []pendingTeamResumeInfo, requestID string) (pendingTeamResumeInfo, bool) {
	for _, team := range teams {
		for _, approval := range team.PendingApprovals {
			if approval.RequestID == requestID {
				return team, true
			}
		}
		for _, callResult := range team.CallResults {
			if callResult.Status != types.TurnWaitingApproval || callResult.PendingApproval == nil {
				continue
			}
			if callResult.PendingApproval.RequestID == requestID {
				return team, true
			}
		}
	}
	return pendingTeamResumeInfo{}, false
}

func pendingTeamResumeFromEvent(event storage.SessionEvent) (pendingTeamResumeInfo, bool) {
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
		return pendingTeamResumeInfo{}, false
	}
	return info, true
}

func hasPendingCall(items []types.PendingToolTask, callID string) bool {
	for _, item := range items {
		if item.CallID == callID {
			return true
		}
	}
	return false
}

func pendingAgentResume(events []storage.SessionEvent) (pendingAgentResumeInfo, bool) {
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

func recoveryHistory(events []storage.SessionEvent) []types.RecoveryEvent {
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
	return r.appendSessionEvent(ctx, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
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
		},
		Payload: map[string]any{"request": request, "target": target},
	})
}

// validateDependencies checks the store's currently published tree. This is a
// startup/validation path rather than a turn path, so it takes its own
// snapshot instead of threading a turn's defs in: a caller asking "is the flow
