package flow

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// launchAsyncDispatch executes a dispatched team in a background goroutine and
// reports its result back through the mailbox. It is the async half of the P0
// mailbox dispatch: the dispatching FlowTurn has already ended by the time this
// runs, so the goroutine owns its own lifetime.
//
// It deliberately uses context.Background(): the turn context is cancelled the
// moment the dispatching turn returns, and a background team must not be killed
// by that. A panic is contained with recover() so a misbehaving team cannot
// take down the process; the message is then marked dead.
func (r *Runtime) launchAsyncDispatch(session types.FlowSession, causeTurnID, targetTeam string, msg types.MailboxMessage) {
	go func() {
		ctx := context.Background()
		defer func() {
			if rec := recover(); rec != nil {
				logging.Error("async dispatch panicked", map[string]any{
					"flow_session_id": session.ID,
					"message_id":      msg.MessageID,
					"team_id":         targetTeam,
					"panic":           fmt.Sprint(rec),
				})
				if _, err := r.markMessageDead(ctx, session, msg, fmt.Sprintf("panic: %v", rec)); err != nil {
					logging.Error("mark dispatched message dead", map[string]any{
						"flow_session_id": session.ID,
						"message_id":      msg.MessageID,
						"error":           err.Error(),
					})
				}
			}
		}()

		r.dispatchWork(ctx, session, causeTurnID, targetTeam, msg)
	}()
}

// dispatchWork is the recoverable body of launchAsyncDispatch, split out so the
// panic guard wraps every step uniformly.
func (r *Runtime) dispatchWork(ctx context.Context, session types.FlowSession, causeTurnID, targetTeam string, msg types.MailboxMessage) {
	// Claim the message first: a claimed message is one a turn has picked up,
	// which is exactly what is happening here.
	if _, err := r.claimMessage(ctx, session, msg); err != nil {
		logging.Error("claim dispatched message", map[string]any{
			"flow_session_id": session.ID,
			"message_id":      msg.MessageID,
			"error":           err.Error(),
		})
	}

	// Snapshot the definitions once for the whole dispatch, matching the
	// per-turn snapshot discipline of the synchronous path.
	defs := r.definitions.Snapshot()
	callerTeam, _ := msg.Payload["caller_team"].(string)
	act := activation{teamID: targetTeam, callerTeam: callerTeam}

	// The original turn is over, so its FlowTurn is gone. executeTeamTurn
	// threads the FlowTurn down for provenance (FlowTurnID on team/record
	// events) and for the team record's producer ref; it does not read
	// Status. A minimal placeholder carrying the dispatching turn's ID is
	// therefore enough. Attempt defaults to 0 and is overwritten by the
	// activation's own attempt when the TeamTurn is built.
	flowTurn := types.FlowTurn{ID: causeTurnID, FlowSessionID: session.ID}

	// The synchronous path hands each activated team the session's historical
	// Flow-scope records (filtered by its binding's inputs inside
	// executeTeamTurn). A dispatched team must see the same records: with nil
	// here, every `flow_records` / `${records.X.Data.y}` reference in a
	// dispatched team resolved against nothing and failed with "record X not
	// found among call inputs". A load failure is reported on the work message
	// like any other team failure rather than silently running with no inputs.
	records, loadErr := r.loadHistoricalFlowRecords(ctx, session.ID)
	var execution teamExecution
	var err error
	if loadErr != nil {
		err = fmt.Errorf("load flow records for dispatched team %q: %w", targetTeam, loadErr)
	} else {
		execution, err = r.executeTeamTurn(ctx, defs, session, flowTurn, act, "", records, nil)
	}
	if err != nil {
		execution.result.Error = err.Error()
	}
	teamResult := execution.result
	teamFailed := teamResult.Error != "" || teamResult.Turn.Status == types.TurnFailed

	// Results go back to whoever dispatched this Team, not to the entry Team:
	// a dispatched Team is a step inside its activator's work, so its result
	// belongs in the activator's inbox. The activator rode in on the work
	// message as caller_team; falling back to the entry Team keeps a message
	// written by an older runtime readable.
	resultTo := callerTeam
	if resultTo == "" {
		resultTo = defs.Flow.EntryTeamID
	}
	if resultTo == "" {
		resultTo = targetTeam
	}

	// Record the work message's outcome on its own lifecycle first, then post
	// a separate result message to the activator's inbox.
	resultPayload := map[string]any{"team_result": teamResult}
	if teamFailed {
		if _, mErr := r.markMessageDead(ctx, session, msg, teamResult.Error); mErr != nil {
			logging.Error("mark dispatched message dead", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           mErr.Error(),
			})
		}
		resultPayload["error"] = teamResult.Error
	} else {
		if _, dErr := r.deliverMessage(ctx, session, msg, resultPayload); dErr != nil {
			logging.Error("deliver dispatched message", map[string]any{
				"flow_session_id": session.ID,
				"message_id":      msg.MessageID,
				"error":           dErr.Error(),
			})
		}
	}

	// The result address carries the activator (or the entry Team as a
	// fallback) in the same snake_case MailboxAddress shape the enqueue
	// helpers produce. The container value (TeamTurnResult) keeps its
	// published Go field names.
	causeTurn := types.FlowTurn{ID: causeTurnID}
	resultMsg, rErr := r.enqueueResultMessage(ctx, session, causeTurn,
		types.MailboxAddress{TeamID: resultTo},
		resultPayload,
	)
	if rErr != nil {
		logging.Error("enqueue dispatched result", map[string]any{
			"flow_session_id": session.ID,
			"message_id":      msg.MessageID,
			"error":           rErr.Error(),
		})
	}

	// Wake the entry team only if the session is idle. A running session keeps
	// the result in the inbox; the next turn it starts will drain it.
	r.wakeIfIdle(ctx, session.ID)

	if resultMsg.MessageID != "" {
		logging.Info("async dispatch finished", map[string]any{
			"flow_session_id": session.ID,
			"team_id":         targetTeam,
			"status":          string(msg.Status),
			"result_message":  resultMsg.MessageID,
			"failed":          teamFailed,
		})
	}
}

