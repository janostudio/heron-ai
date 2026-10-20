package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func newToolEventFixture(t *testing.T, responses []types.ChatResponse, tools []string) (*TurnLoop, *fakeSessionWriter, *recordingToolExecutor) {
	t.Helper()
	model := &mockModelProvider{responses: responses}
	executor := &recordingToolExecutor{}
	loop := NewTurnLoop(
		model,
		executor,
		NewGuardrailChecker(nil, nil),
		NewRouteParser(),
		NewHITLGate(time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	writer := &fakeSessionWriter{}
	loop.SetSessionWriter(writer)
	t.Cleanup(func() {})
	_ = tools
	return loop, writer, executor
}

func toolEventsOfType(writer *fakeSessionWriter, eventType string) []map[string]any {
	var payloads []map[string]any
	for _, event := range writer.recorded() {
		if event.Type == eventType {
			payloads = append(payloads, event.Payload)
		}
	}
	return payloads
}

// TestTurnLoopToolEventsCarryPairingID guards the connector's pairing key:
// the started event used to omit tool_call_id, leaving the caller unable to
// match a completion to its invocation.
func TestTurnLoopToolEventsCarryPairingID(t *testing.T) {
	loop, writer, _ := newToolEventFixture(t, []types.ChatResponse{
		{
			Text:      "reading",
			ToolCalls: []types.ToolCall{{ID: "tc-9", Name: "Read", Arguments: map[string]any{"file_path": "a.go"}}},
			Usage:     types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}, []string{"Read"})

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{FlowSessionID: "fs-1", CallID: "call-1", CallTurnID: "ct-1", Attempt: 1})
	require.NoError(t, err)

	started := toolEventsOfType(writer, types.EventToolCallStarted)
	require.Len(t, started, 1)
	require.Equal(t, "tc-9", started[0]["tool_call_id"])
	require.Equal(t, "Read", started[0]["tool_name"])

	completed := toolEventsOfType(writer, types.EventToolCallCompleted)
	require.Len(t, completed, 1)
	require.Equal(t, started[0]["tool_call_id"], completed[0]["tool_call_id"])
}

// TestTurnLoopRejectedToolStillEmitsTerminalEvent covers the paths that never
// reach the executor. Without an explicit pair, a caller sees a tool call that
// the model produced and that nothing ever resolved.
func TestTurnLoopRejectedToolStillEmitsTerminalEvent(t *testing.T) {
	loop, writer, executor := newToolEventFixture(t, []types.ChatResponse{
		{
			Text:      "trying",
			ToolCalls: []types.ToolCall{{ID: "tc-bad", Name: "Write", Arguments: map[string]any{"file": "x"}}},
			Usage:     types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}, nil)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{FlowSessionID: "fs-1", CallID: "call-1", CallTurnID: "ct-1", Attempt: 1})
	require.NoError(t, err)
	require.Empty(t, executor.names, "a disallowed tool must not reach the executor")

	started := toolEventsOfType(writer, types.EventToolCallStarted)
	failed := toolEventsOfType(writer, types.EventToolCallFailed)
	require.Len(t, started, 1)
	require.Len(t, failed, 1)
	require.Equal(t, started[0]["tool_call_id"], failed[0]["tool_call_id"])
	require.Equal(t, "Write", failed[0]["tool_name"])
	require.Contains(t, failed[0]["error"], "not allowed")
}

// TestTurnLoopAssignsIDWhenModelOmitsIt keeps the pairing key non-empty even
// when the provider returns a tool call without an id.
func TestTurnLoopAssignsIDWhenModelOmitsIt(t *testing.T) {
	loop, writer, _ := newToolEventFixture(t, []types.ChatResponse{
		{
			Text:      "reading",
			ToolCalls: []types.ToolCall{{Name: "Read", Arguments: map[string]any{"file_path": "a.go"}}},
			Usage:     types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}, nil)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{FlowSessionID: "fs-1", CallID: "call-1", CallTurnID: "ct-1", Attempt: 1})
	require.NoError(t, err)

	started := toolEventsOfType(writer, types.EventToolCallStarted)
	completed := toolEventsOfType(writer, types.EventToolCallCompleted)
	require.Len(t, started, 1)
	require.Len(t, completed, 1)
	id, _ := started[0]["tool_call_id"].(string)
	require.NotEmpty(t, id)
	require.Equal(t, id, completed[0]["tool_call_id"])
}
