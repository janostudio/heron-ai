package flow

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// This file is the session-level scheduler for the execution-state model: a
// Team is a peer execution unit that takes a message when it is not executing,
// and its result goes back to whoever sent the work. The entry Team is only the
// Team a user's turn starts at; every other Team is reached the same way.
//
// The model has exactly one scheduling rule: a Team that is not executing can
// take a message. teamIdle (session_state.go) encodes it against the TeamSessions
// folded from the event stream, and runMessageTurn is the single path that acts
// on it: claim one message, run the target Team one round, and route its result
// by posting mail rather than by continuing the dispatcher's own turn.

// runMessageTurn executes one dispatched Team round and routes its outcome. The
// work message is the credential for the hand-off: it is claimed before the
// Team runs and delivered (or marked dead) with the result, so a crash leaves
// the hand-off visible in the event stream.
//
// Routing follows the same rules as a Team inside a turn (routeTeamResult), but
// every route becomes a message instead of a queue entry:
//   - activate: post work to each target and stop. The dispatcher does not run
//     the targets; they run when their own message is taken.
//   - proceed with no on_proceed: post the result back to the activator.
//   - proceed with on_proceed / return: post to the named Team.
//   - fail: post a failure result to the activator.
//
// The Team is marked executing only for the duration of this round, so two
// messages for the same Team never run at once (the scheduler checks teamIdle
// before dispatching).
func (r *Runtime) runMessageTurn(ctx context.Context, session types.FlowSession, msg types.MailboxMessage) {
	defs := r.definitions.Snapshot()

	// The Team-turn budget is a session total, not a per-turn one: with Teams
	// dispatching Teams, each round starts a new turn, so only a count across
	// the whole session bounds a runaway loop. Exceeding it ends the session
	// rather than spawning another round.
	if started, err := r.countTeamTurns(ctx, session.ID); err != nil {
		logging.Error("count team turns", map[string]any{
			"flow_session_id": session.ID,
			"error":           err.Error(),
		})
	} else if r.limits.MaxTeamTurns > 0 && started >= r.limits.MaxTeamTurns {
		limitErr := fmt.Errorf("flow session exceeded max team turns: %d", r.limits.MaxTeamTurns)
		logging.Error("flow session exceeded max team turns", map[string]any{
			"flow_session_id": session.ID,
			"team_id":         msg.To.TeamID,
			"error":           limitErr.Error(),
		})
		if _, err := r.markMessageDead(ctx, session, msg, limitErr.Error()); err != nil {
			logging.Error("mark over-budget message dead", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           err.Error(),
			})
		}
		return
	}

	// A message launched by the dispatcher was already claimed there, which is
	// what kept a concurrent wake-up from taking it. Only a message that
	// arrives unclaimed is claimed here.
	if msg.Status != types.MailboxClaimed {
		if _, err := r.claimMessage(ctx, session, msg); err != nil {
			logging.Error("claim dispatched message", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           err.Error(),
			})
		}
	}

	target := msg.To.TeamID
	caller, _ := msg.Payload["caller_team"].(string)
	// The task travels with the message. A Team woken by a mailbox has no user
	// input of its own, and the only other source of "what am I doing" is its
	// own history — which is how a Team ends up answering the previous
	// question: it sees its last completed task and repeats that verdict.
	input, _ := msg.Payload["input"].(string)

	flowTurn := types.FlowTurn{ID: msg.CauseFlowTurnID, FlowSessionID: session.ID, Input: input}
	records, loadErr := r.loadHistoricalFlowRecords(ctx, session.ID)
	// A Team woken by a result message has to see that result. The upstream
	// Team publishes its records with team scope, while loadHistoricalFlowRecords
	// only reads flow scope and a Team's declared flow_records are matched by
	// name against that slice — so without promoting them here the entry Team
	// reads nothing and repeats its dispatch line instead of relaying the answer.
	if msg.Kind == mailboxKindResult {
		if upstream, ok := msg.Payload["team_result"].(types.TeamTurnResult); ok {
			records = append(records, promoteToFlowScope(upstream.Records)...)
		}
	}

	act := activation{teamID: target, callerTeam: caller}
	// The activator is whoever dispatched this Team: its work message carries
	// caller_team, which is where the result goes and where the Team returns on
	// proceed/coordinate/fail.
	if caller != "" {
		act.activator = caller
	}

	var execution teamExecution
	var err error
	if loadErr != nil {
		err = fmt.Errorf("load flow records for dispatched team %q: %w", target, loadErr)
	} else {
		execution, err = r.executeTeamTurn(ctx, defs, session, flowTurn, act, input, records, time.Time{}, nil)
	}
	if err != nil {
		execution.result.Error = err.Error()
	}
	teamFailed := execution.result.Error != "" || execution.result.Turn.Status == types.TurnFailed

	// Report the work message's own lifecycle first, then post the result.
	resultPayload := map[string]any{"team_result": execution.result}
	if teamFailed {
		if _, mErr := r.markMessageDead(ctx, session, msg, execution.result.Error); mErr != nil {
			logging.Error("mark dispatched message dead", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           mErr.Error(),
			})
		}
		resultPayload["error"] = execution.result.Error
	} else {
		if _, dErr := r.deliverMessage(ctx, session, msg, resultPayload); dErr != nil {
			logging.Error("deliver dispatched message", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           dErr.Error(),
			})
		}
	}

	// Route the outcome as messages. A routing mistake must not be silent: it
	// would strand the work with no Team to pick it up.
	if rErr := r.postRoutes(ctx, defs, session, flowTurn, execution, teamFailed); rErr != nil {
		logging.Error("post dispatched routes", map[string]any{
			"flow_session_id": session.ID,
			"message_id":      msg.MessageID,
			"team_id":         target,
			"error":           rErr.Error(),
		})
	}

	// A round that stopped for a Tool task or an approval has nobody to route
	// to (see postRoutes): it parked, and the session has to say so, because
	// that status is what the completion callback looks for before resuming.
	if parkErr := r.parkDispatchedSession(ctx, session, flowTurn, execution); parkErr != nil {
		logging.Error("park dispatched session", map[string]any{
			"flow_session_id": session.ID,
			"flow_turn_id":    flowTurn.ID,
			"team_id":         target,
			"error":           parkErr.Error(),
		})
	}

	r.wakeIfIdle(ctx, session.ID)

	logging.Info("async dispatch finished", map[string]any{
		"flow_session_id": session.ID,
		"team_id":         target,
		"status":          string(msg.Status),
		"failed":          teamFailed,
	})
}

