package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func newRetryCountingServer(t *testing.T, status int, body string, calls *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

// TestOpenAIRateLimitIsNotRetriedWithStrippedParameters is the regression guard
// for the unconditional response_format retry: a 429 used to trigger a second
// request without response_format and possibly a third without the optional
// generation parameters, tripling traffic while the provider was pushing back.
func TestOpenAIRateLimitIsNotRetriedWithStrippedParameters(t *testing.T) {
	var calls int32
	server := newRetryCountingServer(t, http.StatusTooManyRequests, `{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`, &calls)
	defer server.Close()

	profile := types.ModelProfile{ID: "p", Name: "gpt-test", BaseURL: server.URL}
	provider := NewOpenAIProviderWithProfile("key", server.URL, profile.Name, profile)

	format := &types.StructuredOutput{Type: "json_schema", Schema: map[string]any{"reply": map[string]any{"type": "string"}}}
	_, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil,
		types.ModelConfig{Model: profile.Name, ResponseFormat: format})
	require.Error(t, err)
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls),
		"a rate limit must not be retried with stripped parameters")
}

// TestOpenAIUnsupportedResponseFormatIsRetried keeps the recovery the retry
// exists for: a gateway that rejects response_format still gets a second call
// without it.
func TestOpenAIUnsupportedResponseFormatIsRetried(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if atomic.LoadInt32(&calls) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: response_format"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`))
	}))
	defer server.Close()

	profile := types.ModelProfile{ID: "p", Name: "gpt-test", BaseURL: server.URL}
	provider := NewOpenAIProviderWithProfile("key", server.URL, profile.Name, profile)

	format := &types.StructuredOutput{Type: "json_schema", Schema: map[string]any{"reply": map[string]any{"type": "string"}}}
	resp, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil,
		types.ModelConfig{Model: profile.Name, ResponseFormat: format})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Text)
	assert.Equal(t, int32(2), atomic.LoadInt32(&calls))
}

// TestOpenAIStreamRequestsAndReadsUsage is the regression guard for usage being
// lost in stream mode: OpenAI sends usage in a final chunk whose choices array
// is empty, and the parser skipped empty-choice chunks before reading it. Every
// token counter, including the Agent budget that stops runaway turns, read zero.
func TestOpenAIStreamRequestsAndReadsUsage(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":""}],"usage":null}` + "\n\n",
			`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":4,"total_tokens":15}}` + "\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	profile := types.ModelProfile{ID: "p", Name: "gpt-test", BaseURL: server.URL, Stream: true}
	provider := NewOpenAIProviderWithProfile("key", server.URL, profile.Name, profile)

	resp, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil,
		types.ModelConfig{Model: profile.Name})
	require.NoError(t, err)
	assert.Equal(t, 11, resp.Usage.PromptTokens)
	assert.Equal(t, 4, resp.Usage.CompletionTokens)
	assert.Equal(t, 15, resp.Usage.TotalTokens)

	options, ok := request["stream_options"].(map[string]any)
	require.True(t, ok, "stream requests must ask for usage: %v", request["stream_options"])
	assert.Equal(t, true, options["include_usage"])
}
