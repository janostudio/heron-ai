package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	var resp openAIResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"choices":[{"message":{"content":"hi","reasoning_content":"think"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":20,"completion_tokens":8,"total_tokens":28,
		         "prompt_cache_hit_tokens":4,"prompt_cache_miss_tokens":1}
	}`), &resp))

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
	var resp openAIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"usage":{"prompt_tokens_details":{"cached_tokens":7}}}`), &resp))
	out := convertOpenAIResponse(resp)
	assert.Equal(t, 7, out.Usage.CacheReadInputTokens)
}

func TestConvertOpenAIResponse_ReasoningTokens(t *testing.T) {
	var resp openAIResponse
	require.NoError(t, json.Unmarshal([]byte(`{"usage":{"completion_tokens_details":{"reasoning_tokens":3}}}`), &resp))
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

// 流式路径是这个引擎唯一的调用方式，usage 里的缓存字段必须一起搬进来。
// 之前只搬了 prompt/completion/total 三个，于是所有请求的缓存命中都记成 0，
// 看上去像"网关不支持缓存"，其实是没读。
func TestChatStreamCarriesCacheUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3000,\"completion_tokens\":5,\"total_tokens\":3005,\"prompt_cache_hit_tokens\":2816,\"prompt_cache_miss_tokens\":184}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	provider := NewOpenAIProviderWithProfile("key", server.URL, "test-model", types.ModelProfile{
		Name: "test-model", Stream: true,
	})
	response, err := provider.Chat(context.Background(),
		[]types.Message{{Role: "user", Content: "hi"}}, nil,
		types.ModelConfig{Model: "test-model"})
	require.NoError(t, err)

	assert.Equal(t, 3000, response.Usage.PromptTokens)
	assert.Equal(t, 2816, response.Usage.PromptCacheHitTokens)
	assert.Equal(t, 184, response.Usage.PromptCacheMissTokens)
}
