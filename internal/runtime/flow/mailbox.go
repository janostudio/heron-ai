package flow

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// mailbox events carry the full message under payload["message"], mirroring the
// payload:{"record": record} convention used elsewhere in this package. Every
// state transition re-emits the whole message with an incremented Revision, so
// a reader can fold the event stream into the current state of each message
// without any side-channel.
const mailboxPayloadKey = "message"

// mailboxMessageKind identifies the two message flavours: dispatched work and
// its result.
const (
	mailboxKindWork   = "work"
	mailboxKindResult = "result"
)

// enqueueWorkMessage records a dispatched work message (a team handing work to
// a mailbox) and returns it. It is a fresh message: Status pending, Revision 0.
//
// The event is written without any lock, matching appendSessionEvent's style:
// callers are responsible for ordering their own writes.
func (r *Runtime) enqueueWorkMessage(
	ctx context.Context,
	session types.FlowSession,
	causeTurn types.FlowTurn,
	to types.MailboxAddress,
	payload map[string]any,
) (types.MailboxMessage, error) {
	return r.enqueueMessage(ctx, session, causeTurn, to, mailboxKindWork, payload)
}

// enqueueResultMessage records a result message (a worker reporting back to its
// coordinator) and returns it.
func (r *Runtime) enqueueResultMessage(
	ctx context.Context,
	session types.FlowSession,
	causeTurn types.FlowTurn,
	to types.MailboxAddress,
	payload map[string]any,
) (types.MailboxMessage, error) {
	return r.enqueueMessage(ctx, session, causeTurn, to, mailboxKindResult, payload)
}

func (r *Runtime) enqueueMessage(
	ctx context.Context,
	session types.FlowSession,
	causeTurn types.FlowTurn,
	to types.MailboxAddress,
	kind string,
	payload map[string]any,
) (types.MailboxMessage, error) {
	now := time.Now().UTC()
	msg := types.MailboxMessage{
		MessageID:       newID("mb"),
		FlowSessionID:   session.ID,
		To:              to,
		Kind:            kind,
		Payload:         payload,
		CauseFlowTurnID: causeTurn.ID,
		Status:          types.MailboxPending,
		Revision:        0,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := r.appendMailboxEvent(ctx, types.EventMailboxMessageEnqueued, session, causeTurn, msg); err != nil {
		return types.MailboxMessage{}, err
	}
	return msg, nil
}

// claimMessage marks a message claimed (a turn has picked it up to execute),
// Revision+1, and returns the updated message.
func (r *Runtime) claimMessage(
	ctx context.Context,
	session types.FlowSession,
	msg types.MailboxMessage,
) (types.MailboxMessage, error) {
	msg.Status = types.MailboxClaimed
	msg.Revision++
	msg.UpdatedAt = time.Now().UTC()
	if err := r.appendMailboxEvent(ctx, types.EventMailboxMessageClaimed, session, types.FlowTurn{}, msg); err != nil {
		return types.MailboxMessage{}, err
	}
	return msg, nil
}

// deliverMessage marks a message delivered (consumed, and it produced a result),
// Revision+1. The optional result is recorded on the message so the folded state
// carries the produced output.
func (r *Runtime) deliverMessage(
	ctx context.Context,
	session types.FlowSession,
	msg types.MailboxMessage,
	result map[string]any,
) (types.MailboxMessage, error) {
	msg.Status = types.MailboxDelivered
	msg.Revision++
	msg.UpdatedAt = time.Now().UTC()
	if result != nil {
		if msg.Payload == nil {
			msg.Payload = map[string]any{}
		}
		msg.Payload["result"] = result
	}
	if err := r.appendMailboxEvent(ctx, types.EventMailboxMessageDelivered, session, types.FlowTurn{}, msg); err != nil {
		return types.MailboxMessage{}, err
	}
	return msg, nil
}

// markMessageDead marks a message dead (failed, retry limit exceeded) with the
// reason recorded on the message, Revision+1.
func (r *Runtime) markMessageDead(
	ctx context.Context,
	session types.FlowSession,
	msg types.MailboxMessage,
	reason string,
) (types.MailboxMessage, error) {
	msg.Status = types.MailboxDead
	msg.Revision++
	msg.UpdatedAt = time.Now().UTC()
	if strings.TrimSpace(reason) != "" {
		if msg.Payload == nil {
			msg.Payload = map[string]any{}
		}
		msg.Payload["dead_reason"] = reason
	}
	if err := r.appendMailboxEvent(ctx, types.EventMailboxMessageDead, session, types.FlowTurn{}, msg); err != nil {
		return types.MailboxMessage{}, err
	}
	return msg, nil
}

// appendMailboxEvent writes one mailbox event. The address-derived header fields
// (TeamID) come from the message; claimed/delivered/dead carry an empty cause
// turn this step (a later step fills in the acting team/turn).
func (r *Runtime) appendMailboxEvent(
	ctx context.Context,
	eventType string,
	session types.FlowSession,
	causeTurn types.FlowTurn,
	msg types.MailboxMessage,
) error {
	return r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: session.ID,
			FlowTurnID:    causeTurn.ID,
			TeamID:        msg.To.TeamID,
		},
		Payload: map[string]any{mailboxPayloadKey: msg},
	})
}

