package flow

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// failingTeamRuntime makes the dispatched "verify" Team fail with a fixed
// error. It is the failure counterpart of fakeTeamRuntime: the coordinator
// behaves the same, the dispatched Team does not.
type failingTeamRuntime struct {
	mu     sync.Mutex
	verify int
}

func (f *failingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "coordination finished",
			Next:  &types.Route{Action: types.NextProceed},
		}, nil
	case "verify":
		f.mu.Lock()
		f.verify++
		f.mu.Unlock()
		return types.TeamTurnResult{Turn: req.TeamTurn}, errors.New("verify exploded")
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

func (f *failingTeamRuntime) verifyRuns() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.verify
}

// panickingTeamRuntime panics inside the dispatched "verify" Team. It proves
// the dispatcher's recover() contains a misbehaving Team: the message must be
// marked dead and the process must keep running.
type panickingTeamRuntime struct {
	runs int64
}

func (p *panickingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	if req.TeamTurn.TeamID == "verify" {
		atomic.AddInt64(&p.runs, 1)
		panic("verify panicked")
	}
	return types.TeamTurnResult{
		Turn:  req.TeamTurn,
		Reply: "coordination finished",
		Next:  &types.Route{Action: types.NextProceed},
	}, nil
}

// failingSessionWriter is a SessionWriter whose Append always fails. It is how
// a test forces the "could not record this event" branch that production code
// deliberately logs instead of escalating.
type failingSessionWriter struct {
	mu      sync.Mutex
	appends int
}

func (w *failingSessionWriter) Append(_ context.Context, _ string, _ storage.EventLayer, _ storage.SessionEvent) (storage.SessionEvent, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.appends++
	return storage.SessionEvent{}, errors.New("disk on fire")
}

func (w *failingSessionWriter) Replay(_ context.Context, sessionID string) (*storage.SessionReplay, error) {
	return &storage.SessionReplay{SessionID: sessionID}, nil
}

func (w *failingSessionWriter) Subscribe(_ context.Context, _ string, _ int64) (<-chan storage.SessionEvent, error) {
	ch := make(chan storage.SessionEvent)
	close(ch)
	return ch, nil
}

func (w *failingSessionWriter) appendCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appends
}

// TestDispatchWorkMarksMessageDeadOnTeamFailure covers the failure branch of a
// dispatched round: the work message goes dead carrying the Team's error, and
// the failure is reported back to the activating Team as a result message.
func TestDispatchWorkMarksMessageDeadOnTeamFailure(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &failingTeamRuntime{}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	runtime.launchAsyncDispatch(session, turn.ID, "verify", msg)
	waitForDispatchersDone(t, runtime, session.ID)

	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)

	work := box[msg.MessageID]
	require.Equal(t, types.MailboxDead, work.Status, "a failed round must leave the work message dead")
	require.Equal(t, "verify exploded", work.Payload["dead_reason"])

	// The failure is reported to the Team that dispatched it.
	var resultMsg types.MailboxMessage
	found := false
	for _, m := range box {
		if m.Kind == mailboxKindResult && m.To.TeamID == "default" {
			resultMsg = m
			found = true
			break
		}
	}
	require.True(t, found, "the failure must be reported back to the activating team")
	require.Equal(t, types.MailboxPending, resultMsg.Status)
	require.Equal(t, 1, fake.verifyRuns())

	// The Team's error travels inside the team_result container.
	raw, ok := resultMsg.Payload["team_result"]
	require.True(t, ok)
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var teamResult types.TeamTurnResult
	require.NoError(t, json.Unmarshal(data, &teamResult))
	require.Equal(t, "verify exploded", teamResult.Error)

	// And as a top-level "error" key: that is what renderMailboxInbox falls
	// back to (mailboxResultSummary) when team_result carries no Reply, so a
	// failed round must show its reason instead of "(no rendered reply;
	// raw result unavailable)".
	require.Equal(t, "verify exploded", resultMsg.Payload["error"])
	require.Equal(t, "error: verify exploded", mailboxResultSummary(resultMsg.Payload))
}

