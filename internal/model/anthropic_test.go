package model

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// --- mergeAnthropicMessages / appendAnthropicContent ---

func TestMergeAnthropicMessages_NoMessages(t *testing.T) {
	assert.Nil(t, mergeAnthropicMessages(nil))
	assert.Empty(t, mergeAnthropicMessages([]anthropicMessage{}))
}

func TestMergeAnthropicMessages_SingleMessage(t *testing.T) {
	in := []anthropicMessage{{Role: "user", Content: "hello"}}
	out := mergeAnthropicMessages(in)
	require.Len(t, out, 1)
	assert.Equal(t, "hello", out[0].Content)
}

func TestMergeAnthropicMessages_MergesConsecutiveUserText(t *testing.T) {
	in := []anthropicMessage{
		{Role: "user", Content: "first"},
		{Role: "user", Content: "second"},
	}
	out := mergeAnthropicMessages(in)
	require.Len(t, out, 1)
	assert.Equal(t, "user", out[0].Role)
	assert.Equal(t, "first\n\nsecond", out[0].Content)
}

func TestMergeAnthropicMessages_DoesNotMergeDifferentRoles(t *testing.T) {
	in := []anthropicMessage{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
		{Role: "user", Content: "q2"},
	}
	out := mergeAnthropicMessages(in)
	require.Len(t, out, 3)
	assert.Equal(t, "user", out[0].Role)
	assert.Equal(t, "assistant", out[1].Role)
	assert.Equal(t, "user", out[2].Role)
}

func TestMergeAnthropicMessages_MergesToolResultBlocks(t *testing.T) {
	in := []anthropicMessage{
		{Role: "user", Content: []anthropicBlock{{Type: "tool_result", ToolUseID: "t1", Content: "x"}}},
		{Role: "user", Content: []anthropicBlock{{Type: "tool_result", ToolUseID: "t2", Content: "y"}}},
	}
	out := mergeAnthropicMessages(in)
	require.Len(t, out, 1)
	blocks, ok := out[0].Content.([]anthropicBlock)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	assert.Equal(t, "t1", blocks[0].ToolUseID)
	assert.Equal(t, "t2", blocks[1].ToolUseID)
}

func TestAppendAnthropicContent_BlockBlock(t *testing.T) {
	left := []anthropicBlock{{Type: "text", Text: "a"}}
	right := []anthropicBlock{{Type: "text", Text: "b"}}
	out := appendAnthropicContent(left, right)
	blocks, ok := out.([]anthropicBlock)
	require.True(t, ok)
	require.Len(t, blocks, 2)
}

func TestAppendAnthropicContent_StringString(t *testing.T) {
	out := appendAnthropicContent("a", "b")
	assert.Equal(t, "a\n\nb", out)
}

func TestAppendAnthropicContent_MixedTypes(t *testing.T) {
	out := appendAnthropicContent("text", []anthropicBlock{{Type: "text", Text: "block"}})
	blocks, ok := out.([]anthropicBlock)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	assert.Equal(t, "text", blocks[0].Text)
	assert.Equal(t, "block", blocks[1].Text)
}

// --- anthropicReasoning ---

func TestAnthropicReasoning_Nil(t *testing.T) {
	thinking, output := anthropicReasoning(nil)
	assert.Nil(t, thinking)
	assert.Nil(t, output)
}

func TestAnthropicReasoning_Disabled(t *testing.T) {
	for _, typ := range []string{"none", "disabled"} {
		thinking, output := anthropicReasoning(&types.ReasoningConfig{Type: typ})
		assert.Nil(t, thinking, "type %s", typ)
		assert.Nil(t, output, "type %s", typ)
	}
}

func TestAnthropicReasoning_EmptyTypeWithBudget(t *testing.T) {
	budget := 1024
	thinking, output := anthropicReasoning(&types.ReasoningConfig{BudgetTokens: &budget})
	require.NotNil(t, thinking)
	assert.Equal(t, "enabled", thinking.Type)
	assert.Equal(t, 1024, thinking.BudgetTokens)
	assert.Nil(t, output)
}

