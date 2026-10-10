package flow

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// blockingTeamRuntime blocks the activated "verify" team on a gate channel so a
// test can prove the dispatching turn does not wait for it. The coordinator
// ("default") activates "verify" once, exactly like fakeTeamRuntime.
type blockingTeamRuntime struct {
	gate    chan struct{} // closed to release "verify"
	started chan string   // receives the team ID when "verify" starts

	mu        sync.Mutex
	activated bool
}

func (b *blockingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		b.mu.Lock()
		activated := b.activated
		b.activated = true
		b.mu.Unlock()
		if activated {
			return types.TeamTurnResult{
				Turn:  req.TeamTurn,
				Reply: "coordination finished",
				Next:  &types.Route{Action: types.NextProceed},
			}, nil
		}
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "starting diagnosis",
			Next: &types.Route{
				Action: types.NextActivate,
				Teams:  []string{"verify"},
			},
		}, nil
	case "verify":
		if b.started != nil {
			select {
			case b.started <- req.TeamTurn.TeamID:
			default:
			}
		}
		<-b.gate // block until the test releases the team
		return types.TeamTurnResult{
			Turn:    req.TeamTurn,
			Reply:   "verification passed",
			Records: []types.SharedRecord{{RecordID: "verification-1", Name: "VerificationReport", Scope: types.RecordScopeFlow, Status: types.RecordActive, Revision: 1}},
			Next:    &types.Route{Action: types.NextReturn},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

// TestAsyncDispatchDoesNotBlockOnDispatchedTeam is the core P0 assertion: an
// async-dispatched team is executed in the background, so the coordinator's
// FlowTurn returns immediately (waiting_input) even while the team is still
// blocked; after the team is released, the work message lands delivered and a
// pending result message appears in the coordinator's inbox.
func TestAsyncDispatchDoesNotBlockOnDispatchedTeam(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	fake := &blockingTeamRuntime{
		gate:    make(chan struct{}),
		started: make(chan string, 1),
	}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, evidenceStore)
	runtime.SetAsyncDispatch(true)

	startedAt := time.Now()
	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)
	elapsed := time.Since(startedAt)

	// The turn returned without waiting on the still-blocked "verify" team.
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Equal(t, types.TurnWaitingInput, result.Turn.Status)
	require.Less(t, elapsed, 2*time.Second, "dispatch turn must not block on the team")
	require.Len(t, result.TeamResults, 1, "only the coordinator ran inside the turn")

	// The work message is claimed once the background dispatcher picks it up.
	// Claiming is asynchronous (it happens in the goroutine), so wait for it
	// rather than reading a single instantaneous snapshot.
	box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
	require.NoError(t, err)
	workID := ""
	for id, m := range box {
		if m.Kind == mailboxKindWork && m.To.TeamID == "verify" {
			workID = id
			break
		}
	}
	require.NotEmpty(t, workID, "expected a work message for the dispatched team")
	<-fake.started
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
		if err != nil {
			return false
		}
		return box[workID].Status == types.MailboxClaimed
	}, 5*time.Second, 20*time.Millisecond)

	// Release the team and wait for the result message to be posted.
	close(fake.gate)
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
		if err != nil {
			return false
		}
		if box[workID].Status != types.MailboxDelivered {
			return false
		}
		for _, msg := range box {
			if msg.Kind == mailboxKindResult && msg.To.TeamID == "default" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

	box, err = runtime.loadMailbox(context.Background(), result.Session.ID)
	require.NoError(t, err)
	require.Equal(t, types.MailboxDelivered, box[workID].Status)

	var resultMsg types.MailboxMessage
	found := false
	for _, msg := range box {
		if msg.Kind == mailboxKindResult && msg.To.TeamID == "default" {
			resultMsg = msg
			found = true
			break
		}
	}
	require.True(t, found, "expected a result message in the coordinator inbox")
	require.Equal(t, types.MailboxPending, resultMsg.Status)
	require.Equal(t, result.Turn.ID, resultMsg.CauseFlowTurnID)
	// The result container keeps its published Go field names (team_result →
	// TeamTurnResult), not a re-serialized snake_case shape.
	require.Contains(t, resultMsg.Payload, "team_result")
}

