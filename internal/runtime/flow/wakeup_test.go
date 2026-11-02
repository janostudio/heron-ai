package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// nosilentRuntime disables the "user spoke recently" window, which every test
// below would otherwise trip: each one starts a conversation and then wakes it
// within the same second.
func newWakeupRuntime(t *testing.T) (*Runtime, storage.SessionWriter) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	rt := newTestRuntime(testDefinitions(), proceedRuntime{}, writer, storage.NewJSONLEvidenceStore(files))
	rt.wakeupMu.Lock()
	rt.wakeupCfg.QuietAfterUserTurn = 0
	rt.wakeupMu.Unlock()
	return rt, writer
}

func startSession(t *testing.T, rt *Runtime) types.FlowTurnResult {
	t.Helper()
	result, err := rt.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "hand the build off and check on it later",
	})
	require.NoError(t, err)
	return result
}

func sessionEvents(t *testing.T, writer storage.SessionWriter, sessionID string) []storage.SessionEvent {
	t.Helper()
	replay, err := writer.Replay(context.Background(), sessionID)
	require.NoError(t, err)
	return replay.Events
}

func countEvents(events []storage.SessionEvent, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// A registered wake-up must come back as a turn of its own, starting at the
// Team that registered it, and it must never look like the user said anything.
func TestWakeupFiresAsItsOwnTurnFromTheSchedulingTeam(t *testing.T) {
	rt, writer := newWakeupRuntime(t)
	started := startSession(t, rt)

	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(2 * time.Minute),
		Note:          "check whether the build is still on track; if it failed, read the log",
	})
	require.NoError(t, err)
	require.Equal(t, types.WakeupScheduled, record.Status)
	require.Equal(t, "default", record.TeamID)

	events := sessionEvents(t, writer, started.Session.ID)
	require.Equal(t, 1, countEvents(events, types.EventWakeupCreated))
	require.Greater(t, record.LastSeq, int64(0), "the record must remember where the session was")

	fired, err := rt.ResumeForWakeup(context.Background(), started.Session.ID, record)
	require.NoError(t, err)
	require.Equal(t, turnOriginWakeup, fired.Turn.Origin,
		"a wake-up is its own kind of turn, not a user turn")
	require.False(t, fired.Turn.Intermediate && fired.Reply != "",
		"an intermediate turn must not also carry a reply")

	var kinds []string
	var noteBlock *types.ContextBlock
	for i := range fired.Turn.ContextBlocks {
		kinds = append(kinds, fired.Turn.ContextBlocks[i].Kind)
		if fired.Turn.ContextBlocks[i].Kind == "wakeup_note" {
			noteBlock = &fired.Turn.ContextBlocks[i]
		}
	}
	require.Contains(t, kinds, "wakeup_note")
	require.NotNil(t, noteBlock)
	require.Equal(t, "system", noteBlock.Placement,
		"the note is the model's own message to itself, so it must not sit in the user position")
	require.Contains(t, noteBlock.Text, "check whether the build is still on track")

	events = sessionEvents(t, writer, started.Session.ID)
	require.Equal(t, 1, countEvents(events, types.EventWakeupFired))
}

// Cancelling before the deadline means the turn never happens.
func TestCancelledWakeupDoesNotFire(t *testing.T) {
	rt, writer := newWakeupRuntime(t)
	started := startSession(t, rt)

	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          "check the deploy",
	})
	require.NoError(t, err)
	require.NoError(t, rt.CancelWakeup(context.Background(), started.Session.ID, record.ID))

	require.Empty(t, rt.pendingWakeups(), "a cancelled wake-up must not stay pending")
	events := sessionEvents(t, writer, started.Session.ID)
	require.Equal(t, 1, countEvents(events, types.EventWakeupCancelled))
}

// A cancelled or fired wake-up cannot be cancelled twice into an error: the
// model's intent is "this no longer matters", and a race must not make it retry.
func TestCancellingSettledWakeupIsNoOp(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          "check the deploy",
	})
	require.NoError(t, err)
	require.NoError(t, rt.CancelWakeup(context.Background(), started.Session.ID, record.ID))
	require.NoError(t, rt.CancelWakeup(context.Background(), started.Session.ID, record.ID))
}