// loadMailbox replays a session's flow events and folds them into the current
// state of every mailbox message, keyed by MessageID.
//
// The fold is deterministic and independent of event arrival order: for each
// MessageID the highest Revision wins, and a Revision tie is broken by the
// larger event Seq. It reads the same flow.jsonl the events were written to.
func (r *Runtime) loadMailbox(ctx context.Context, sessionID string) (map[string]types.MailboxMessage, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return foldMailbox(events), nil
}

// listInbox returns the pending messages addressed to `to`, ordered by
// CreatedAt ascending with MessageID as a stable tiebreaker.
func (r *Runtime) listInbox(ctx context.Context, sessionID string, to types.MailboxAddress) ([]types.MailboxMessage, error) {
	box, err := r.loadMailbox(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	var inbox []types.MailboxMessage
	for _, msg := range box {
		if msg.Status != types.MailboxPending {
			continue
		}
		if !sameAddress(msg.To, to) {
			continue
		}
		inbox = append(inbox, msg)
	}
	sort.Slice(inbox, func(i, j int) bool {
		if !inbox[i].CreatedAt.Equal(inbox[j].CreatedAt) {
			return inbox[i].CreatedAt.Before(inbox[j].CreatedAt)
		}
		return inbox[i].MessageID < inbox[j].MessageID
	})
	return inbox, nil
}

// replayFlowEvents returns the session's events sorted by Seq ascending. A
// session that has never written a flow.jsonl is treated as empty rather than
// an error, so loadMailbox on a fresh session yields an empty mailbox.
func (r *Runtime) replayFlowEvents(ctx context.Context, sessionID string) ([]storage.SessionEvent, error) {
	if r.sessions == nil {
		return nil, errors.New("session writer is nil")
	}
	replay, err := r.sessions.Replay(ctx, sessionID)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := make([]storage.SessionEvent, len(replay.Events))
	copy(events, replay.Events)
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Seq < events[j].Seq
	})
	return events, nil
}

// sameAddress reports whether two mailbox addresses are equal. Empty fields are
// compared literally: a team-level inbox (AgentID "") is distinct from any
// agent-level inbox.
func sameAddress(a, b types.MailboxAddress) bool {
	return a.TeamID == b.TeamID && a.AgentID == b.AgentID
}

// mailboxStateEventTypes mirrors the mailbox lifecycle events; only these carry
// a payload["message"] the fold understands.
var mailboxStateEventTypes = map[string]bool{
	types.EventMailboxMessageEnqueued:  true,
	types.EventMailboxMessageClaimed:   true,
	types.EventMailboxMessageDelivered: true,
	types.EventMailboxMessageDead:      true,
}

// foldMailbox folds a globally-ordered event slice into the per-message current
// state. It is the pure core of loadMailbox: given the same multiset of events
// it returns the same result no matter what order they are presented in.
func foldMailbox(events []storage.SessionEvent) map[string]types.MailboxMessage {
	type winner struct {
		msg types.MailboxMessage
		rev int
		seq int64
	}
	best := map[string]winner{}
	for _, event := range events {
		if !mailboxStateEventTypes[event.Type] {
			continue
		}
		msg, ok := decodeMailboxMessage(event.Payload)
		if !ok || msg.MessageID == "" {
			continue
		}
		cur, seen := best[msg.MessageID]
		if !seen || betterMailboxState(cur.rev, cur.seq, msg.Revision, event.Seq) {
			best[msg.MessageID] = winner{msg: msg, rev: msg.Revision, seq: event.Seq}
		}
	}
	out := make(map[string]types.MailboxMessage, len(best))
	for id, w := range best {
		out[id] = w.msg
	}
	return out
}

// betterMailboxState reports whether the candidate (candRev, candSeq) beats the
// incumbent (curRev, curSeq): higher Revision wins, and a Revision tie is broken
// by the larger Seq.
func betterMailboxState(curRev int, curSeq int64, candRev int, candSeq int64) bool {
	if candRev != curRev {
		return candRev > curRev
	}
	return candSeq > curSeq
}

// decodeMailboxMessage pulls the MailboxMessage out of an event payload. The
// event is JSON-round-tripped on the way off disk, so the value arrives as a
// generic map; re-marshalling it back into the typed struct is the canonical
// decode.
func decodeMailboxMessage(payload map[string]any) (types.MailboxMessage, bool) {
	if payload == nil {
		return types.MailboxMessage{}, false
	}
	raw, ok := payload[mailboxPayloadKey]
	if !ok {
		return types.MailboxMessage{}, false
	}
	msg, err := decodeMailboxValue(raw)
	if err != nil {
		return types.MailboxMessage{}, false
	}
	return msg, true
}

// decodeMailboxValue re-marshals an arbitrary decoded JSON value back into a
// MailboxMessage. Events read straight from memory hold the typed struct, events
// read from JSONL hold a generic map; the round-trip handles both.
func decodeMailboxValue(raw any) (types.MailboxMessage, error) {
	switch v := raw.(type) {
	case types.MailboxMessage:
		return v, nil
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return types.MailboxMessage{}, err
		}
		var msg types.MailboxMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return types.MailboxMessage{}, err
		}
		return msg, nil
	default:
		data, err := json.Marshal(raw)
		if err != nil {
			return types.MailboxMessage{}, err
		}
		var msg types.MailboxMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			return types.MailboxMessage{}, err
		}
		return msg, nil
	}
}
