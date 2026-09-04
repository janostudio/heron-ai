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

func TestToolResultEventFieldsExposesMetadata(t *testing.T) {
	result := &types.ToolResult{
		Content: "command output",
		Metadata: map[string]any{
			"exit_code": 0,
			"stdout":    "out",
			"stderr":    "err",
			"truncated": false,
		},
	}

	fields := toolResultEventFields(result)

	// content stays plain text (no flattened Metadata string).
	assert.Equal(t, "command output", fields["content"])
	// Metadata keys are promoted to top-level fields.
	assert.Equal(t, 0, fields["exit_code"])
	assert.Equal(t, "out", fields["stdout"])
	assert.Equal(t, "err", fields["stderr"])
	assert.Equal(t, false, fields["truncated"])
}

func TestToolResultEventFieldsEmptyMetadata(t *testing.T) {
	result := &types.ToolResult{Content: "plain"}

	fields := toolResultEventFields(result)

	assert.Equal(t, "plain", fields["content"])
	assert.Len(t, fields, 1) // only content, no metadata keys
}

// capturingModelProvider records the messages each Chat call receives so tests
// can assert that replay restored prior context.
type capturingModelProvider struct {
	captured [][]types.Message
}

func (m *capturingModelProvider) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.captured = append(m.captured, cloneMessages(messages))
	return &types.ChatResponse{Text: "answer", Usage: types.TokenUsage{TotalTokens: 1}}, nil
}

func (m *capturingModelProvider) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}

// TestRunReplaysPriorContext verifies the end-to-end path: when a session has
// prior agent.jsonl events for the same call, Run replays them into the model
// context instead of starting from a blank transcript.
func TestRunReplaysPriorContext(t *testing.T) {
	w := &fakeSessionWriter{}

	// Simulate a prior turn: one assistant model response recorded in agent.jsonl.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload:     map[string]any{"text": "prior answer"},
	})

	model := &capturingModelProvider{}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	loop.SetSessionWriter(w)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "test-agent",
		Loop: types.LoopConfig{MaxRounds: 1},
	}, types.AgentRequest{
		FlowSessionID: "fs-1",
		CallID:        "call-1",
		AgentID:       "assistant",
	})
	require.NoError(t, err)
	require.NotEmpty(t, model.captured, "expected at least one Chat call")

	messages := model.captured[0]
	// The replay should have appended the prior assistant message after the
	// freshly rendered prompt.
	found := false
	for _, msg := range messages {
		if msg.Role == "assistant" && msg.Content == "prior answer" {
			found = true
		}
	}
	assert.True(t, found, "prior assistant message should be replayed into context, got: %+v", messages)
}

// toolCallModel returns one assistant message with a tool call, then a final
// answer, so the Run loop exercises tool execution.
type toolCallModel struct {
	calls int
}

func (m *toolCallModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.calls++
	if m.calls == 1 {
		return &types.ChatResponse{
			Text:      "calling tool",
			ToolCalls: []types.ToolCall{{ID: "t1", Name: "Bash", Arguments: map[string]any{"command": "ls"}}},
			Usage:     types.TokenUsage{TotalTokens: 1},
		}, nil
	}
	return &types.ChatResponse{Text: "done", Usage: types.TokenUsage{TotalTokens: 1}}, nil
}

func (m *toolCallModel) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}

// metadataToolExecutor returns a tool result with Metadata so tests can assert
// tool_call.completed events expose metadata as top-level fields.
type metadataToolExecutor struct{}

func (m *metadataToolExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: true,
		Content: "ls output",
		Metadata: map[string]any{
			"exit_code": 0,
			"stdout":    "ls output",
			"stderr":    "",
		},
	}, nil
}

// TestToolCallEventExposesMetadataEndToEnd verifies the emitted
// tool_call.completed event carries metadata (exit_code etc.) as top-level
// fields rather than flattened into the content string.
func TestToolCallEventExposesMetadataEndToEnd(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := NewTurnLoop(
		&toolCallModel{},
		&metadataToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "run ls"}}},
	)
	loop.SetSessionWriter(w)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		FlowSessionID: "fs-1",
		CallID:        "call-1",
		AgentID:       "assistant",
	})
	require.NoError(t, err)

	var completed *storage.SessionEvent
	for _, event := range w.recorded() {
		if event.Type == types.EventToolCallCompleted {
			completed = &event
		}
	}
	require.NotNil(t, completed, "expected a tool_call.completed event")
	assert.Equal(t, 0, completed.Payload["exit_code"])
	assert.Equal(t, "ls output", completed.Payload["stdout"])
	assert.Equal(t, "ls output", completed.Payload["content"])
}