// TestDispatchWorkRecoversPanicAndMarksDead proves the panic guard: a Team
// that panics is contained by launchAsyncDispatch's recover(), the message is
// marked dead with the panic reason, the Team's execution slot is released, and
// the process survives.
func TestDispatchWorkRecoversPanicAndMarksDead(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &panickingTeamRuntime{}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	runtime.launchAsyncDispatch(session, turn.ID, "verify", msg)
	waitForDispatchersDone(t, runtime, session.ID)

	require.Equal(t, int64(1), atomic.LoadInt64(&fake.runs))

	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	work := box[msg.MessageID]
	require.Equal(t, types.MailboxDead, work.Status, "a panicked round must be marked dead")
	reason, _ := work.Payload["dead_reason"].(string)
	require.Contains(t, reason, "panic")
	require.Contains(t, reason, "verify panicked")

	// The slot was released in the deferred cleanup, otherwise every later
	// dispatch to this Team would be silently skipped forever.
	require.False(t, runtime.teamBusy(session.ID, "verify"))
}

// TestLaunchAsyncDispatchDefersWhenTeamBusy covers the tryMarkTeamBusy guard:
// a second dispatch for a Team that is already running is dropped, the message
// stays pending for the draining path, and the running Team is not disturbed.
func TestLaunchAsyncDispatchDefersWhenTeamBusy(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &failingTeamRuntime{}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	// Hold the Team's slot, then dispatch: the launch must be dropped.
	require.True(t, runtime.tryMarkTeamBusy(session.ID, "verify"))
	runtime.launchAsyncDispatch(session, turn.ID, "verify", msg)

	require.Equal(t, 0, fake.verifyRuns(), "a busy team must not be run again")
	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, types.MailboxPending, box[msg.MessageID].Status,
		"a deferred message must stay pending")
	require.True(t, runtime.teamBusy(session.ID, "verify"), "the running team keeps its slot")

	runtime.clearTeamBusy(session.ID, "verify")
}

// TestDispatchWorkFillsMissingRoutingFields covers the two defaulting branches
// of dispatchWork: a message that carries neither its target Team nor its
// cause turn gets both filled in from the dispatch arguments before the Team
// runs, so the events it writes are addressable.
func TestDispatchWorkFillsMissingRoutingFields(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &failingTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	causeTurnID := newID("ft")

	// A bare message: no target Team, no cause turn, exactly what a caller
	// that only knows "run this now" would hand over.
	msg := types.MailboxMessage{
		MessageID: newID("mb"),
		Kind:      mailboxKindWork,
		Payload:   map[string]any{"caller_team": "default"},
	}
	runtime.dispatchWork(ctx, session, causeTurnID, "verify", msg)

	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	stored := box[msg.MessageID]
	require.Equal(t, "verify", stored.To.TeamID)
	require.Equal(t, causeTurnID, stored.CauseFlowTurnID)
}

// TestWakeIfIdleOnlyWakesIdleSessions pins every branch of the wake condition,
// including the two that return silently: a running session and a session in a
// terminal state must not be woken.
func TestWakeIfIdleOnlyWakesIdleSessions(t *testing.T) {
	cases := []struct {
		status types.SessionStatus
		wake   bool
	}{
		{types.SessionCreated, true},
		{types.SessionWaitingInput, true},
		{types.SessionWaitingTool, true},
		{types.SessionWaitingApproval, true},
		{types.SessionRunning, false},
		{types.SessionCompleted, false},
		{types.SessionFailed, false},
		{types.SessionCancelled, false},
		{types.SessionInterrupted, false},
	}

	for _, tc := range cases {
		t.Run(string(tc.status), func(t *testing.T) {
			fileStore := storage.NewFileStore(t.TempDir())
			sessionWriter := storage.NewJSONLSessionWriter(fileStore)
			runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

			var mu sync.Mutex
			var wakes []string
			runtime.SetWaker(func(sessionID string) {
				mu.Lock()
				wakes = append(wakes, sessionID)
				mu.Unlock()
			})

			ctx := context.Background()
			session := seedMailboxSession(t, runtime, newID("fs"), tc.status)

			runtime.wakeIfIdle(ctx, session.ID)

			mu.Lock()
			defer mu.Unlock()
			if tc.wake {
				require.Equal(t, []string{session.ID}, wakes)
			} else {
				require.Empty(t, wakes, "status %q must not be woken", tc.status)
			}
		})
	}
}

