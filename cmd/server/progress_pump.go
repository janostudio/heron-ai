package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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
	// followAsyncMaxWait caps how long one turn may keep following. A model
	// call can legitimately take minutes; a wedged Team must not hold the
	// subscription (and the caller's stdout) forever.
	followAsyncMaxWait = 15 * time.Minute
)

// The follow cadence is a var so tests can shorten it; production keeps the
// values below.
var (
	// followAsyncPoll is how often a following pump re-checks whether the
	// session still has background work.
	followAsyncPoll = 500 * time.Millisecond
	// followAsyncHold is how long the session must look quiescent before the
	// subscription closes. A dispatch turn ends waiting_input and the
	// mailbox wake-up starts a fresh turn a moment later, so an instant
	// "no work" reading would close the window before the answer arrives.
	followAsyncHold = 3 * time.Second
	// followAsyncBusyPoll throttles the task-store lookup that tells the
	// follower about work the event log cannot see (a running TeamRun task).
	// Checking every poll would re-read session state ten times a second for a
	// signal that only changes when a Team finishes.
	followAsyncBusyPoll = 2 * time.Second
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
	// followAll stops the per-turn filter once the caller starts waiting for
	// turns it did not start (follow_async). Set by waitForIdle; never cleared,
	// because a pump that follows is released when following ends.
	followAll atomic.Bool
	// lastSeq is the highest seq handed to emit. Notifications are strictly
	// increasing so a client can drop duplicates.
	//
	// atomic because two goroutines touch it: the subscription loop stores it,
	// and waitForIdle / subscribe read it while reporting and reconnecting. A
	// plain int64 there was a data race the race detector reports.
	lastSeq atomic.Int64

	cancel  context.CancelFunc
	done    chan struct{}
	dropped int

	// work is the incrementally maintained background-work state that
	// waitForIdle reads. workMu guards it: the subscription goroutine writes,
	// the follower goroutine reads.
	workMu sync.Mutex
	work   backgroundWork

	// busy reports outstanding work the event log cannot show. A TeamRun tool
	// task lives in the task store, not in flow.jsonl, so a dispatch turn ends
	// waiting_input with nothing left to fold while the dispatched Team is still
	// running: backgroundPending() reads false, the follower closes the
	// subscription three seconds later, and the answer written afterwards never
	// reaches the caller (s24: the conclusion landed at 17:49 while the
	// subscription had already stopped at 17:44).
	//
	// Nil means "no extra source", which keeps every existing caller and test
	// working unchanged.
	busy func(ctx context.Context, sessionID string) bool
	// busyAt/busyResult throttle that lookup: the follower ticks every
	// followAsyncPoll, and reading session state that often is needless IO.
	busyMu     sync.Mutex
	busyAt     time.Time
	busyResult bool
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
		done:       make(chan struct{}),
	}
	pump.lastSeq.Store(afterSeq)
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
				"last_seq":     p.lastSeq.Load(),
			})
			return
		}
	}
}

