package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBuildModelRequestStatsHashesStablePrefixAndTools(t *testing.T) {
	messages := []types.Message{
		{Role: "system", Content: "stable instructions"},
		{Role: "user", Content: "dynamic input"},
		{Role: "assistant", Content: "tool request", ToolCalls: []types.ToolCall{{
			ID: "call-1", Name: "Read", Arguments: map[string]any{"file": "project/a.txt"},
		}}},
		{Role: "tool", ToolCallID: "call-1", Content: "file contents"},
	}
	tools := []types.JSONSchema{{
		Name: "Read",
		Type: "object",
		Properties: map[string]types.JSONProperty{
			"file": {Type: "string"},
		},
		Required: []string{"file"},
	}}

	first := buildModelRequestStats(1, messages, tools, 42, false)
	second := buildModelRequestStats(1, messages, tools, 42, false)

	require.Equal(t, first.PromptHash, second.PromptHash)
	require.Equal(t, first.StablePrefixHash, second.StablePrefixHash)
	require.Equal(t, first.ToolSchemaHash, second.ToolSchemaHash)
	require.Equal(t, 4, first.MessageCount)
	require.Equal(t, 1, first.ToolSchemaCount)
	require.Equal(t, 42, first.EstimatedPromptTokens)
	require.NotEmpty(t, first.PromptHash)
	require.NotEmpty(t, first.StablePrefixHash)
	require.NotEmpty(t, first.ToolSchemaHash)
	require.Greater(t, first.SystemChars, 0)
	require.Greater(t, first.ToolMessageChars, 0)
}

func TestBuildModelRequestStatsStablePrefixIgnoresDynamicTail(t *testing.T) {
	base := []types.Message{
		{Role: "system", Content: "stable instructions"},
		{Role: "user", Content: "first input"},
	}
	changed := []types.Message{
		{Role: "system", Content: "stable instructions"},
		{Role: "user", Content: "second input"},
	}

	first := buildModelRequestStats(0, base, nil, 10, false)
	second := buildModelRequestStats(0, changed, nil, 11, false)

	require.Equal(t, first.StablePrefixHash, second.StablePrefixHash)
	require.NotEqual(t, first.PromptHash, second.PromptHash)
}

func TestBuildModelRequestStatsStablePrefixChangesWhenToolsChange(t *testing.T) {
	messages := []types.Message{{Role: "system", Content: "stable instructions"}}
	readTool := []types.JSONSchema{{Name: "Read", Type: "object"}}
	writeTool := []types.JSONSchema{{Name: "Write", Type: "object"}}

	first := buildModelRequestStats(0, messages, readTool, 10, false)
	second := buildModelRequestStats(0, messages, writeTool, 10, false)

	require.NotEqual(t, first.StablePrefixHash, second.StablePrefixHash)
	require.NotEqual(t, first.ToolSchemaHash, second.ToolSchemaHash)
}