func TestAnthropicReasoning_EmptyTypeWithEffort(t *testing.T) {
	thinking, output := anthropicReasoning(&types.ReasoningConfig{Effort: "high"})
	require.NotNil(t, thinking)
	assert.Equal(t, "adaptive", thinking.Type)
	require.NotNil(t, output)
	require.NotNil(t, output.Effort)
	assert.Equal(t, "high", *output.Effort)
}

func TestAnthropicReasoning_ExplicitEnabled(t *testing.T) {
	budget := 2048
	thinking, output := anthropicReasoning(&types.ReasoningConfig{Type: "enabled", BudgetTokens: &budget})
	require.NotNil(t, thinking)
	assert.Equal(t, "enabled", thinking.Type)
	assert.Equal(t, 2048, thinking.BudgetTokens)
	assert.Nil(t, output)
}

func TestAnthropicReasoning_ExplicitWithEffort(t *testing.T) {
	thinking, output := anthropicReasoning(&types.ReasoningConfig{Type: "enabled", Effort: "medium"})
	require.NotNil(t, thinking)
	assert.Equal(t, "enabled", thinking.Type)
	require.NotNil(t, output)
	require.NotNil(t, output.Effort)
	assert.Equal(t, "medium", *output.Effort)
}

// --- convertAnthropicResponse ---

func TestConvertAnthropicResponse_UsageAndFinishReason(t *testing.T) {
	resp := anthropicResponse{
		StopReason: "end_turn",
		Usage: struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		}{InputTokens: 10, OutputTokens: 5, CacheReadInputTokens: 3, CacheCreationInputTokens: 2},
		Content: []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}{{Type: "text", Text: "hello"}},
	}
	out := convertAnthropicResponse(resp)
	assert.Equal(t, "end_turn", out.FinishReason)
	assert.Equal(t, 10, out.Usage.PromptTokens)
	assert.Equal(t, 5, out.Usage.CompletionTokens)
	assert.Equal(t, 15, out.Usage.TotalTokens)
	assert.Equal(t, 3, out.Usage.CacheReadInputTokens)
	assert.Equal(t, 2, out.Usage.CacheCreationInputTokens)
	assert.Equal(t, "hello", out.Text)
}

func TestConvertAnthropicResponse_EmptyContent(t *testing.T) {
	out := convertAnthropicResponse(anthropicResponse{})
	assert.Empty(t, out.Text)
	assert.Empty(t, out.ToolCalls)
	assert.Empty(t, out.FinishReason)
}

func TestConvertAnthropicResponse_ThinkingAndToolUse(t *testing.T) {
	resp := anthropicResponse{
		Content: []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}{
			{Type: "thinking", Thinking: "reasoned"},
			{Type: "tool_use", ID: "c1", Name: "lookup", Input: json.RawMessage(`{"k":"v"}`)},
		},
	}
	out := convertAnthropicResponse(resp)
	assert.Equal(t, "reasoned", out.Reasoning)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, "c1", out.ToolCalls[0].ID)
	assert.Equal(t, "lookup", out.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"k": "v"}, out.ToolCalls[0].Arguments)
}

