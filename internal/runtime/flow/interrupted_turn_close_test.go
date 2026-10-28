package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// A turn that was running when the process died must get a terminal event
// before the next turn starts.
//
// Replaying 128 real sessions found 13 FlowTurns (and 22 TeamTurns) with a
// start and no end, all on the tail of a session. "Still running" forever is
// not a state a consumer can act on: it can only guess, and guessing is what
// produced the missing/duplicated output this ledger tracks.
func TestCloseInterruptedTurnsClosesDanglingFlowTurn(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	rt := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-interrupted", types.SessionRunning)

	// A turn that started and never ended: the state a killed process leaves.
	require.NoError(t, rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowTurnStarted,
			FlowSessionID: session.ID,
			FlowTurnID:    "ft-dangling",
		},
		Payload: map[string]any{
			"turn": types.FlowTurn{
				ID:            "ft-dangling",
				FlowSessionID: session.ID,
				Input:         "查一下版本号",
				Status:        types.TurnRunning,
				StartedAt:     time.Now().UTC().Add(-time.Minute),
			},
		},
	}))

	rt.closeInterruptedTurns(context.Background(), session.ID)

	replay, err := rt.sessions.Replay(context.Background(), session.ID)
	require.NoError(t, err)
	var terminals []map[string]any
	for _, event := range replay.Events {
		if event.Type != types.EventFlowTurnCompleted {
			continue
		}
		if event.FlowTurnID != "ft-dangling" {
			continue
		}
		// Replay decodes payloads from JSON, so the turn arrives as a map.
		turn, ok := event.Payload["turn"].(map[string]any)
		require.True(t, ok, "终态事件必须带完整的 turn 记录")
		terminals = append(terminals, turn)
	}
	require.Len(t, terminals, 1, "悬空回合必须被收口，且只收一次")
	require.Equal(t, string(types.TurnCancelled), terminals[0]["status"])
	require.NotEmpty(t, terminals[0]["finished_at"])

	// A second pass must not write a second terminal.
	rt.closeInterruptedTurns(context.Background(), session.ID)
	replay, err = rt.sessions.Replay(context.Background(), session.ID)
	require.NoError(t, err)
	count := 0
	for _, event := range replay.Events {
		if event.Type == types.EventFlowTurnCompleted && event.FlowTurnID == "ft-dangling" {
			count++
		}
	}
	require.Equal(t, 1, count, "收口是幂等的")
}

// A turn that ended waiting for the user is resumable by design: it must not be
// mistaken for an interrupted one.
func TestCloseInterruptedTurnsLeavesResumableTurnAlone(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	rt := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-resumable", types.SessionWaitingInput)

	require.NoError(t, rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowTurnStarted, FlowSessionID: session.ID, FlowTurnID: "ft-waiting"},
		Payload:     map[string]any{"turn": types.FlowTurn{ID: "ft-waiting", FlowSessionID: session.ID, Status: types.TurnRunning}},
	}))
	require.NoError(t, rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowTurnWaitingInput, FlowSessionID: session.ID, FlowTurnID: "ft-waiting"},
		Payload:     map[string]any{"turn": types.FlowTurn{ID: "ft-waiting", FlowSessionID: session.ID, Status: types.TurnWaitingInput}},
	}))

	rt.closeInterruptedTurns(context.Background(), session.ID)

	replay, err := rt.sessions.Replay(context.Background(), session.ID)
	require.NoError(t, err)
	for _, event := range replay.Events {
		require.NotEqual(t, types.EventFlowTurnCompleted, event.Type,
			"等待用户的回合是可恢复的，不能被当成中断收口")
	}
}
