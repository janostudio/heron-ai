package model

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestMergeProfileDefaults_AgentOverridesModel(t *testing.T) {
	temperature := 0.7
	topP := 0.8
	maxOutput := 64000
	profile := types.ModelProfile{
		ID:              "hy3-ioa",
		Name:            "hy3-ioa",
		BaseURL:         "http://model",
		Temperature:     &temperature,
		TopP:            &topP,
		MaxOutputTokens: maxOutput,
	}

	overrideTemperature := 0.0
	overrideMaxOutput := 8192
	effective := MergeProfileDefaults(profile, types.ModelConfig{
		Model:           profile.Name,
		Temperature:     &overrideTemperature,
		MaxOutputTokens: &overrideMaxOutput,
	})

	require.NotNil(t, effective.Temperature)
	require.NotNil(t, effective.TopP)
	require.NotNil(t, effective.MaxOutputTokens)
	assert.Equal(t, 0.0, *effective.Temperature)
	assert.Equal(t, 0.8, *effective.TopP)
	assert.Equal(t, 8192, *effective.MaxOutputTokens)
}

func TestOpenAIProvider_UsesModelDefaults(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		require.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{
			"choices": [{"message": {"content": "ok"}}],
			"usage": {
				"prompt_tokens": 3,
				"completion_tokens": 2,
				"total_tokens": 5,
				"prompt_cache_hit_tokens": 2,
				"prompt_cache_miss_tokens": 1,
				"prompt_tokens_details": {"cached_tokens": 2}
			}
		}`), nil
	})

	temperature := 0.7
	topP := 0.8
	maxOutput := 64000
	profile := types.ModelProfile{
		ID:                  "hy3-ioa",
		Name:                "hy3-ioa",
		BaseURL:             "http://test.local/v1",
		APIKey:              "test-key",
		Temperature:         &temperature,
		TopP:                &topP,
		MaxOutputTokens:     maxOutput,
		SupportsToolCall:    boolPtr(true),
		SupportsTemperature: boolPtr(true),
		SupportsTopP:        boolPtr(true),
	}
	provider := NewOpenAIProviderWithProfile("test-key", "http://test.local/v1", profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	response, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, "ok", response.Text)
	assert.Equal(t, float64(64000), request["max_completion_tokens"])
	assert.Equal(t, 0.7, request["temperature"])
	assert.Equal(t, 0.8, request["top_p"])
	assert.NotContains(t, request, "max_tokens")
	assert.Equal(t, 2, response.Usage.PromptCacheHitTokens)
	assert.Equal(t, 1, response.Usage.PromptCacheMissTokens)
	assert.Equal(t, 2, response.Usage.CacheReadInputTokens)
}

func TestOpenAIProvider_PreservesFinishReason(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResponse(`{
			"choices": [{"message": {"content": "{\"partial\":"}, "finish_reason": "length"}]
		}`), nil
	})
	provider := NewOpenAIProviderWithProfile("key", "http://test.local/v1", "model", types.ModelProfile{
		ID: "model", Name: "model", BaseURL: "http://test.local/v1",
	})
	provider.client = &http.Client{Transport: transport}

	response, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "json"},
	}, nil, types.ModelConfig{Model: "model"})
	require.NoError(t, err)
	require.Equal(t, "length", response.FinishReason)
}

func TestOpenAIProviderDisablesReasoningForExplicitNone(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"choices":[{"message":{"content":"{}"}}]}`), nil
	})
	provider := NewOpenAIProviderWithProfile("key", "http://test.local/v1", "model", types.ModelProfile{
		ID: "model", Name: "model", BaseURL: "http://test.local/v1",
		SupportsReasoning: boolPtr(true),
	})
	provider.client = &http.Client{Transport: transport}

	_, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "return json"},
	}, nil, types.ModelConfig{
		Model:     "model",
		Reasoning: &types.ReasoningConfig{Type: "none"},
	})
	require.NoError(t, err)
	require.Equal(t, "none", request["reasoning_effort"])
}