// wakeIfIdle calls the injected waker only when the session is idle enough to
// resume. A running session (including the dispatching turn itself, whose
// flow_session.updated event lands before the background team finishes) is left
// alone: its result message sits in the inbox and the next turn drains it.
func (r *Runtime) wakeIfIdle(ctx context.Context, sessionID string) {
	if r.wakeFn == nil {
		return
	}
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		// Cannot prove the session is idle; stay conservative and do not wake.
		logging.Error("load session before wake", map[string]any{
			"flow_session_id": sessionID,
			"error":           err.Error(),
		})
		return
	}
	if session.Status == types.SessionRunning {
		return
	}
	switch session.Status {
	case types.SessionWaitingInput,
		types.SessionWaitingTool,
		types.SessionWaitingApproval,
		types.SessionCreated:
		r.wakeFn(sessionID)
	default:
		// Failed / cancelled / interrupted / completed are terminal or
		// externally controlled; do not wake.
	}
}

// recordBatchDispatched writes the end-of-turn EventMailboxBatchDispatched
// event for a turn that handed work to the background dispatcher. It is a
// purely additive event: finishTurn's existing turn/session event is untouched.
// A failure to record it is logged, not escalated, so a bookkeeping write can
// never turn a successfully dispatched turn into a failed one.
func (r *Runtime) recordBatchDispatched(ctx context.Context, session types.FlowSession, turn types.FlowTurn, messageIDs []string) {
	if len(messageIDs) == 0 {
		return
	}
	payload := mailboxBatchDispatchedPayload(messageIDs, time.Now().UTC())
	if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventMailboxBatchDispatched,
			FlowSessionID: session.ID,
			FlowTurnID:    turn.ID,
		},
		Payload: payload,
	}); err != nil {
		logging.Error("record mailbox batch dispatched", map[string]any{
			"flow_session_id": session.ID,
			"flow_turn_id":    turn.ID,
			"error":           err.Error(),
		})
	}
}

// mailboxBatchDispatchedPayload builds the payload for the end-of-turn
// EventMailboxBatchDispatched event. It carries the message IDs handed to the
// background dispatcher this turn; it does not extend finishTurn's existing
// turn/session event shape.
func mailboxBatchDispatchedPayload(messageIDs []string, dispatchedAt time.Time) map[string]any {
	ids := append([]string(nil), messageIDs...)
	return map[string]any{
		"message_ids":   ids,
		"dispatched_at": dispatchedAt,
	}
}
