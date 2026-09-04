package agent

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newReplayTurnLoop(w storage.SessionWriter) *TurnLoop {
	loop := NewTurnLoop(nil, nil, nil, nil, nil, nil, nil)
	loop.SetSessionWriter(w)
	return loop
}

func reqFor(callID string) types.AgentRequest {
	return types.AgentRequest{
		FlowSessionID: "fs-1",
		TeamID:        "team-1",
		CallID:        callID,
	}
}

func TestReplayAgentContextNoWriter(t *testing.T) {
	loop := newReplayTurnLoop(nil)
	initial := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello"}}

	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)
	assert.Equal(t, initial, got)
}

func TestReplayAgentContextNoEvents(t *testing.T) {
	loop := newReplayTurnLoop(&fakeSessionWriter{})
	initial := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello"}}

	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)
	assert.Equal(t, initial, got)
}

func TestReplayAgentContextAppendsIncrements(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	// Simulate a prior turn: assistant asks tool, tool returns, assistant answers.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "let me check", "tool_calls": []types.ToolCall{{Name: "Read", ID: "t1"}}},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallCompleted, CallID: "call-1"},
		Payload:     map[string]any{"tool_name": "Read", "content": "file content"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "done"},
	})

	initial := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	require.Len(t, got, 5)
	assert.Equal(t, "sys", got[0].Content)
	assert.Equal(t, "hello", got[1].Content)
	assert.Equal(t, "assistant", got[2].Role)
	assert.Equal(t, "let me check", got[2].Content)
	assert.Equal(t, "tool", got[3].Role)
	assert.Equal(t, "file content", got[3].Content)
	assert.Equal(t, "assistant", got[4].Role)
	assert.Equal(t, "done", got[4].Content)
}

func TestReplayAgentContextFiltersByCallID(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	// Event for a different call must not be replayed.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "other-call"},
		Payload:     map[string]any{"text": "wrong"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "right"},
	})

	initial := []types.Message{{Role: "user", Content: "hello"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	require.Len(t, got, 2)
	assert.Equal(t, "right", got[1].Content)
}

func TestReplayAgentContextFoldsCompacted(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	// Two assistant/tool groups, then a compaction that drops the first group.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "first", "tool_calls": []types.ToolCall{{Name: "Read", ID: "t1"}}},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallCompleted, CallID: "call-1"},
		Payload:     map[string]any{"tool_name": "Read", "content": "first tool"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "second"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventContextCompacted, CallID: "call-1"},
		Payload:     map[string]any{"summary": "compacted summary", "dropped_count": float64(1)},
	})

	initial := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hello"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	// initial(2) + group1(2) + group2(1) = 5 messages; compaction drops group1(2),
	// replacing with summary(1). Result: system + user + summary + second.
	require.Len(t, got, 4)
	assert.Equal(t, "sys", got[0].Content)
	assert.Equal(t, "hello", got[1].Content)
	assert.Equal(t, "user", got[2].Role)
	assert.Contains(t, got[2].Content, "compacted summary")
	assert.Equal(t, "assistant", got[3].Role)
	assert.Equal(t, "second", got[3].Content)
}

func TestReplayAgentContextFeedback(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentFeedback, CallID: "call-1"},
		Payload:     map[string]any{"content": "## Completion Feedback\nredo"},
	})

	initial := []types.Message{{Role: "user", Content: "hello"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	require.Len(t, got, 2)
	assert.Equal(t, "user", got[1].Role)
	assert.Equal(t, "## Completion Feedback\nredo", got[1].Content)
}