func TestOpenAIProvider_AgentCanOverrideModelDefaults(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`), nil
	})

	modelTemperature := 0.7
	profile := types.ModelProfile{
		ID:          "model",
		Name:        "model",
		BaseURL:     "http://test.local/v1",
		Temperature: &modelTemperature,
	}
	provider := NewOpenAIProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	agentTemperature := 0.0
	agentMaxOutput := 2048
	_, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{
		Model:           profile.Name,
		Temperature:     &agentTemperature,
		MaxOutputTokens: &agentMaxOutput,
	})
	require.NoError(t, err)
	assert.Equal(t, 0.0, request["temperature"])
	assert.Equal(t, float64(2048), request["max_completion_tokens"])
}

func TestOpenAIProvider_MapsImageContentPart(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"choices":[{"message":{"content":"seen"}}]}`), nil
	})
	profile := types.ModelProfile{
		ID: "vision", Name: "vision", BaseURL: "http://test.local/v1",
		SupportsImages: boolPtr(true),
	}
	provider := NewOpenAIProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	_, err := provider.Chat(context.Background(), []types.Message{{
		Role: "user", Content: "look",
		Parts: []types.ContentPart{{
			Type: "image",
			Media: &types.MediaAttachment{
				Kind: "image", MIMEType: "image/png", SourceType: "base64",
				DataBase64: "cG5n",
			},
		}},
	}}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	messages := request["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	require.Len(t, content, 2)
	require.Equal(t, "image_url", content[1].(map[string]any)["type"])
	require.Contains(t, content[1].(map[string]any)["image_url"].(map[string]any)["url"], "data:image/png;base64")
}

func TestAnthropicProvider_MapsImageContentPart(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"content":[{"type":"text","text":"seen"}]}`), nil
	})
	profile := types.ModelProfile{
		ID: "claude-vision", Name: "claude-vision",
		BaseURL: "http://test.local/v1", Protocol: "anthropic_messages",
		SupportsImages: boolPtr(true),
	}
	provider := NewAnthropicProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	_, err := provider.Chat(context.Background(), []types.Message{{
		Role: "user", Content: "look",
		Parts: []types.ContentPart{{
			Type: "image",
			Media: &types.MediaAttachment{
				Kind: "image", MIMEType: "image/png", SourceType: "base64",
				DataBase64: "cG5n",
			},
		}},
	}}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	messages := request["messages"].([]any)
	blocks := messages[0].(map[string]any)["content"].([]any)
	require.Len(t, blocks, 2)
	require.Equal(t, "image", blocks[1].(map[string]any)["type"])
	require.Equal(t, "base64", blocks[1].(map[string]any)["source"].(map[string]any)["type"])
}

func TestAnthropicProvider_TranslatesNativeMessages(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-api-key"))
		require.Equal(t, defaultAnthropicVersion, r.Header.Get("anthropic-version"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{
			"content": [{"type": "text", "text": "hello from claude"}],
			"usage": {
				"input_tokens": 4,
				"output_tokens": 6,
				"cache_read_input_tokens": 3,
				"cache_creation_input_tokens": 1
			}
		}`), nil
	})

	maxOutput := 4096
	profile := types.ModelProfile{
		ID:              "claude-test",
		Name:            "claude-test",
		BaseURL:         "http://test.local/v1",
		APIKey:          "test-key",
		Protocol:        "anthropic_messages",
		MaxOutputTokens: 16384,
	}
	provider := NewAnthropicProviderWithProfile("test-key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	response, err := provider.Chat(context.Background(), []types.Message{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{
		Model:           profile.Name,
		MaxOutputTokens: &maxOutput,
	})
	require.NoError(t, err)
	assert.Equal(t, "hello from claude", response.Text)
	assert.Equal(t, float64(4096), request["max_tokens"])
	assert.Equal(t, "You are helpful.", request["system"])
	assert.NotContains(t, request, "max_completion_tokens")
	assert.Equal(t, 3, response.Usage.CacheReadInputTokens)
	assert.Equal(t, 1, response.Usage.CacheCreationInputTokens)
	messages, ok := request["messages"].([]any)
	require.True(t, ok)
	require.Len(t, messages, 1)
	assert.Equal(t, "user", messages[0].(map[string]any)["role"])
}

func TestProviderRouter_SelectsAnthropicByProtocol(t *testing.T) {
	router, err := NewProviderRouter("claude-test", []types.ModelProfile{{
		ID:       "claude-test",
		Name:     "claude-test",
		Protocol: "anthropic_messages",
		BaseURL:  "http://127.0.0.1",
		APIKey:   "key",
	}})
	require.NoError(t, err)
	assert.Equal(t, "claude-test", router.DefaultModel())
	assert.IsType(t, &AnthropicProvider{}, router.providers["claude-test"])
}