// postRoutes turns a dispatched Team's route into mailbox messages, mirroring
// the in-turn routing in runtime.go but always posting mail instead of
// continuing a queue. A result route with nowhere to go (an entry Team
// finishing on its own) is dropped: only the user can act on it, and the
// session is left waiting.
func (r *Runtime) postRoutes(
	ctx context.Context,
	defs *types.Definitions,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	execution teamExecution,
	teamFailed bool,
) error {
	next := execution.next
	if next == nil {
		next = &types.Route{Action: types.NextProceed}
	}

	if teamFailed {
		// A failed Team reports to its activator, exactly like the in-turn
		// failure escalation; an entry Team's failure has no one to tell.
		return r.postResult(ctx, session, flowTurn, execution.activation.activator, execution)
	}

	switch next.Action {
	case types.NextWaitTool, types.NextWaitApproval:
		// Nothing to route: the round stopped to wait for a user, a Tool task
		// or an approval. The parked status is written by parkDispatchedSession
		// so the matching resume path can find the session again.
		return nil
	case types.NextActivate:
		if len(execution.binding.CanActivate) == 0 {
			return fmt.Errorf("team %q cannot activate other teams", execution.activation.teamID)
		}
		if err := validateActivation(execution.binding, defs.Flow, next.Teams); err != nil {
			return err
		}
		// Activation is counted across the whole session, not per turn: a
		// dispatched Team starts a new turn each time, so a Team that keeps
		// re-activating the same target would otherwise loop until the much
		// larger session budget stops it. The bound has to see the repetition.
		source := execution.activation.teamID
		for _, target := range next.Teams {
			if err := r.checkActivationBudget(ctx, session.ID, source, target); err != nil {
				return err
			}
			if _, err := r.enqueueWorkMessage(ctx, session, flowTurn,
				types.MailboxAddress{TeamID: target},
				map[string]any{"caller_team": source, "input": flowTurn.Input},
			); err != nil {
				return fmt.Errorf("enqueue work for team %q: %w", target, err)
			}
		}
		return nil
	case types.NextProceed:
		targets := fixedTargets(execution.binding)
		if len(targets) == 0 {
			return r.postResult(ctx, session, flowTurn, execution.activation.activator, execution)
		}
		for _, target := range targets {
			if err := r.postResult(ctx, session, flowTurn, target, execution); err != nil {
				return err
			}
		}
		return nil
	case types.NextReturn:
		target := next.CallerTeam
		if target == "" {
			target = execution.activation.activator
		}
		return r.postResult(ctx, session, flowTurn, target, execution)
	case types.NextCoordinate:
		return r.postResult(ctx, session, flowTurn, execution.activation.activator, execution)
	case types.NextFail:
		return r.postResult(ctx, session, flowTurn, execution.activation.activator, execution)
	default:
		return fmt.Errorf("unsupported next action %q from dispatched team %q", next.Action, execution.activation.teamID)
	}
}

