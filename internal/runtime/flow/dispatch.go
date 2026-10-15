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
// panic guard wraps every step uniformly. The work itself lives in
// runMessageTurn: a dispatched Team is just a Team taking a message, and its
// routing follows the same rules whether it runs in the background or inside a
// turn.
func (r *Runtime) dispatchWork(ctx context.Context, session types.FlowSession, causeTurnID, targetTeam string, msg types.MailboxMessage) {
	if msg.CauseFlowTurnID == "" {
		msg.CauseFlowTurnID = causeTurnID
	}
	if msg.To.TeamID == "" {
		msg.To.TeamID = targetTeam
	}
	r.runMessageTurn(ctx, session, msg)
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
