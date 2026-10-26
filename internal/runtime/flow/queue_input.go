package flow

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// mailboxKindUser is a message the user sent while the session was busy.
//
// The mailbox carries two things, not one: results coming back from a Team, and
// input arriving mid-turn. Both are "work for the next turn", and both have the
// same problem — the current turn cannot be interrupted, so they must wait
// somewhere durable. Refusing the message instead is what made a second question
// fail outright while a first one was still running its follow-up turns.
const mailboxKindUser = "user"

// userInputQueuedReply is what the caller shows the user when their message was
// stored rather than executed.
const userInputQueuedReply = "已收到，当前任务完成后继续处理。"

// queueUserInput stores a message that arrived while the session was running.
//
// Returning it as queued rather than as an error keeps the promise the user
// made by sending it: the message is not rejected, only deferred. The turn that
// is running will start it as soon as it finishes (see drainQueuedInput).
func (r *Runtime) queueUserInput(ctx context.Context, session types.FlowSession, input string) (types.FlowTurnResult, error) {
	defs := r.definitions.Snapshot()
	entry := defs.Flow.EntryTeamID
	if entry == "" {
		return types.FlowTurnResult{}, fmt.Errorf("flow has no entry team; cannot queue input")
	}
	if _, err := r.enqueueMessage(ctx, session, types.FlowTurn{},
		types.MailboxAddress{TeamID: entry},
		mailboxKindUser,
		map[string]any{"input": input},
	); err != nil {
		return types.FlowTurnResult{}, err
	}
	logging.Info("user input queued behind a running turn", map[string]any{
		"flow_session_id": session.ID,
		"session_status":  string(session.Status),
	})
	return types.FlowTurnResult{
		Session: session,
		Queued:  true,
		Reply:   userInputQueuedReply,
	}, nil
}

// drainQueuedInput runs the next stored user message, if any.
//
// It is called after a turn finishes, in its own goroutine: the finished turn
// still holds the caller's lock, and starting the next one inline would either
// recurse or deadlock. The lock is taken here so two drains cannot start two
// turns at once.
func (r *Runtime) drainQueuedInput(ctx context.Context, sessionID string) {
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()

	for {
		next, session, ok := r.pendingUserInput(ctx, sessionID)
		if !ok {
			return
		}
		claimed, err := r.claimMessage(ctx, session, next)
		if err != nil {
			logging.Error("claim queued user input", map[string]any{
				"flow_session_id": sessionID,
				"message_id":      next.MessageID,
				"error":           err.Error(),
			})
			return
		}
		input, _ := claimed.Payload["input"].(string)
		if _, err := r.runTurnWithOrigin(ctx, r.definitions.Snapshot(), session, input, nil,
			[]activation{{teamID: r.definitions.Snapshot().Flow.EntryTeamID}},
			turnOriginUser, time.Now().UTC(), nil); err != nil {
			logging.Error("run queued user input", map[string]any{
				"flow_session_id": sessionID,
				"error":           err.Error(),
			})
			return
		}
	}
}

// pendingUserInput returns the oldest stored user message, if the session is
// idle enough to run it.
func (r *Runtime) pendingUserInput(ctx context.Context, sessionID string) (types.MailboxMessage, types.FlowSession, bool) {
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return types.MailboxMessage{}, types.FlowSession{}, false
	}
	switch session.Status {
	case types.SessionWaitingInput, types.SessionCreated:
	default:
		// Still running: the current turn will call us again when it ends.
		return types.MailboxMessage{}, types.FlowSession{}, false
	}
	box, err := r.loadMailbox(ctx, sessionID)
	if err != nil {
		return types.MailboxMessage{}, types.FlowSession{}, false
	}
	for _, msg := range box {
		if msg.Kind == mailboxKindUser && msg.Status == types.MailboxPending {
			return msg, session, true
		}
	}
	return types.MailboxMessage{}, types.FlowSession{}, false
}