func TestOpenAIProvider_UsesIDAsRequestModel(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`), nil
	})
	profile := types.ModelProfile{
		ID:      "hy3-ioa",
		Name:    "hy3-ioa-cp",
		BaseURL: "http://test.local/v1",
	}
	provider := NewOpenAIProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	resp, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, "hy3-ioa", request["model"])
	assert.Equal(t, "hy3-ioa", resp.Model)
}

func TestAnthropicProvider_UsesIDAsRequestModel(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"content":[{"type":"text","text":"ok"}]}`), nil
	})
	profile := types.ModelProfile{
		ID:       "claude-real",
		Name:     "claude-display",
		BaseURL:  "http://test.local/v1",
		Protocol: "anthropic_messages",
	}
	provider := NewAnthropicProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	resp, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, "claude-real", request["model"])
	assert.Equal(t, "claude-real", resp.Model)
}

func TestOpenAIProvider_EmptyIDFallsBackToName(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`), nil
	})
	profile := types.ModelProfile{
		Name:    "name-only",
		BaseURL: "http://test.local/v1",
	}
	provider := NewOpenAIProviderWithProfile("key", profile.BaseURL, profile.Name, profile)
	provider.client = &http.Client{Transport: transport}

	_, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, "name-only", request["model"])
}

func TestProviderRouter_IndexesByNameOnly(t *testing.T) {
	router, err := NewProviderRouter("a-cp", []types.ModelProfile{
		{ID: "hy3-ioa", Name: "a-cp", BaseURL: "http://127.0.0.1", APIKey: "key"},
		{ID: "hy3-ioa", Name: "b-cp", BaseURL: "http://127.0.0.1", APIKey: "key"},
	})
	require.NoError(t, err)

	// Both names resolve to distinct profiles despite sharing the same ID.
	_, ok := router.providers["a-cp"]
	require.True(t, ok)
	_, ok = router.providers["b-cp"]
	require.True(t, ok)

	// The shared ID must not be registered as its own key.
	_, ok = router.providers["hy3-ioa"]
	require.False(t, ok)

	// Default model resolves to the first profile's name.
	assert.Equal(t, "a-cp", router.DefaultModel())
}

func TestOpenAIProvider_StreamAggregatesSSE(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, "/chat/completions", r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"index":0,"delta":{"content":"你","reasoning_content":"思","tool_calls":[]},"finish_reason":""}],"usage":null}` + "\n\n",
			`data: {"choices":[{"index":0,"delta":{"content":"好","reasoning_content":"考","tool_calls":[]},"finish_reason":""}],"usage":null}` + "\n\n",
			`data: {"choices":[{"index":0,"delta":{"content":"","reasoning_content":"","tool_calls":[]},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	profile := types.ModelProfile{
		ID:      "cp-model",
		Name:    "cp-model",
		BaseURL: server.URL,
		Stream:  true,
	}
	provider := NewOpenAIProviderWithProfile("key", server.URL, profile.Name, profile)

	resp, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "hello"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, "你好", resp.Text)
	assert.Equal(t, "思考", resp.Reasoning)
	assert.Equal(t, "stop", resp.FinishReason)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 2, resp.Usage.CompletionTokens)
	assert.Equal(t, 12, resp.Usage.TotalTokens)
	assert.Equal(t, true, request["stream"])
}

func TestOpenAIProvider_StreamAggregatesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"index":0,"delta":{"content":"","tool_calls":[{"index":0,"id":"t1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":""}],"usage":null}` + "\n\n",
			`data: {"choices":[{"index":0,"delta":{"content":"","tool_calls":[{"index":0,"function":{"name":"","arguments":"\"北京\"}"}}]},"finish_reason":"tool_calls"}],"usage":null}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	profile := types.ModelProfile{
		ID:      "cp-model",
		Name:    "cp-model",
		BaseURL: server.URL,
		Stream:  true,
	}
	provider := NewOpenAIProviderWithProfile("key", server.URL, profile.Name, profile)

	resp, err := provider.Chat(context.Background(), []types.Message{
		{Role: "user", Content: "weather?"},
	}, nil, types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	require.Len(t, resp.ToolCalls, 1)
	assert.Equal(t, "t1", resp.ToolCalls[0].ID)
	assert.Equal(t, "get_weather", resp.ToolCalls[0].Name)
	assert.Equal(t, map[string]any{"city": "北京"}, resp.ToolCalls[0].Arguments)
	assert.Equal(t, "tool_calls", resp.FinishReason)
}

func boolPtr(value bool) *bool {
	return &value
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(bytes.NewBufferString(body)),
	}
}