// subscribe registers a subscriber that backfills everything after lastSeq,
// so a reconnect never loses the events that arrived while storage had
// dropped us.
func (p *eventPump) subscribe(ctx context.Context, sessionID string) (<-chan storage.SessionEvent, error) {
	return p.sessions.Subscribe(ctx, sessionID, p.lastSeq.Load())
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
	// Track before the turn filter: the follower asks about the whole session,
	// and events belonging to other turns still change whether work is left.
	p.track(event)
	if event.Seq <= p.lastSeq.Load() {
		return
	}
	// The turn filter exists so a caller sees only the turn it asked about.
	// While following async work the caller is waiting for a DIFFERENT turn:
	// the dispatch turn ends, the engine starts the follow-up one, and its
	// status frame carries the answer. Filtering by the original turn threw that
	// frame away and the user never saw the result (A→B fixture, 2026-10-28).
	if !p.followAll.Load() && p.flowTurnID != "" && event.FlowTurnID != p.flowTurnID {
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
	p.lastSeq.Store(event.Seq)
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

	// One full replay seeds the state; every event after that arrives through
	// the subscription and is applied incrementally (track).
	p.seedBackgroundWork(ctx, sessionID)

	// From here on the caller is waiting for turns it did not start, so the
	// per-turn filter must stop applying.
	p.followAll.Store(true)
	logging.Info("progress pump: following", map[string]any{
		"session_id": sessionID,
		"last_seq":   p.lastSeq.Load(),
	})

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
				"last_seq":   p.lastSeq.Load(),
			})
			return
		}
		if p.backgroundPending() || p.sessionBusy(ctx, sessionID) {
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

// seedBackgroundWork establishes the initial background-work state from the
// events already on disk. The subscription only carries events after this
// point, so the history has to be folded once.
func (p *eventPump) seedBackgroundWork(ctx context.Context, sessionID string) {
	replay, err := p.sessions.Replay(ctx, sessionID)
	if err != nil {
		logging.Debug("progress pump: follow_async replay failed", map[string]any{
			"session_id": sessionID,
			"error":      err.Error(),
		})
		return
	}
	state := backgroundWorkFromEvents(replay.Events)
	p.workMu.Lock()
	p.work = state
	p.workMu.Unlock()
}

// track applies one event to the background-work state. Called for every pumped
// event regardless of which turn it belongs to: the follower asks about the
// session, not about one turn.
func (p *eventPump) track(event storage.SessionEvent) {
	p.workMu.Lock()
	defer p.workMu.Unlock()
	if p.work.openMail == nil {
		p.work.openMail = make(map[string]struct{})
	}
	p.work.apply(event)
}

func (p *eventPump) backgroundPending() bool {
	p.workMu.Lock()
	defer p.workMu.Unlock()
	return p.work.pending()
}

// sessionBusy asks the caller-supplied source whether the session still owes
// work that the event log does not record. Result is cached for
// followAsyncBusyPoll so the 500ms follower loop does not re-read session
// state on every tick.
func (p *eventPump) sessionBusy(ctx context.Context, sessionID string) bool {
	if p.busy == nil {
		return false
	}
	p.busyMu.Lock()
	defer p.busyMu.Unlock()
	if time.Since(p.busyAt) < followAsyncBusyPoll {
		return p.busyResult
	}
	p.busyResult = p.busy(ctx, sessionID)
	p.busyAt = time.Now()
	return p.busyResult
}

// backgroundWork is the part of the event log that decides whether a session
// still owes the caller something. Tracking it incrementally is what keeps
// follow_async from re-reading the whole session every poll: at the default
// 500ms interval a 15-minute follow used to parse the entire JSONL ~1800 times,
// each parse holding the writer's global lock and blocking every event append.
type backgroundWork struct {
	openMail  map[string]struct{}
	openTeams map[string]struct{}
	// openFlows closes the gap between "the callee's team_turn completed" and
	// "the caller's follow-up FlowTurn started": at that instant no mailbox
	// message and no TeamTurn are open, so the follower used to stop before the
	// final answer existed.
	openFlows    map[string]struct{}
	lastTerminal *storage.SessionEvent
}

func newBackgroundWork() backgroundWork {
	return backgroundWork{
		openMail:  make(map[string]struct{}),
		openTeams: make(map[string]struct{}),
		openFlows: make(map[string]struct{}),
	}
}

// backgroundWorkFromEvents folds a complete event log. The follower uses the
// incremental form; this is the one-shot version for callers that already hold
// the events.
func backgroundWorkFromEvents(events []storage.SessionEvent) backgroundWork {
	state := newBackgroundWork()
	for _, event := range events {
		state.apply(event)
	}
	return state
}

func (b *backgroundWork) apply(event storage.SessionEvent) {
	if b.openFlows == nil {
		b.openFlows = make(map[string]struct{})
	}
	switch event.Type {
	case types.EventFlowTurnStarted:
		if id := strings.TrimSpace(event.FlowTurnID); id != "" {
			b.openFlows[id] = struct{}{}
		}
	case types.EventFlowTurnCompleted,
		types.EventFlowTurnWaitingInput,
		types.EventFlowTurnWaitingTool,
		types.EventFlowTurnWaitingApproval:
		if id := strings.TrimSpace(event.FlowTurnID); id != "" {
			delete(b.openFlows, id)
		}
		copied := event
		b.lastTerminal = &copied
	case types.EventMailboxMessageEnqueued, types.EventMailboxMessageClaimed:
		if id := mailboxMessageID(event.Payload); id != "" {
			b.openMail[id] = struct{}{}
		}
	case types.EventMailboxMessageDelivered, types.EventMailboxMessageDead:
		if id := mailboxMessageID(event.Payload); id != "" {
			delete(b.openMail, id)
		}
	case types.EventTeamTurnStarted:
		// A Team that is still executing is work the caller is waiting on,
		// even when the turn that activated it has already finished with a
		// plain proceed route and no mailbox message is open. Without this the
		// follower closed three seconds after the activator ended and the
		// replies of the following turns never reached the client.
		if id := event.TeamTurnID; id != "" {
			if b.openTeams == nil {
				b.openTeams = make(map[string]struct{})
			}
			b.openTeams[id] = struct{}{}
		}
	case types.EventTeamTurnCompleted,
		types.EventTeamTurnWaitingInput,
		types.EventTeamTurnWaitingTool,
		types.EventTeamTurnWaitingApproval:
		// A paused TeamTurn no longer owes the caller background work: the
		// engine will not continue on its own, and the pause event itself has
		// already been forwarded. Treating only completed as terminal would
		// keep the subscription alive until followAsyncMaxWait.
		if id := event.TeamTurnID; id != "" {
			delete(b.openTeams, id)
		}
	}
}

func (b backgroundWork) pending() bool {
	if len(b.openMail) > 0 || len(b.openTeams) > 0 || len(b.openFlows) > 0 {
		return true
	}
	if b.lastTerminal == nil {
		return false
	}
	return turnRoutesToActivation(b.lastTerminal.Payload)
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

// newSnapshotPump builds a pump for a turn whose events are already written.
// It has no goroutine: replayEventsSnapshot emits synchronously, so the pump is
// finished the moment it returns and done must start closed.
//
// It used to start open, and Stop waits progressPumpDrainTimeout (5s) on a done
// that nothing would ever close. That made every new session block the serial
// JSON-RPC read loop for five seconds after its response was written, and emit a
// "drain timed out" warning on the way out.
func newSnapshotPump(sessions storage.SessionWriter, emit func(map[string]any) error, flowTurnID string) *eventPump {
	done := make(chan struct{})
	close(done)
	return &eventPump{
		sessions:   sessions,
		emit:       emit,
		flowTurnID: flowTurnID,
		done:       done,
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
			"last_seq":     p.lastSeq.Load(),
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
