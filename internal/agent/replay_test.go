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

func TestReplayAgentContextFeedbackWithRawText(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	// A structured-output retry feedback carries the model's raw (invalid)
	// output. Replay must restore it as an assistant message so the rebuilt
	// context matches the in-memory context the retry used.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentFeedback, CallID: "call-1"},
		Payload: map[string]any{
			"content":  "The previous response was not valid JSON...",
			"raw_text": `{"partial": "output"`,
		},
	})

	initial := []types.Message{{Role: "user", Content: "hello"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	require.Len(t, got, 3)
	assert.Equal(t, "assistant", got[1].Role)
	assert.Equal(t, `{"partial": "output"`, got[1].Content)
	assert.Equal(t, "user", got[2].Role)
	assert.Equal(t, "The previous response was not valid JSON...", got[2].Content)
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

// A failed tool call must be restored as a tool message, so an assistant
// tool_call is never left without a matching result — the shape a strict
// provider rejects with "tool calls and tool results do not match"
// (ISSUE-replay-missing-tool-call-failed).
func TestReplayAgentContextRestoresFailedToolCall(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload: map[string]any{"text": "", "tool_calls": []map[string]any{
			{"id": "tc-1", "name": "Bash", "arguments": map[string]any{"command": "exit 1"}},
		}},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallFailed, CallID: "call-1"},
		Payload:     map[string]any{"tool_name": "Bash", "tool_call_id": "tc-1", "error": "command exited with code 1"},
	})

	initial := []types.Message{{Role: "user", Content: "hi"}}
	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), initial)

	require.Len(t, got, 3)
	assert.Equal(t, "assistant", got[1].Role)
	require.Len(t, got[1].ToolCalls, 1)
	assert.Equal(t, "tc-1", got[1].ToolCalls[0].ID)
	assert.Equal(t, "tool", got[2].Role)
	assert.Equal(t, "tc-1", got[2].ToolCallID, "the tool result must answer the call by id")
	assert.Equal(t, "command exited with code 1", got[2].Content, "error falls back to content")
}

// A completed tool call restores its tool_call_id too.
func TestReplayAgentContextRestoresCompletedToolCallID(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallCompleted, CallID: "call-1"},
		Payload:     map[string]any{"tool_call_id": "tc-9", "content": "ok"},
	})

	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), nil)
	require.Len(t, got, 1)
	assert.Equal(t, "ok", got[0].Content)
	assert.Equal(t, "tc-9", got[0].ToolCallID)
}

// A tool result whose event predates the tool_call_id field must still be
// paired to its call, by order, so replayed requests do not reach the provider
// with unpaired tool messages.
func TestReplayAgentContextPairsToolCallIDByOrder(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1"},
		Payload: map[string]any{"text": "", "tool_calls": []map[string]any{
			{"id": "call_a", "name": "Read", "arguments": map[string]any{}},
			{"id": "call_b", "name": "Grep", "arguments": map[string]any{}},
		}},
	})
	// Old-format events: no tool_call_id.
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallCompleted, CallID: "call-1"},
		Payload:     map[string]any{"tool_name": "Read", "content": "a"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventToolCallFailed, CallID: "call-1"},
		Payload:     map[string]any{"tool_name": "Grep", "error": "boom"},
	})

	got := loop.replayAgentContext(context.Background(), reqFor("call-1"), nil)
	require.Len(t, got, 3)
	assert.Equal(t, "tool", got[1].Role)
	assert.Equal(t, "call_a", got[1].ToolCallID, "first result pairs to the first call")
	assert.Equal(t, "a", got[1].Content)
	assert.Equal(t, "tool", got[2].Role)
	assert.Equal(t, "call_b", got[2].ToolCallID, "second result pairs to the second call")
	assert.Equal(t, "boom", got[2].Content, "failed call's error becomes content")
}

// 跨回合重放会让模型看到自己上一轮的结论，于是把新问题当旧任务继续答
// （s13：问 cls 日志，回了上一轮 genie-agent 的主分支）。同一 call 的历史
// 必须按 flow turn 隔离，只重放本回合的增量。
func TestReplayAgentContextStopsAtTurnBoundary(t *testing.T) {
	w := &fakeSessionWriter{}
	loop := newReplayTurnLoop(w)

	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1", FlowTurnID: "ft-old"},
		Payload:     map[string]any{"text": "上一轮的结论：genie-agent 主分支是 main"},
	})
	_, _ = w.Append(context.Background(), "fs-1", storage.LayerAgent, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventAgentModelResponse, CallID: "call-1", FlowTurnID: "ft-new"},
		Payload:     map[string]any{"text": "本回合的中间输出"},
	})

	req := reqFor("call-1")
	req.FlowTurnID = "ft-new"
	initial := []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "查看cls线上日志"}}

	got := loop.replayAgentContext(context.Background(), req, initial)

	require.Len(t, got, 3, "只应重放本回合的事件")
	assert.Equal(t, "本回合的中间输出", got[2].Content)
	for _, msg := range got {
		assert.NotContains(t, msg.Content, "genie-agent")
	}
}

// schema 里没有 reply 字段时，原始 JSON 曾直接成为 Reply 并进入对话。
func TestStructuredSummaryFlattensToReadableLine(t *testing.T) {
	parsed := map[string]any{
		"status":         "done",
		"repo":           "genie-agent",
		"primary_branch": "main",
		"exists":         true,
	}
	got := structuredSummary(parsed)
	require.Contains(t, got, "repo=genie-agent")
	require.Contains(t, got, "primary_branch=main")
	require.NotContains(t, got, "{")
}

func TestStructuredSummaryPrefersNamedFields(t *testing.T) {
	require.Equal(t, "主分支是 main", structuredSummary(map[string]any{
		"summary": "主分支是 main", "repo": "genie-agent",
	}))
	require.Equal(t, "", structuredSummary("not a map"))
}

// 数组元素常常是 {question, answer} 对，%v 会把 Go 的 map[...] 语法漏进回复。
func TestStructuredSummaryRendersArrayItemsReadably(t *testing.T) {
	got := structuredSummary(map[string]any{
		"repo": "genie-agent",
		"checks": []any{
			map[string]any{"question": "主分支", "answer": "main"},
			map[string]any{"question": "当前分支", "answer": "main"},
		},
	})
	require.Contains(t, got, "checks=主分支→main; 当前分支→main")
	require.NotContains(t, got, "map[")
}
