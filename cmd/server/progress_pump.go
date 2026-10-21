package main

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	// progressPumpDrainTimeout bounds how long a turn waits for the pump to
	// deliver the events it already holds. The terminal response must never
	// be blocked by a slow or dead subscriber.
	progressPumpDrainTimeout = 5 * time.Second
	// progressPumpReconnects bounds how many times the pump re-subscribes
	// after storage dropped it for falling behind.
	progressPumpReconnects = 2
	// followAsyncPoll is how often a following pump re-checks whether the
	// session still has background work.
	followAsyncPoll = 500 * time.Millisecond
	// followAsyncHold is how long the session must look quiescent before the
	// subscription closes. A dispatch turn ends waiting_input and the
	// mailbox wake-up starts a fresh turn a moment later, so an instant
	// "no work" reading would close the window before the answer arrives.
	followAsyncHold = 3 * time.Second
	// followAsyncMaxWait caps how long one turn may keep following. A model
	// call can legitimately take minutes; a wedged Team must not hold the
	// subscription (and the caller's stdout) forever.
	followAsyncMaxWait = 15 * time.Minute
)

// eventPump forwards the session events of one flow turn to a JSON-RPC
// caller as "progress" notifications.
//
// Only the pump writes notifications; only the serve loop writes responses.
// Both go through jsonRPCServer.write which serialises them, so a long turn
// cannot interleave half a notification into a response line.
type eventPump struct {
	sessions storage.SessionWriter
	emit     func(params map[string]any) error

	flowTurnID string
	afterSeq   int64
	// lastSeq is the highest seq handed to emit. Notifications are strictly
	// increasing so a client can drop duplicates.
	lastSeq int64

	cancel  context.CancelFunc
	done    chan struct{}
	dropped int
}

func startEventPump(
	parent context.Context,
	sessions storage.SessionWriter,
	sessionID string,
	flowTurnID string,
	afterSeq int64,
	emit func(params map[string]any) error,
) *eventPump {
	pump := &eventPump{
		sessions:   sessions,
		emit:       emit,
		flowTurnID: flowTurnID,
		afterSeq:   afterSeq,
		lastSeq:    afterSeq,
		done:       make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(parent)
	pump.cancel = cancel
	go pump.run(ctx, sessionID)
	return pump
}

func (p *eventPump) run(ctx context.Context, sessionID string) {
	defer close(p.done)
	reconnects := 0
	for {
		channel, err := p.subscribe(ctx, sessionID)
		if err != nil {
			// A session with no events yet has nothing to forward; the
			// terminal response still carries the turn result.
			logging.Debug("progress pump: subscribe failed", map[string]any{
				"session_id": sessionID,
				"error":      err.Error(),
			})
			return
		}
		closed := p.consume(ctx, channel)
		if !closed {
			return
		}
		reconnects++
		p.dropped++
		if reconnects > progressPumpReconnects {
			logging.Warn("progress pump: giving up after subscriber drops", map[string]any{
				"session_id":   sessionID,
				"flow_turn_id": p.flowTurnID,
				"last_seq":     p.lastSeq,
			})
			return
		}
	}
}

// subscribe registers a subscriber that backfills everything after lastSeq,
// so a reconnect never loses the events that arrived while storage had
// dropped us.
func (p *eventPump) subscribe(ctx context.Context, sessionID string) (<-chan storage.SessionEvent, error) {
	return p.sessions.Subscribe(ctx, sessionID, p.lastSeq)
}

// consume drains one subscription until the subscription context is
// cancelled or storage drops the subscriber.
//
// It deliberately does NOT select on p.done: Stop signals by cancelling the
// subscription and then waits for this loop to finish, so that the events
// already buffered are still delivered before the terminal response. Mixing
// both signals is also what let an event sent during Subscribe registration
// win the select race and be discarded.
func (p *eventPump) consume(ctx context.Context, channel <-chan storage.SessionEvent) bool {
	for {
		select {
		case event, ok := <-channel:
			if !ok {
				return true
			}
			p.handle(event)
		case <-ctx.Done():
			// Drain whatever storage already handed over, never block.
			for {
				select {
				case event, ok := <-channel:
					if !ok {
						return false
					}
					p.handle(event)
				default:
					return false
				}
			}
		}
	}
}

func (p *eventPump) handle(event storage.SessionEvent) {
	if event.Seq <= p.lastSeq {
		return
	}
	if p.flowTurnID != "" && event.FlowTurnID != p.flowTurnID {
		return
	}
	params := projectSessionEvent(event)
	if params == nil {
		return
	}
	params = enforceNotificationSize(params)
	// Advance the cursor even when emit fails: a reconnect replays from
	// lastSeq, and retrying the same broken event forever would wedge the
	// drain loop.
	p.lastSeq = event.Seq
	if err := p.emit(params); err != nil {
		logging.Warn("progress pump: emit failed", map[string]any{
			"session_id": event.FlowSessionID,
			"seq":        event.Seq,
			"error":      err.Error(),
		})
	}
}

// waitForIdle keeps the subscription open until the session stops producing
// background work, so a caller that asked to follow async turns receives the
// events of turns it never started. Without it the pump stops with the turn
// response and the background answer is written to storage with nobody
// listening.
//
// The idle check reads the event log rather than runtime state on purpose:
// the pump already owns a subscription and the log is the same source the
// caller sees, so the two cannot disagree about whether work is left.
func (p *eventPump) waitForIdle(ctx context.Context, sessionID string, maxWait time.Duration) {
	if p == nil || p.sessions == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	deadline := time.Now().Add(maxWait)
	ticker := time.NewTicker(followAsyncPoll)
	defer ticker.Stop()

	var idleSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Now().After(deadline) {
			logging.Warn("progress pump: follow_async timed out", map[string]any{
				"session_id": sessionID,
				"last_seq":   p.lastSeq,
			})
			return
		}
		replay, err := p.sessions.Replay(ctx, sessionID)
		if err != nil {
			logging.Debug("progress pump: follow_async replay failed", map[string]any{
				"session_id": sessionID,
				"error":      err.Error(),
			})
			return
		}
		if sessionHasBackgroundWork(replay.Events) {
			idleSince = time.Time{}
			continue
		}
		if idleSince.IsZero() {
			idleSince = time.Now()
			continue
		}
		if time.Since(idleSince) >= followAsyncHold {
			return
		}
	}
}