// The timer is rebuilt from the event stream, so a wake-up survives a restart.
func TestWakeupSurvivesRestart(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	first := newTestRuntimeWithStore(types.NewDefinitionStore(testDefinitions(), "", ""), proceedRuntime{}, writer, storage.NewJSONLEvidenceStore(files))
	first.wakeupMu.Lock()
	first.wakeupCfg.QuietAfterUserTurn = 0
	first.wakeupMu.Unlock()

	started := startSession(t, first)
	record, err := first.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          "still here after a restart",
	})
	require.NoError(t, err)

	// A new Runtime over the same files stands in for a process restart.
	second := newTestRuntimeWithStore(types.NewDefinitionStore(testDefinitions(), "", ""), proceedRuntime{}, storage.NewJSONLSessionWriter(files), storage.NewJSONLEvidenceStore(files))
	second.SetSessionLister(func(context.Context) ([]string, error) {
		return []string{started.Session.ID}, nil
	})
	second.RecoverWakeups(context.Background())

	pending := second.pendingWakeups()
	require.Len(t, pending, 1, "the scheduled wake-up must be rebuilt from the event log")
	require.Equal(t, record.ID, pending[0].ID)
	require.Equal(t, record.Note, pending[0].Note)
}

// The whole point of the feature: a wake-up with nothing to look at is useless,
// so the facts block must carry what actually happened downstream.
func TestWakeupFactsDescribeDownstreamActivity(t *testing.T) {
	rt, writer := newWakeupRuntime(t)
	started := startSession(t, rt)
	sessionID := started.Session.ID

	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: sessionID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          "check the build",
	})
	require.NoError(t, err)

	now := time.Now().UTC()
	// A downstream Team finishes one tool call, fails another, and publishes a
	// record — exactly the three things a reader needs to judge progress.
	require.NoError(t, appendWakeupFixture(rt, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type: types.EventToolCallCompleted, FlowSessionID: sessionID, TeamID: "default", CreatedAt: now,
		},
		Payload: map[string]any{"tool_name": "Bash", "arguments": map[string]any{"command": "go test ./..."}},
	}))
	require.NoError(t, appendWakeupFixture(rt, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type: types.EventToolCallFailed, FlowSessionID: sessionID, TeamID: "default", CreatedAt: now,
		},
		Payload: map[string]any{"tool_name": "Edit", "error": "permission denied"},
	}))
	require.NoError(t, appendWakeupFixture(rt, sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type: types.EventSharedRecordPublished, FlowSessionID: sessionID, TeamID: "default", CreatedAt: now,
		},
		Payload: map[string]any{"record": types.SharedRecord{RecordID: "rec_1", Name: "build result"}},
	}))

	facts := rt.buildWakeupFacts(context.Background(), sessionID, record, time.Now().UTC())
	require.Contains(t, facts, "Bash", "a recent tool call must be visible")
	require.Contains(t, facts, "go test", "the tool call's argument is what makes it recognisable")
	require.Contains(t, facts, "failed", "a failure must be visible")
	require.Contains(t, facts, "permission denied", "the failure reason must be visible")
	require.Contains(t, facts, "build result", "published records are the progress evidence")
	require.NotContains(t, facts, "stalled", "facts state durations and counts, never a conclusion")

	// And the facts must actually reach the turn's context.
	_, err = rt.ResumeForWakeup(context.Background(), sessionID, record)
	require.NoError(t, err)
	events := sessionEvents(t, writer, sessionID)
	require.Equal(t, 1, countEvents(events, types.EventWakeupFired))
}

// Quotas are resource limits the model has to be able to see and act on.
func TestWakeupRejectsBadTimingAndOversizedNote(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	_, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Second),
		Note:          "too soon",
	})
	require.Error(t, err, "a delay under the minimum must be rejected")

	long := make([]rune, maxWakeupNoteChars+1)
	for i := range long {
		long[i] = 'x'
	}
	_, err = rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          string(long),
	})
	require.Error(t, err, "an oversized note is rejected, not truncated")
}

// A session parked on a Tool or approval wakes itself when that resolves;
// starting another turn on top of it is what the input path refuses too.
func TestWakeupDeferredWhenSessionIsParked(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(2 * time.Minute),
		Note:          "check later",
	})
	require.NoError(t, err)

	// Park the session the way a pending Tool task would.
	session := started.Session
	session.Status = types.SessionWaitingTool
	require.NoError(t, rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type: types.EventFlowSessionUpdated, FlowSessionID: session.ID,
		},
		Payload: map[string]any{"session": session},
	}))

	_, err = rt.ResumeForWakeup(context.Background(), started.Session.ID, record)
	require.ErrorIs(t, err, errWakeupDeferred,
		"a parked session must defer the wake-up rather than stack a second turn")
	// The event stream records this reason, so it has to name the actual gate.
	// "session was busy" covers five different causes and tells the model
	// nothing it can act on next time it wakes up.
	require.Contains(t, wakeupDeferralReason(err), string(types.SessionWaitingTool))
}

// appendWakeupFixture writes one event through the runtime's own writer so
// sequence numbers stay monotonic.
func appendWakeupFixture(rt *Runtime, sessionID string, event storage.SessionEvent) error {
	return rt.appendSessionEvent(context.Background(), sessionID, event)
}