// TestAsyncDispatchRecordsBatchDispatchedOnce is the regression guard for a
// duplicate EventMailboxBatchDispatched: the coordinator's dispatch turn ends
// via the queue-empty path (queue drains, no wait case matches), and that path
// used to record the batch-dispatched event and then fall through to the
// loop-exit path which recorded it a second time. Exactly one event must exist
// for the turn.
func TestAsyncDispatchRecordsBatchDispatchedOnce(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &blockingTeamRuntime{
		gate:    make(chan struct{}),
		started: make(chan string, 1),
	}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)

	// Release the background team and wait for the goroutine to finish its
	// final session write before reading events. The dispatch goroutine owns
	// its own lifetime; it keeps appending to flow.jsonl (delivered + result
	// events) after Start returns. Replaying without this synchronization
	// races t.TempDir()'s RemoveAll against those writes and flakes with
	// "directory not empty" on Linux.
	<-fake.started
	close(fake.gate)

	// The result message is the last thing the goroutine records; once it is
	// visible the dispatcher has no more writes pending for this session.
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
		if err != nil {
			return false
		}
		for _, msg := range box {
			if msg.Kind == mailboxKindResult && msg.To.TeamID == "default" {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)

	replay, err := sessionWriter.Replay(context.Background(), result.Session.ID)
	require.NoError(t, err)
	dispatched := 0
	var messageIDs []string
	for _, event := range replay.Events {
		if event.Type != types.EventMailboxBatchDispatched {
			continue
		}
		require.Equal(t, result.Turn.ID, event.FlowTurnID,
			"batch-dispatched event must belong to the dispatching turn")
		dispatched++
		if ids, ok := event.Payload["message_ids"].([]any); ok {
			for _, id := range ids {
				messageIDs = append(messageIDs, fmt.Sprint(id))
			}
		}
	}
	require.Equal(t, 1, dispatched, "batch-dispatched must be recorded exactly once per turn")
	require.Len(t, messageIDs, 1, "the single event carries the dispatched work message")
}

// TestAsyncDispatchDisabledKeepsSynchronousBehaviour is the no-op contract:
// with dispatch off (the default), the coordinator waits for the activated
// team and its result is part of the turn's TeamResults.
func TestAsyncDispatchDisabledKeepsSynchronousBehaviour(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)
	// Default is off; set it explicitly to prove the switch is consulted.
	runtime.SetAsyncDispatch(false)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Contains(t, result.Reply, "verification passed")
	require.Len(t, result.TeamResults, 3)

	// No mailbox events are written when dispatch is off.
	replay, err := sessionWriter.Replay(context.Background(), result.Session.ID)
	require.NoError(t, err)
	for _, event := range replay.Events {
		require.NotEqual(t, types.EventMailboxMessageEnqueued, event.Type)
		require.NotEqual(t, types.EventMailboxBatchDispatched, event.Type)
	}
}

// TestAsyncDispatchWakesOnlyWhenIdle pins the wake condition: the waker is
// called after a background dispatch finishes while the coordinator is idle,
// and is not called while the coordinator is still running.
func TestAsyncDispatchWakesOnlyWhenIdle(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &blockingTeamRuntime{
		gate:    make(chan struct{}),
		started: make(chan string, 1),
	}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)
	runtime.SetAsyncDispatch(true)

	var mu sync.Mutex
	var wakes []string
	runtime.SetWaker(func(sessionID string) {
		mu.Lock()
		wakes = append(wakes, sessionID)
		mu.Unlock()
	})
	wakeCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(wakes)
	}

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)

	// The team is blocked, so its dispatch has not finished; no wake yet.
	<-fake.started
	require.Eventually(t, func() bool {
		// The session is idle (Start already returned waiting_input) but the
		// background team has not reported, so the waker must not have fired.
		return wakeCount() == 0
	}, 300*time.Millisecond, 20*time.Millisecond)

	// Release the team: the dispatch completes while the session is idle, so
	// the waker fires exactly once with this session ID.
	close(fake.gate)
	require.Eventually(t, func() bool { return wakeCount() == 1 }, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, []string{result.Session.ID}, wakes)
}

// TestAsyncDispatchDoesNotWakeRunningSession proves the running guard: a
// dispatch that completes while the session is still running records its
// result but does not wake.
func TestAsyncDispatchDoesNotWakeRunningSession(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &blockingTeamRuntime{
		gate:    make(chan struct{}),
		started: make(chan string, 1),
	}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)
	runtime.SetAsyncDispatch(true)

	var mu sync.Mutex
	var wakes []string
	runtime.SetWaker(func(sessionID string) {
		mu.Lock()
		wakes = append(wakes, sessionID)
		mu.Unlock()
	})
	wakeCount := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(wakes)
	}

	// Drive the turn directly so we can hold the session in "running" while
	// the background dispatch completes.
	ctx := context.Background()
	session := types.FlowSession{
		ID:        newID("fs"),
		FlowID:    "code-fix",
		Status:    types.SessionCreated,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": session},
	}))

	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID, Status: types.TurnRunning}
	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	// Hold the session in running and start the dispatch.
	running := session
	running.Status = types.SessionRunning
	require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": running},
	}))
	runtime.launchAsyncDispatch(session, turn.ID, "verify", msg)

	<-fake.started
	close(fake.gate)

	// The dispatch finishes, but the session is running, so no wake.
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(ctx, session.ID)
		if err != nil {
			return false
		}
		for _, m := range box {
			if m.Kind == mailboxKindResult {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 0, wakeCount(), "running session must not be woken")
}