// parkDispatchedSession records the waiting status a dispatched round stopped
// in.
//
// A background round has no FlowTurn to finish, so nothing wrote the session
// state after the Team parked. The session therefore kept whatever status it
// had when it was dispatched — usually waiting_input — while the Agent
// checkpoint and the Team waiting-tool event said otherwise. That mismatch is
// not cosmetic: appendWakingTeam (app/runtime.go) only consumes a completed
// Tool task's wake-up when the session is in waiting_tool, so the wake-up was
// dropped after a minute and the result was never collected. The session simply
// stopped answering.
//
// Only the three waiting states are written: completed and failed rounds still
// have routing to do (postRoutes posts their result), and their status is
// decided by whoever consumes that result.
func (r *Runtime) parkDispatchedSession(
	ctx context.Context,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	execution teamExecution,
) error {
	status := sessionStatusForTurn(execution.result.Turn.Status)
	switch status {
	case types.SessionWaitingInput, types.SessionWaitingTool, types.SessionWaitingApproval:
	default:
		return nil
	}
	if session.Status == status {
		return nil
	}
	session.Status = status
	session.UpdatedAt = time.Now().UTC()
	return r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: session.ID,
			FlowTurnID:    flowTurn.ID,
		},
		Payload: map[string]any{"session": session},
	})
}

// postResult enqueues a result message to target carrying the Team's outcome.
// An empty target means no one is waiting (an entry Team's own completion);
// that is not an error, it just has nowhere to go.
func (r *Runtime) postResult(ctx context.Context,
	session types.FlowSession,
	flowTurn types.FlowTurn,
	target string,
	execution teamExecution,
) error {
	if target == "" {
		return nil
	}
	payload := map[string]any{"team_result": execution.result}
	// The entry Team is woken to answer the user, so it needs the question —
	// not just the result it is relaying. It travels as the statement this
	// continuation belongs to, not as the woken turn's input: as input it
	// renders as the trailing user turn, and a report coming back then reads
	// as a fresh request (see continuedTaskStatement).
	if flowTurn.Input != "" {
		payload["input"] = flowTurn.Input
	}
	// This message is what the caller renders, so the failure text has to
	// travel on it: mailboxResultSummary reads payload["error"], and without
	// it a failed round degrades to "(no rendered reply; raw result
	// unavailable)". A failed round always carries error text —
	// executeTeamTurn fills in a fallback reason when the Team itself
	// returned none — so this is the same condition as teamFailed above.
	if execution.result.Error != "" {
		payload["error"] = execution.result.Error
	}
	_, err := r.enqueueResultMessage(ctx, session, flowTurn,
		types.MailboxAddress{TeamID: target},
		payload,
	)
	return err
}

// promoteToFlowScope exposes an upstream Team's records to a dispatch target.
// A Team that declares `flow_records: [OwnerReply]` is matched by name against
// the flow-scope slice only, so a record published in team scope is invisible
// to it — including the reply it is supposed to relay.
func promoteToFlowScope(records []types.SharedRecord) []types.SharedRecord {
	promoted := make([]types.SharedRecord, 0, len(records))
	for _, record := range records {
		if record.Name == "" {
			continue
		}
		record.Scope = types.RecordScopeFlow
		promoted = append(promoted, record)
	}
	return promoted
}
