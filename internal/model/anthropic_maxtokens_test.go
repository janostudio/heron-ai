package model

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestAnthropicMaxTokensDowngrade(t *testing.T) {
	req := anthropicRequest{MaxTokens: 32768}
	tooLarge := errors.New(`anthropic error 400: max_tokens: 32768 > 8192, which is the maximum allowed number of output tokens for claude-3-5-sonnet-20241022`)

	downgraded, ok := anthropicMaxTokensDowngrade(req, tooLarge)
	require.True(t, ok)
	assert.Equal(t, 16384, downgraded.MaxTokens)

	// Unrelated 400s keep their budget.
	_, ok = anthropicMaxTokensDowngrade(req, errors.New("invalid api key"))
	assert.False(t, ok)

	// Already at the floor: nothing left to try.
	_, ok = anthropicMaxTokensDowngrade(anthropicRequest{MaxTokens: anthropicMaxTokensFloor}, tooLarge)
	assert.False(t, ok)
}

// TestAnthropicRetriesWithSmallerMaxTokens is the end-to-end guard: the default
// used to be 32768, which claude-3-5 rejects with a 400 the classifier marks
// non-retryable, so every turn against that model failed outright.
func TestAnthropicRetriesWithSmallerMaxTokens(t *testing.T) {
	var attempts []int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MaxTokens int `json:"max_tokens"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		attempts = append(attempts, body.MaxTokens)
		if body.MaxTokens > 8192 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 16384 > 8192, which is the maximum allowed number of output tokens for claude-3-5-sonnet"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	profile := types.ModelProfile{ID: "claude", Name: "claude-3-5-sonnet", BaseURL: server.URL}
	provider := NewAnthropicProviderWithProfile("key", server.URL, profile.Name, profile)

	resp, err := provider.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil,
		types.ModelConfig{Model: profile.Name, MaxOutputTokens: ptr(16384)})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Text)
	require.Len(t, attempts, 2, "the oversized request must be retried once with a smaller budget")
	assert.Equal(t, 16384, attempts[0])
	assert.Equal(t, 8192, attempts[1])
}

func ptr(value int) *int { return &value }
