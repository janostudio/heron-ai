package model

import (
	"context"
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
