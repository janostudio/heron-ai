package main

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestPumpForwardsOtherTurnsWhileFollowing is the P-05 regression.
//
// Before following, a pump shows only the turn the caller asked about. While
// following async work the caller is waiting for a turn it did NOT start: the
// dispatch turn ends, the engine begins the follow-up one, and its status frame
// carries the answer. The per-turn filter threw that frame away, so the user saw
// "已派发…" and never got the result.
func TestPumpForwardsOtherTurnsWhileFollowing(t *testing.T) {
	store := newMemorySessionStore()
	var mu sync.Mutex
	var seen []map[string]any
	pump := startEventPump(context.Background(), store, "fs-1", "ft-1", 0, func(params map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, params)
		return nil
	})

	// Following starts: the filter must stop applying.
	pump.followAll.Store(true)

	answer := eventFor(0, types.EventFlowTurnWaitingInput, map[string]any{
		"turn":  types.FlowTurn{ID: "ft-2", Status: types.TurnWaitingInput, Origin: "tool"},
		"reply": "版本号是 v9.9.9。",
	})
	answer.FlowTurnID = "ft-2"
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent, answer)
	pump.Stop()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1, "跟随期间其它 turn 的事件必须送达")
	require.Equal(t, "版本号是 v9.9.9。", seen[0]["reply"])
}

// TestPumpStillFiltersWithoutFollowing keeps the original contract: a pump that
// is not following shows one turn only.
func TestPumpStillFiltersWithoutFollowing(t *testing.T) {
	store := newMemorySessionStore()
	var mu sync.Mutex
	var seen []map[string]any
	pump := startEventPump(context.Background(), store, "fs-1", "ft-1", 0, func(params map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, params)
		return nil
	})

	other := eventFor(0, types.EventToolCallStarted, map[string]any{"tool_name": "Other", "tool_call_id": "z"})
	other.FlowTurnID = "ft-2"
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent, other)
	pump.Stop()

	mu.Lock()
	defer mu.Unlock()
	require.Empty(t, seen, "未跟随时仍只转发本 turn 的事件")
}