// sessionHasBackgroundWork reports whether the session still owes the caller
// something: dispatched mail that no Team has consumed, or a turn whose route
// still activates more Teams.
func sessionHasBackgroundWork(events []storage.SessionEvent) bool {
	var lastTerminal *storage.SessionEvent
	openMail := make(map[string]struct{})
	for i := range events {
		event := events[i]
		switch event.Type {
		case types.EventFlowTurnCompleted,
			types.EventFlowTurnWaitingInput,
			types.EventFlowTurnWaitingTool,
			types.EventFlowTurnWaitingApproval:
			lastTerminal = &events[i]
		case types.EventMailboxMessageEnqueued, types.EventMailboxMessageClaimed:
			if id := mailboxMessageID(event.Payload); id != "" {
				openMail[id] = struct{}{}
			}
		case types.EventMailboxMessageDelivered, types.EventMailboxMessageDead:
			if id := mailboxMessageID(event.Payload); id != "" {
				delete(openMail, id)
			}
		}
	}
	if len(openMail) > 0 {
		return true
	}
	if lastTerminal == nil {
		return false
	}
	return turnRoutesToActivation(lastTerminal.Payload)
}

func mailboxMessageID(payload map[string]any) string {
	switch message := payload["message"].(type) {
	case types.MailboxMessage:
		return message.MessageID
	case map[string]any:
		id, _ := message["message_id"].(string)
		return id
	default:
		return ""
	}
}

// turnRoutesToActivation reads the route of a finished turn. A turn that ends
// with activate has handed work to another Team and will be followed by
// another turn; anything else is the answer.
func turnRoutesToActivation(payload map[string]any) bool {
	switch turn := payload["turn"].(type) {
	case types.FlowTurn:
		return turn.Next != nil && turn.Next.Action == types.NextActivate
	case map[string]any:
		next, ok := turn["next"].(map[string]any)
		if !ok {
			return false
		}
		action, _ := next["action"].(string)
		return action == string(types.NextActivate)
	default:
		return false
	}
}

// Stop terminates the pump and waits for it to drain. It never returns an
// error to the caller: a stuck pump must not fail the turn.
func (p *eventPump) Stop() {
	if p == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
	case <-time.After(progressPumpDrainTimeout):
		logging.Warn("progress pump: drain timed out", map[string]any{
			"flow_turn_id": p.flowTurnID,
			"last_seq":     p.lastSeq,
		})
	}
}

// replayEventsSnapshot projects a finished turn's events in one batch. It is
// used for a brand-new session, where the id only exists once Start returns,
// so there was no subscription window to stream from.
func (p *eventPump) replayEventsSnapshot(ctx context.Context, sessionID string) {
	if p.sessions == nil {
		return
	}
	replay, err := p.sessions.Replay(ctx, sessionID)
	if err != nil {
		if !errors.Is(err, storage.ErrNotFound) {
			logging.Debug("progress pump: snapshot replay failed", map[string]any{
				"session_id": sessionID,
				"error":      err.Error(),
			})
		}
		return
	}
	for _, event := range replay.Events {
		p.handle(event)
	}
}

// turnSummaryFromResult emits the pending-state notification for a turn that
// ended parked instead of running to completion.
func turnSummaryFromResult(result types.FlowTurnResult, emit func(map[string]any) error) {
	params := statusNotificationFromResult(result)
	if params == nil {
		return
	}
	params = enforceNotificationSize(params)
	if err := emit(params); err != nil {
		logging.Debug("progress: failed to emit status notification", map[string]any{
			"session_id": result.Session.ID,
			"error":      err.Error(),
		})
	}
}