// TestWakeIfIdleWithoutWakerIsNoop covers the no-waker branch: nothing is
// configured to wake the session, so the call is a silent no-op rather than a
// crash.
func TestWakeIfIdleWithoutWakerIsNoop(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	require.NotPanics(t, func() {
		runtime.wakeIfIdle(context.Background(), session.ID)
	})
}

// TestWakeIfIdleSkipsUnknownSession covers the conservative branch: a session
// that cannot be loaded is not proven idle, so it is not woken.
func TestWakeIfIdleSkipsUnknownSession(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	var woken atomic.Int64
	runtime.SetWaker(func(string) { woken.Add(1) })

	require.NotPanics(t, func() {
		runtime.wakeIfIdle(context.Background(), "fs_does_not_exist")
	})
	require.Zero(t, woken.Load())
}

// TestRecordBatchDispatchedDoesNotEscalateWriteFailure pins the contract of
// the end-of-turn bookkeeping event: a failed write is logged and swallowed.
// It has no error return by design, so the only observable behaviour is that
// the call completes and still attempted exactly one append.
func TestRecordBatchDispatchedDoesNotEscalateWriteFailure(t *testing.T) {
	writer := &failingSessionWriter{}
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, nil)

	ctx := context.Background()
	session := types.FlowSession{ID: newID("fs"), FlowID: "code-fix", Status: types.SessionWaitingInput}
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	require.NotPanics(t, func() {
		runtime.recordBatchDispatched(ctx, session, turn, []string{"mb_1", "mb_2"})
	})
	require.Equal(t, 1, writer.appendCount(), "the write is attempted once and its failure swallowed")

	// Nothing to record means no write at all.
	runtime.recordBatchDispatched(ctx, session, turn, nil)
	require.Equal(t, 1, writer.appendCount())
}

// TestRecordBatchDispatchedWritesMessageIDs is the positive counterpart: with
// a working writer the event carries exactly the dispatched message IDs.
func TestRecordBatchDispatchedWritesMessageIDs(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	runtime.recordBatchDispatched(ctx, session, turn, []string{"mb_1", "mb_2"})

	replay, err := sessionWriter.Replay(ctx, session.ID)
	require.NoError(t, err)
	found := 0
	for _, event := range replay.Events {
		if event.Type != types.EventMailboxBatchDispatched {
			continue
		}
		found++
		ids, ok := event.Payload["message_ids"].([]any)
		require.True(t, ok)
		require.Len(t, ids, 2)
		assert.Equal(t, "mb_1", ids[0])
		assert.Equal(t, "mb_2", ids[1])
	}
	require.Equal(t, 1, found)
}

// TestDrainTeamInboxLaunchesPendingMessage covers the drain path that runs a
// message left pending while its Team was busy: clearing the slot and draining
// picks it up, and a second drain with nothing pending is a no-op.
func TestDrainTeamInboxLaunchesPendingMessage(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	fake := &failingTeamRuntime{}
	runtime := newTestRuntime(testDefinitions(), fake, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}

	msg, err := runtime.enqueueWorkMessage(ctx, session, turn,
		types.MailboxAddress{TeamID: "verify"},
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	// The Team is mid-round: the drain must leave the message pending. Holding
	// the slot explicitly is what makes this deterministic — without it the
	// first drain launches the message and the assertion races the dispatcher
	// goroutine, which may already have run the Team.
	require.True(t, runtime.tryMarkTeamBusy(session.ID, "verify"))
	runtime.drainTeamInbox(ctx, session, "verify")
	require.Equal(t, 0, fake.verifyRuns())
	runtime.clearTeamBusy(session.ID, "verify")

	// With the slot free, the drain launches it.
	runtime.drainTeamInbox(ctx, session, "verify")
	waitForDispatchersDone(t, runtime, session.ID)
	require.Equal(t, 1, fake.verifyRuns())

	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, types.MailboxDead, box[msg.MessageID].Status)
}
