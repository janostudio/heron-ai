package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// unknownCapacityContext builds a manager whose provider never told the loop
// how many input tokens the model accepts (MaxInputTokens stays 0). That is the
// configuration `CompactForce` runs under after a provider rejects an oversized
// prompt, and the one the trimming step had no budget for.
func unknownCapacityContext(t *testing.T) *MessageContextManager {
	t.Helper()
	manager := NewContextManager(types.ContextConfig{MaxInputTokens: 0})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "summarise the build"}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role:      "assistant",
		Content:   "reading the log",
		ToolCalls: []types.ToolCall{{ID: "call-1", Name: "Read", Arguments: map[string]any{"file": "build.log"}}},
	}))
	require.NoError(t, manager.AddMessage(types.Message{
		Role:       "tool",
		ToolCallID: "call-1",
		ToolName:   "Read",
		Content:    "TOOL_OUTPUT_MARKER: the build failed on step 3",
	}))
	return manager
}

func TestCompactForceWithoutCapacityKeepsToolOutput(t *testing.T) {
	manager := unknownCapacityContext(t)

	require.NoError(t, manager.CompactForce(context.Background()))

	messages := manager.Messages()
	require.NotEmpty(t, messages)

	keptToolOutput := false
	for _, message := range messages {
		if message.Role == "tool" && strings.Contains(message.Content, "TOOL_OUTPUT_MARKER") {
			keptToolOutput = true
		}
	}
	require.True(t, keptToolOutput,
		"compaction without a configured capacity must not empty the tool output: %+v", messages)
}

// TestCompactForceWithoutCapacityKeepsSummary pins the second half of the same
// defect: with target == 0 the summary budget was non-positive, so
// fitCompactionSummary dropped the summary while the messages it described had
// already been thrown away — the model saw a conversation that silently lost
// its history with no account of it.
func TestCompactForceWithoutCapacityKeepsSummary(t *testing.T) {
	manager := NewContextManager(types.ContextConfig{MaxInputTokens: 0})
	require.NoError(t, manager.AddMessage(types.Message{Role: "system", Content: "system"}))
	require.NoError(t, manager.AddMessage(types.Message{Role: "user", Content: "first question"}))
	for i := 0; i < 4; i++ {
		require.NoError(t, manager.AddMessage(types.Message{
			Role:      "assistant",
			Content:   "calling a tool",
			ToolCalls: []types.ToolCall{{ID: "call", Name: "Read", Arguments: map[string]any{"file": "a.log"}}},
		}))
		require.NoError(t, manager.AddMessage(types.Message{
			Role: "tool", ToolCallID: "call", ToolName: "Read", Content: "result " + strings.Repeat("x", 10),
		}))
	}

	require.NoError(t, manager.CompactForce(context.Background()))

	hasSummary := false
	for _, message := range manager.Messages() {
		if strings.Contains(message.Content, compactedContextMarker) {
			hasSummary = true
		}
	}
	require.True(t, hasSummary, "a dropped history must be represented by a summary")
}
