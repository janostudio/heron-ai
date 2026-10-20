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
// reports its result back through the mailbox. It is the async half of the
// mailbox dispatch: the dispatching FlowTurn has already ended by the time this
// runs, so the goroutine owns its own lifetime.
//
// A Team is one execution unit, so a message is only launched when the Team is
// free. When it is busy the message stays pending; the running round's
// completion wakes the session, which takes the next pending message then. This
// is what lets several Teams run at once while a single Team never runs two
// rounds concurrently.
//
// It deliberately uses context.Background(): the turn context is cancelled the
// moment the dispatching turn returns, and a background team must not be killed
// by that. A panic is contained with recover() so a misbehaving team cannot
// take down the process; the message is then marked dead.
func (r *Runtime) launchAsyncDispatch(session types.FlowSession, causeTurnID, targetTeam string, msg types.MailboxMessage) {
	if !r.tryMarkTeamBusy(targetTeam) {
		// The silent path: the Team is mid-round, so the message stays
		// pending and drainTeamInbox picks it up when that round ends.
		// Without this line a backlog looks like a lost message.
		logging.Debug("async dispatch deferred: team already running", map[string]any{
			"flow_session_id": session.ID,
			"message_id":      msg.MessageID,
			"team_id":         targetTeam,
			"status":          string(msg.Status),
		})
		return
	}
	logging.Debug("async dispatch started", map[string]any{
		"flow_session_id":    session.ID,
		"message_id":         msg.MessageID,
		"team_id":            targetTeam,
		"kind":               msg.Kind,
		"cause_flow_turn_id": causeTurnID,
	})
	// Claim the message before starting the goroutine: this is what takes it
	// out of the pending set, so a concurrent wake-up cannot pick the same
	// message and run the Team a second time. A failure here still launches the
	// round — running the work is more important than the bookkeeping — and is
	// logged.
	if claimed, err := r.claimMessage(context.Background(), session, msg); err != nil {
		logging.Error("claim dispatched message", map[string]any{
			"flow_session_id": session.ID,
			"message_id":      msg.MessageID,
			"team_id":         targetTeam,
			"error":           err.Error(),
		})
	} else {
		msg = claimed
	}
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
			// Free the slot before draining, so the drain can take the next
			// message for this Team.
			r.clearTeamBusy(targetTeam)
			r.drainTeamInbox(ctx, session, targetTeam)
		}()

		r.dispatchWork(ctx, session, causeTurnID, targetTeam, msg)
	}()
}

// drainTeamInbox launches the next pending message addressed to teamID, if any.
// A message is left pending when it arrives while the Team is already running;
// this is the path that eventually runs it, so a Team's backlog is drained
// without waiting for a user turn. One message is taken per call: it re-enters
// the same launch path and drains the one after it when it finishes.
func (r *Runtime) drainTeamInbox(ctx context.Context, session types.FlowSession, teamID string) {
	messages, err := r.listInbox(ctx, session.ID, types.MailboxAddress{TeamID: teamID})
	if err != nil {
		logging.Error("drain team inbox", map[string]any{
			"flow_session_id": session.ID,
			"team_id":         teamID,
			"error":           err.Error(),
		})
		return
	}
	if len(messages) == 0 {
		logging.Debug("drain team inbox: no pending message", map[string]any{
			"flow_session_id": session.ID,
			"team_id":         teamID,
		})
		return
	}
	logging.Debug("drain team inbox: launching next message", map[string]any{
		"flow_session_id": session.ID,
		"team_id":         teamID,
		"message_id":      messages[0].MessageID,
		"pending_count":   len(messages),
	})
	r.launchAsyncDispatch(session, messages[0].CauseFlowTurnID, teamID, messages[0])
}

// tryMarkTeamBusy claims the Team's execution slot, returning false when the
// Team is already running. It is the concurrency guard that keeps a Team from
// running two rounds at once.
func (r *Runtime) tryMarkTeamBusy(teamID string) bool {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	if r.busyTeams == nil {
		r.busyTeams = make(map[string]bool)
	}
	if r.busyTeams[teamID] {
		return false
	}
	r.busyTeams[teamID] = true
	return true
}

func (r *Runtime) clearTeamBusy(teamID string) {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	delete(r.busyTeams, teamID)
}

// teamBusy reports whether a Team is executing in this process right now. The
// scheduler uses it to keep from starting a second round for a Team whose
// first round the background dispatcher already owns.
func (r *Runtime) teamBusy(teamID string) bool {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	return r.busyTeams[teamID]
}

// liveTeams returns a snapshot of the Teams executing in this process. Recovery
// uses it to tell a Team that is still running from one a crash left behind:
// both look like "started with no completion" in the event stream, and only the
// former must not be reported as interrupted.
func (r *Runtime) liveTeams() map[string]bool {
	r.dispatchMu.Lock()
	defer r.dispatchMu.Unlock()
	live := make(map[string]bool, len(r.busyTeams))
	for team := range r.busyTeams {
		live[team] = true
	}
	return live
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
	logging.Debug("async dispatch round started", map[string]any{
		"flow_session_id":    session.ID,
		"message_id":         msg.MessageID,
		"team_id":            msg.To.TeamID,
		"cause_flow_turn_id": msg.CauseFlowTurnID,
	})
	started := time.Now()
	r.runMessageTurn(ctx, session, msg)
	// Success/failure is already logged by runMessageTurn; this closes the
	// bracket with the wall-clock cost of the round.
	logging.Debug("async dispatch round finished", map[string]any{
		"flow_session_id": session.ID,
		"message_id":      msg.MessageID,
		"team_id":         msg.To.TeamID,
		"duration_ms":     time.Since(started).Milliseconds(),
	})
}
// wakeIfIdle calls the injected waker only when the session is idle enough to
// resume. A running session (including the dispatching turn itself, whose
// flow_session.updated event lands before the background team finishes) is left
// alone: its result message sits in the inbox and the next turn drains it.
func (r *Runtime) wakeIfIdle(ctx context.Context, sessionID string) {
	if r.wakeFn == nil {
		logging.Debug("wake skipped: no waker configured", map[string]any{
			"flow_session_id": sessionID,
		})
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
		logging.Debug("wake skipped: session running", map[string]any{
			"flow_session_id": sessionID,
			"session_status":  string(session.Status),
		})
		return
	}
	switch session.Status {
	case types.SessionWaitingInput,
		types.SessionWaitingTool,
		types.SessionWaitingApproval,
		types.SessionCreated:
		logging.Debug("waking session", map[string]any{
			"flow_session_id": sessionID,
			"session_status":  string(session.Status),
		})
		r.wakeFn(sessionID)
	default:
		// Failed / cancelled / interrupted / completed are terminal or
		// externally controlled; do not wake.
		logging.Debug("wake skipped: session not idle", map[string]any{
			"flow_session_id": sessionID,
			"session_status":  string(session.Status),
		})
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
		// Logged, not returned: a bookkeeping write must not turn a
		// successfully dispatched turn into a failed one.
		logging.Error("record mailbox batch dispatched", map[string]any{
			"flow_session_id": session.ID,
			"flow_turn_id":    turn.ID,
			"error":           err.Error(),
		})
		return
	}
	logging.Debug("recorded mailbox batch dispatched", map[string]any{
		"flow_session_id": session.ID,
		"flow_turn_id":    turn.ID,
		"message_count":   len(messageIDs),
	})
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
