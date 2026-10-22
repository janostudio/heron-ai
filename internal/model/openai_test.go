package model

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestConvertOpenAIResponse_EmptyChoices(t *testing.T) {
	out := convertOpenAIResponse(openAIResponse{})
	assert.Empty(t, out.Text)
	assert.Empty(t, out.Reasoning)
	assert.Empty(t, out.FinishReason)
	assert.Empty(t, out.ToolCalls)
	assert.Equal(t, 0, out.Usage.PromptTokens)
}

func TestConvertOpenAIResponse_UsageAndContent(t *testing.T) {
	resp := openAIResponse{
		Choices: []struct {
			Message struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Content          string           `json:"content"`
					ReasoningContent string           `json:"reasoning_content"`
					ToolCalls        []openAIToolCall `json:"tool_calls"`
				}{Content: "hi", ReasoningContent: "think"},
				FinishReason: "stop",
			},
		},
		Usage: struct {
			PromptTokens             int `json:"prompt_tokens"`
			CompletionTokens         int `json:"completion_tokens"`
			TotalTokens              int `json:"total_tokens"`
			PromptCacheHitTokens     int `json:"prompt_cache_hit_tokens"`
			PromptCacheMissTokens    int `json:"prompt_cache_miss_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			PromptTokensDetails      *struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			CompletionTokensDetails *struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		}{PromptTokens: 20, CompletionTokens: 8, TotalTokens: 28, PromptCacheHitTokens: 4, PromptCacheMissTokens: 1},
	}
	out := convertOpenAIResponse(resp)
	assert.Equal(t, "hi", out.Text)
	assert.Equal(t, "think", out.Reasoning)
	assert.Equal(t, "stop", out.FinishReason)
	assert.Equal(t, 20, out.Usage.PromptTokens)
	assert.Equal(t, 8, out.Usage.CompletionTokens)
	assert.Equal(t, 28, out.Usage.TotalTokens)
	assert.Equal(t, 4, out.Usage.PromptCacheHitTokens)
	assert.Equal(t, 1, out.Usage.PromptCacheMissTokens)
}

func TestConvertOpenAIResponse_PromptTokensDetailsFallback(t *testing.T) {
	resp := openAIResponse{}
	resp.Usage.PromptTokensDetails = &struct {
		CachedTokens int `json:"cached_tokens"`
	}{CachedTokens: 7}
	out := convertOpenAIResponse(resp)
	assert.Equal(t, 7, out.Usage.CacheReadInputTokens)
}

func TestConvertOpenAIResponse_ReasoningTokens(t *testing.T) {
	resp := openAIResponse{}
	resp.Usage.CompletionTokensDetails = &struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	}{ReasoningTokens: 3}
	out := convertOpenAIResponse(resp)
	assert.Equal(t, 3, out.Usage.ReasoningTokens)
}

func TestConvertOpenAIResponse_ToolCalls(t *testing.T) {
	resp := openAIResponse{
		Choices: []struct {
			Message struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Content          string           `json:"content"`
					ReasoningContent string           `json:"reasoning_content"`
					ToolCalls        []openAIToolCall `json:"tool_calls"`
				}{ToolCalls: []openAIToolCall{
					{ID: "c1", Type: "function", Function: openAIFunctionCall{Name: "f", Arguments: `{"x":1}`}},
				}},
				FinishReason: "tool_calls",
			},
		},
	}
	out := convertOpenAIResponse(resp)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, "c1", out.ToolCalls[0].ID)
	assert.Equal(t, "f", out.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"x": float64(1)}, out.ToolCalls[0].Arguments)
	assert.Equal(t, "tool_calls", out.FinishReason)
}

func TestConvertOpenAIResponse_ToolCallInvalidJSON(t *testing.T) {
	resp := openAIResponse{
		Choices: []struct {
			Message struct {
				Content          string           `json:"content"`
				ReasoningContent string           `json:"reasoning_content"`
				ToolCalls        []openAIToolCall `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		}{
			{
				Message: struct {
					Content          string           `json:"content"`
					ReasoningContent string           `json:"reasoning_content"`
					ToolCalls        []openAIToolCall `json:"tool_calls"`
				}{ToolCalls: []openAIToolCall{
					{ID: "c1", Function: openAIFunctionCall{Name: "f", Arguments: `garbage`}},
				}},
			},
		},
	}
	out := convertOpenAIResponse(resp)
	require.Len(t, out.ToolCalls, 1)
	assert.Equal(t, map[string]any{"_raw": "garbage"}, out.ToolCalls[0].Arguments)
}

// diffIDs/description helpers back the rejection log: they must surface a
// tool_call with no result and a result with no call.
func TestDiffIDs(t *testing.T) {
	assert.Equal(t, []string{"b"}, diffIDs([]string{"a", "b", "c"}, []string{"a", "c"}))
	assert.Empty(t, diffIDs([]string{"a"}, []string{"a"}))
	assert.Empty(t, diffIDs([]string{""}, nil), "blank ids are ignored")
}

func TestLogRejectedRequestDoesNotPanic(t *testing.T) {
	// The logger is a no-op unless wired, but the summarizer must still run
	// over a mixed message list without panicking.
	req := openAIRequest{Messages: []openAIMessage{
		{Role: "user", Content: "hi"},
		{Role: "assistant", ToolCalls: []openAIToolCall{{ID: "tc-1", Function: openAIFunctionCall{Name: "Bash"}}}},
		{Role: "tool", ToolCallID: "tc-1"},
		{Role: "tool", ToolCallID: ""}, // orphaned / missing id
	}}
	require.NotPanics(t, func() {
		logRejectedRequest("rejected", 400, req, []byte(`{"error":{"message":"x"}}`))
	})
}

// A collaborator's output reaches the model as a normal message whose `name`
// says which Team produced it. Without the field the model cannot tell another
// Team's claim from its own earlier output.
func TestOpenAIMessagesCarryParticipantName(t *testing.T) {
	provider := &OpenAIProvider{}
	messages, err := provider.convertMessages(context.Background(), []types.Message{
		{Role: "assistant", TeamID: "owner", Content: "the branch is main"},
		{Role: "assistant", Content: "my own earlier answer"},
		{Role: "user", Content: "查看主分支"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if messages[0].Name != "owner" {
		t.Fatalf("collaborator message must carry its team as name, got %q", messages[0].Name)
	}
	if messages[1].Name != "" || messages[2].Name != "" {
		t.Fatalf("own output and user input must stay unnamed: %#v", messages)
	}

	encoded, err := json.Marshal(messages[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"name":"owner"`) {
		t.Fatalf("name missing from the wire payload: %s", encoded)
	}
	encoded, err = json.Marshal(messages[1])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"name"`) {
		t.Fatalf("an empty name must be omitted, not sent blank: %s", encoded)
	}
}
