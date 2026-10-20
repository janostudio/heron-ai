package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// failOnceRuntime fails the first team turn and proceeds afterwards, so one
// runtime can prove both "this turn failed" and "the next turn still runs".
type failOnceRuntime struct{ runs int }

func (f *failOnceRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	f.runs++
	if f.runs == 1 {
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "first turn fails"},
		}, nil
	}
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

type proceedRuntime struct{}

func (proceedRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

// A failed turn is a turn outcome, not a conversation outcome: the session must
// stay continuable, otherwise one model hiccup ends the whole conversation and
// every later message is rejected with "session is already failed".
func TestFailedTurnLeavesSessionContinuable(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &failOnceRuntime{}, storage.NewJSONLSessionWriter(files), nil)

	ctx := context.Background()
	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "code-fix", Input: "boom"})
	require.Error(t, err, "the turn itself must report failure")
	require.Equal(t, types.TurnFailed, first.Turn.Status)
	require.Equal(t, types.SessionWaitingInput, first.Session.Status, "the session must stay continuable")

	second, err := runtime.HandleInputWithContext(ctx, first.Session.ID, "retry", nil)
	require.NoError(t, err, "a failed turn must not block the next one")
	require.Equal(t, first.Session.ID, second.Session.ID)
	require.NotEqual(t, first.Turn.ID, second.Turn.ID)
	require.Equal(t, types.TurnWaitingInput, second.Turn.Status)
	require.Equal(t, types.SessionWaitingInput, second.Session.Status)
}

// Cancel stops the running turn; it must not strand the conversation either.
func TestCancelledSessionAcceptsNewTurn(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), proceedRuntime{}, storage.NewJSONLSessionWriter(files), nil)

	ctx := context.Background()
	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "code-fix", Input: "hello"})
	require.NoError(t, err)

	require.NoError(t, runtime.Cancel(ctx, first.Session.ID))
	cancelled, err := runtime.loadSession(ctx, first.Session.ID)
	require.NoError(t, err)
	require.Equal(t, types.SessionCancelled, cancelled.Status)

	second, err := runtime.HandleInputWithContext(ctx, first.Session.ID, "continue", nil)
	require.NoError(t, err, "a cancelled session must accept the next turn")
	require.Equal(t, types.SessionWaitingInput, second.Session.Status)
}

// A failed turn may leave an async Team running. Continuation must wait for it
// instead of stacking a second turn on top of results still being produced.
func TestRunningTeamBlocksNewTurnUntilItDrains(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), proceedRuntime{}, storage.NewJSONLSessionWriter(files), nil)

	ctx := context.Background()
	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "code-fix", Input: "hello"})
	require.NoError(t, err)

	// A Team that started and never finished, still present in the busy set.
	require.NoError(t, runtime.appendSessionEvent(ctx, first.Session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamTurnStarted,
			FlowSessionID: first.Session.ID,
			TeamTurnID:    "tt_slow",
			TeamID:        "slow-team",
		},
		Payload: map[string]any{},
	}))
	runtime.dispatchMu.Lock()
	runtime.busyTeams["slow-team"] = true
	runtime.dispatchMu.Unlock()

	_, err = runtime.HandleInputWithContext(ctx, first.Session.ID, "next", nil)
	require.Error(t, err, "a running Team must block the next turn")
	require.Contains(t, err.Error(), "still has running teams")

	// Once it drains, the conversation continues normally.
	runtime.dispatchMu.Lock()
	delete(runtime.busyTeams, "slow-team")
	runtime.dispatchMu.Unlock()
	require.NoError(t, runtime.appendSessionEvent(ctx, first.Session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamTurnCompleted,
			FlowSessionID: first.Session.ID,
			TeamTurnID:    "tt_slow",
			TeamID:        "slow-team",
		},
		Payload: map[string]any{},
	}))

	second, err := runtime.HandleInputWithContext(ctx, first.Session.ID, "next", nil)
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, second.Session.Status)
}

// A session recorded as failed by an older build must still be usable after the
// upgrade — that is the only way an already-broken conversation gets rescued.
func TestLegacyFailedSessionAcceptsNewTurn(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), proceedRuntime{}, storage.NewJSONLSessionWriter(files), nil)

	ctx := context.Background()
	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "code-fix", Input: "hello"})
	require.NoError(t, err)

	legacy := first.Session
	legacy.Status = types.SessionFailed
	legacy.UpdatedAt = legacy.UpdatedAt.Add(1)
	require.NoError(t, runtime.appendSessionEvent(ctx, legacy.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: legacy.ID,
		},
		Payload: map[string]any{"session": legacy},
	}))

	second, err := runtime.HandleInputWithContext(ctx, legacy.ID, "continue", nil)
	require.NoError(t, err, "a session marked failed by an older build must still accept a turn")
	require.Equal(t, types.SessionWaitingInput, second.Session.Status)
}
