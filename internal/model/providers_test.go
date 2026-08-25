package model

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
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
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
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

func TestAnthropicProvider_TranslatesNativeMessages(t *testing.T) {
	var request map[string]any
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/v1/messages", r.URL.Path)
		require.Equal(t, "test-key", r.Header.Get("x-api-key"))
		require.Equal(t, defaultAnthropicVersion, r.Header.Get("anthropic-version"))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		return jsonResponse(`{
			"content": [{"type": "text", "text": "hello from claude"}],
			"usage": {"input_tokens": 4, "output_tokens": 6}
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