// The end-to-end shape of the feature: a registration with nobody watching
// turns into a turn of its own when the time comes. Everything else in this
// file tests one piece; this is the piece that says the loop actually runs.
func TestSchedulerFiresDueWakeupWithoutAnyCaller(t *testing.T) {
	rt, writer := newWakeupRuntime(t)
	rt.wakeupMu.Lock()
	rt.wakeupCfg.MinAfter = 10 * time.Millisecond
	rt.wakeupMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rt.RunWakeupScheduler(ctx)

	started := startSession(t, rt)
	_, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(80 * time.Millisecond),
		Note:          "check the build",
	})
	require.NoError(t, err)

	// Wait for the turn itself, not just the fired event: the turn is written
	// a moment later, and asserting on the first half would race the second.
	require.Eventually(t, func() bool {
		for _, event := range sessionEvents(t, writer, started.Session.ID) {
			if event.Type != types.EventFlowTurnStarted {
				continue
			}
			if origin, _ := turnOriginAndStart(event.Payload); origin == turnOriginWakeup {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the fired wake-up must start a flow turn with origin=wakeup")

	// Let the turn finish before the test directory is removed: the fired turn
	// runs in the scheduler's goroutine, and its last write races cleanup.
	require.Eventually(t, func() bool {
		return countEvents(sessionEvents(t, writer, started.Session.ID), types.EventFlowTurnWaitingInput) >= 2
	}, 5*time.Second, 20*time.Millisecond, "the wake-up turn must settle")
}

// A wake-up turn is marked intermediate whenever the Team it is checking on is
// still running — which is exactly when its text matters to the user. The
// clearing rule must therefore not swallow it.
func TestWakeupTurnReplyBecomesDelivery(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	const text = "下游已跑 5 小时，无新增产出，已催一次"
	result := types.FlowTurnResult{
		Session:    started.Session,
		Turn:       types.FlowTurn{ID: "ft_wake", Origin: turnOriginWakeup, Intermediate: true},
		Reply:      text,
		FinalReply: text,
	}
	finished, err := rt.finishTurn(context.Background(), result, types.SessionWaitingInput, nil)
	require.NoError(t, err)
	require.Empty(t, finished.Reply, "an intermediate turn still carries no reply")
	require.Len(t, finished.Deliveries, 1, "but a wake-up's conclusion must survive as a delivery")
	require.Contains(t, finished.Deliveries[0].Text, "5 小时")
}

// The same shape from any other origin keeps the old behaviour: no delivery.
func TestNonWakeupIntermediateTurnProducesNoDelivery(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	result := types.FlowTurnResult{
		Session:    started.Session,
		Turn:       types.FlowTurn{ID: "ft_mailbox", Origin: turnOriginMailbox, Intermediate: true},
		Reply:      "已转给执行侧",
		FinalReply: "已转给执行侧",
	}
	finished, err := rt.finishTurn(context.Background(), result, types.SessionWaitingInput, nil)
	require.NoError(t, err)
	require.Empty(t, finished.Deliveries)
}

// /api/health reports these two numbers; they must match what the scheduler
// will actually fire.
func TestWakeupStatsCountsWhatWillFire(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	started := startSession(t, rt)

	_, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(time.Hour),
		Note:          "check the build",
	})
	require.NoError(t, err)

	cancelled, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(2 * time.Hour),
		Note:          "check the deploy",
	})
	require.NoError(t, err)
	require.NoError(t, rt.CancelWakeup(context.Background(), started.Session.ID, cancelled.ID))

	pending, sessions := rt.WakeupStats(context.Background())
	require.Equal(t, 1, pending, "a cancelled record must not be reported as pending")
	require.Equal(t, 1, sessions)
}

// A deferred wake-up must say which gate held it. The reason is what the model
// reads on its next wake-up, and the quiet window is the one it can act on —
// it explains why "12 seconds later" came back as "five minutes later".
func TestWakeupDeferralNamesTheQuietWindow(t *testing.T) {
	rt, _ := newWakeupRuntime(t)
	rt.wakeupMu.Lock()
	rt.wakeupCfg.QuietAfterUserTurn = time.Hour
	rt.wakeupMu.Unlock()

	started := startSession(t, rt)
	record, err := rt.ScheduleWakeup(context.Background(), types.WakeupRequest{
		FlowSessionID: started.Session.ID,
		TeamID:        "default",
		FireAt:        time.Now().UTC().Add(2 * time.Minute),
		Note:          "check later",
	})
	require.NoError(t, err)

	_, err = rt.ResumeForWakeup(context.Background(), started.Session.ID, record)
	require.ErrorIs(t, err, errWakeupDeferred)
	require.Contains(t, wakeupDeferralReason(err), "quiet window")
}