func TestConvertAnthropicResponse_ToolUseInvalidJSON(t *testing.T) {
	resp := anthropicResponse{
		Content: []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			Thinking  string          `json:"thinking"`
			Signature string          `json:"signature"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
		}{{Type: "tool_use", ID: "c1", Name: "bad", Input: json.RawMessage(`not-json`)}},
	}
	out := convertAnthropicResponse(resp)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, map[string]any{"_raw": "not-json"}, out.ToolCalls[0].Arguments)
}

// --- convertMessages empty-user guard ---

func TestAnthropicConvertMessages_SkipsEmptyUserMessage(t *testing.T) {
	p := NewAnthropicProvider("key", "http://test.local/v1", "model")
	system, messages, err := p.convertMessages(context.Background(), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: ""},
		{Role: "user", Content: "real"},
	})
	require.NoError(t, err)
	assert.Equal(t, "sys", system)
	// The empty user message must be skipped, not emitted with empty content.
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0].Role)
	blocks, ok := messages[0].Content.([]anthropicBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	assert.Equal(t, "real", blocks[0].Text)
}

func TestAnthropicConvertMessages_EmptyUserWithPartsIsKept(t *testing.T) {
	p := NewAnthropicProvider("key", "http://test.local/v1", "model")
	_, messages, err := p.convertMessages(context.Background(), []types.Message{
		{Role: "user", Content: "", Parts: []types.ContentPart{{Type: "text", Text: "from part"}}},
	})
	require.NoError(t, err)
	require.Len(t, messages, 1)
	blocks, ok := messages[0].Content.([]anthropicBlock)
	require.True(t, ok)
	require.Len(t, blocks, 1)
	assert.Equal(t, "from part", blocks[0].Text)
}

// --- hasAnthropicOptionalParameters / withoutAnthropicOptionalParameters ---

func TestHasAnthropicOptionalParameters(t *testing.T) {
	assert.False(t, hasAnthropicOptionalParameters(anthropicRequest{}))
	temp := 0.5
	assert.True(t, hasAnthropicOptionalParameters(anthropicRequest{Temperature: &temp}))
	assert.True(t, hasAnthropicOptionalParameters(anthropicRequest{Thinking: &anthropicThinking{Type: "enabled"}}))
	assert.True(t, hasAnthropicOptionalParameters(anthropicRequest{OutputConfig: &anthropicOutputConfig{}}))
}

func TestWithoutAnthropicOptionalParameters(t *testing.T) {
	temp := 0.5
	topP := 0.9
	topK := 40
	req := anthropicRequest{
		Temperature:  &temp,
		TopP:         &topP,
		TopK:         &topK,
		Thinking:     &anthropicThinking{Type: "enabled"},
		OutputConfig: &anthropicOutputConfig{},
	}
	cleaned := withoutAnthropicOptionalParameters(req)
	assert.Nil(t, cleaned.Temperature)
	assert.Nil(t, cleaned.TopP)
	assert.Nil(t, cleaned.TopK)
	assert.Nil(t, cleaned.Thinking)
	assert.Nil(t, cleaned.OutputConfig)
}

// --- hasOptionalGenerationParameters / withoutOptionalGenerationParameters ---

func TestHasOptionalGenerationParameters(t *testing.T) {
	assert.False(t, hasOptionalGenerationParameters(openAIRequest{}))
	temp := 0.5
	assert.True(t, hasOptionalGenerationParameters(openAIRequest{Temperature: &temp}))
	assert.True(t, hasOptionalGenerationParameters(openAIRequest{ReasoningEffort: "high"}))
}

func TestWithoutOptionalGenerationParameters(t *testing.T) {
	temp := 0.5
	req := openAIRequest{
		Temperature:       &temp,
		TopP:              &temp,
		TopK:              func() *int { i := 40; return &i }(),
		RepetitionPenalty: &temp,
		ReasoningEffort:   "high",
	}
	cleaned := withoutOptionalGenerationParameters(req)
	assert.Nil(t, cleaned.Temperature)
	assert.Nil(t, cleaned.TopP)
	assert.Nil(t, cleaned.TopK)
	assert.Nil(t, cleaned.RepetitionPenalty)
	assert.Empty(t, cleaned.ReasoningEffort)
}

// --- isUnsupportedParameterError ---
func TestIsUnsupportedParameterError(t *testing.T) {
	assert.False(t, isUnsupportedParameterError(nil))
	assert.False(t, isUnsupportedParameterError(assert.AnError))
	for _, msg := range []string{
		"unsupported parameter: temperature",
		"top_p is not supported",
		"unknown parameter foo",
		"unrecognized parameter bar",
		"invalid parameter baz",
		"this model does not support reasoning",
	} {
		assert.True(t, isUnsupportedParameterError(errors.New(msg)), msg)
	}
}
