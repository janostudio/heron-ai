package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type memoryTranscriptStore struct {
	saved []types.AgentTranscript
}

func (s *memoryTranscriptStore) Save(_ context.Context, transcript types.AgentTranscript) error {
	s.saved = append(s.saved, transcript)
	return nil
}

func (s *memoryTranscriptStore) Load(_ context.Context, _ string) (*types.AgentTranscript, error) {
	return nil, nil
}

// TestConditionalRuleReachesTheRequestAndTheLog is the end-to-end half of the
// mechanism at the Agent level: the model writes into docs/, the rule about
// writing into docs/ is injected at the tail of the very next request, one event
// records why, and the rule body does not become part of the conversation.
func TestConditionalRuleReachesTheRequestAndTheLog(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{
			Text: "writing",
			ToolCalls: []types.ToolCall{{
				ID: "tc-1", Name: "Write",
				Arguments: map[string]any{"file": "docs/self/note.md", "content": "# note"},
			}},
			Usage: types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}}
	loop := NewTurnLoop(
		model,
		&recordingToolExecutor{},
		NewGuardrailChecker(nil, nil),
		NewRouteParser(),
		NewHITLGate(time.Minute),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "write a note"}}},
	)
	writer := &fakeSessionWriter{}
	loop.SetSessionWriter(writer)
	transcripts := &memoryTranscriptStore{}
	loop.transcripts = transcripts

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Write"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, types.AgentRequest{
		FlowSessionID: "fs-rules",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
		AgentID:       "test-agent",
		AgentTurnID:   "at-1",
		Attempt:       1,
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "write a note"}},
		ConditionalRules: []types.ConditionalRule{{
			ID:    "docs-placement",
			Paths: []string{"docs/**"},
			Body:  "Write explanatory documents under docs/self or docs/repos.",
		}},
	})
	require.NoError(t, err)

	// 1. It reached the model, at the tail, as a system message.
	require.NotEmpty(t, model.lastMessages)
	last := model.lastMessages[len(model.lastMessages)-1]
	require.Equal(t, "system", last.Role)
	require.Contains(t, last.Content, "[rule:docs-placement trigger=docs/self/note.md kind=write]")
	require.Contains(t, last.Content, "docs/self or docs/repos")

	// 2. It is recorded once, with the path that triggered it.
	events := toolEventsOfType(writer, types.EventRuleInjected)
	require.Len(t, events, 1, "one event per rule, not one per round")
	require.Equal(t, "docs-placement", events[0]["rule_id"])
	require.Equal(t, "docs/self/note.md", events[0]["trigger_path"])
	require.Equal(t, "write", events[0]["trigger_kind"])

	// 3. It did not become conversation: the transcript is what the next turn
	// continues from, and a rule replayed there would apply long after the work
	// left that directory.
	require.NotEmpty(t, transcripts.saved)
	for _, message := range transcripts.saved[len(transcripts.saved)-1].Messages {
		require.NotContains(t, message.Content, "[rule:docs-placement]",
			"a rule note must not be persisted into the conversation")
	}
}

// TestConditionalRuleNotInjectedWithoutMatchingPath: a turn that never touches
// the directory must not pay for its contract.
func TestConditionalRuleNotInjectedWithoutMatchingPath(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{
			Text:      "reading",
			ToolCalls: []types.ToolCall{{ID: "tc-1", Name: "Read", Arguments: map[string]any{"file": "internal/agent/runtime.go"}}},
			Usage:     types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}}
	loop := NewTurnLoop(model, &recordingToolExecutor{}, NewGuardrailChecker(nil, nil), NewRouteParser(),
		NewHITLGate(time.Minute), NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "read"}}})
	writer := &fakeSessionWriter{}
	loop.SetSessionWriter(writer)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		FlowSessionID: "fs-rules-2",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
		AgentID:       "test-agent",
		AgentTurnID:   "at-1",
		Attempt:       1,
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "read"}},
		ConditionalRules: []types.ConditionalRule{{
			ID: "docs-placement", Paths: []string{"docs/**"}, Body: "docs body",
		}},
	})
	require.NoError(t, err)

	for _, message := range model.lastMessages {
		assert.NotContains(t, message.Content, "[rule:docs-placement]")
	}
	assert.Empty(t, toolEventsOfType(writer, types.EventRuleInjected))
}

// TestBashWriteTriggersTheWriteContract: the write path in this workspace is
// mostly a shell command, so a contract that only reads tool operations would
// never fire for the writes that matter.
func TestBashWriteTriggersTheWriteContract(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{
			Text: "writing",
			ToolCalls: []types.ToolCall{{
				ID: "tc-1", Name: "Bash",
				Arguments: map[string]any{"command": "cat > docs/INDEX.md <<'EOF'\n# Docs\nEOF"},
			}},
			Usage: types.TokenUsage{TotalTokens: 10},
		},
		{Text: "done", Usage: types.TokenUsage{TotalTokens: 10}},
	}}
	loop := NewTurnLoop(model, &recordingToolExecutor{}, NewGuardrailChecker(nil, nil), NewRouteParser(),
		NewHITLGate(time.Minute), NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "write"}}})
	writer := &fakeSessionWriter{}
	loop.SetSessionWriter(writer)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "test-agent",
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		FlowSessionID: "fs-rules-3",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
		AgentID:       "test-agent",
		AgentTurnID:   "at-1",
		Attempt:       1,
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "write"}},
		ConditionalRules: []types.ConditionalRule{{
			ID: "docs-placement", Paths: []string{"docs/**"}, Body: "docs body",
		}},
	})
	require.NoError(t, err)

	last := model.lastMessages[len(model.lastMessages)-1]
	require.Equal(t, "system", last.Role)
	require.Contains(t, last.Content, "[rule:docs-placement trigger=docs/INDEX.md kind=write]")
}
