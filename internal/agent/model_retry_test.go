package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// The provider layer already knows a 500 is a server error; the agent loop must
// use that classification instead of matching phrases in the message. Before
// this change a 500 (missing from the old phrase list) was treated as fatal.
func TestIsRetryableModelErrorUsesProviderClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "500 server error", err: &types.ProviderError{Kind: types.KindServerError, StatusCode: 500}, want: true},
		{name: "529 overloaded", err: &types.ProviderError{Kind: types.KindServerError, StatusCode: 529}, want: true},
		{name: "429 rate limit", err: &types.ProviderError{Kind: types.KindRateLimit, StatusCode: 429}, want: true},
		{name: "timeout", err: &types.ProviderError{Kind: types.KindTimeout}, want: true},
		{name: "network", err: &types.ProviderError{Kind: types.KindNetwork}, want: true},
		{name: "400 bad request", err: &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 400}, want: false},
		{name: "401 auth", err: &types.ProviderError{Kind: types.KindAuth, StatusCode: 401}, want: false},
		{name: "wrapped 500", err: fmt.Errorf("llm chat: %w", &types.ProviderError{Kind: types.KindServerError, StatusCode: 500}), want: true},
		{name: "plain text fallback", err: errors.New("upstream 502 bad gateway"), want: true},
		{name: "plain text not retryable", err: errors.New("invalid api key"), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isRetryableModelError(tc.err))
		})
	}
}

func TestModelRetryDelayHonoursProviderRetryAfter(t *testing.T) {
	rateLimited := &types.ProviderError{Kind: types.KindRateLimit, StatusCode: 429, RetryAfter: 3 * time.Second}
	require.Equal(t, 3*time.Second, modelRetryDelay(1, rateLimited))

	capped := &types.ProviderError{Kind: types.KindRateLimit, StatusCode: 429, RetryAfter: time.Hour}
	require.Equal(t, modelRetryAfterCap, modelRetryDelay(1, capped), "a bogus header must not park the turn")
}

func TestModelRetryDelayBacksOffWithJitter(t *testing.T) {
	for _, attempt := range []int{1, 2, 3, 50} {
		base := modelRetryBaseDelay << uint(minInt(attempt-1, modelRetryMaxBackoffShift))
		if base > modelRetryMaxDelay {
			base = modelRetryMaxDelay
		}
		spread := int64(base) * modelRetryJitterPermille / 1000
		for i := 0; i < 20; i++ {
			got := int64(modelRetryDelay(attempt, nil))
			require.GreaterOrEqual(t, got, int64(base)-spread, "attempt %d", attempt)
			require.LessOrEqual(t, got, int64(base)+spread, "attempt %d", attempt)
		}
	}
}

// A retryable provider failure (here a 500) must be retried and then succeed,
// while a non-retryable one (400) must fail on the first attempt.
func TestTurnLoopRetriesProviderServerError(t *testing.T) {
	model := &mockModelProvider{err: &types.ProviderError{Kind: types.KindServerError, StatusCode: 500}}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 2, MaxModelRetries: 1},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Equal(t, 2, model.attempts)
}

func TestTurnLoopDoesNotRetryProviderBadRequest(t *testing.T) {
	model := &mockModelProvider{err: &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 400}}
	loop := newEmptyResponseLoop(model)

	_, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 2, MaxModelRetries: 2},
	}, types.AgentRequest{})
	require.Error(t, err)
	require.Equal(t, 1, model.attempts, "a 400 is a client bug; retrying it wastes the turn budget")
}

// Compaction must only trigger for size problems. A 429 that mentions "token
// limit" is still a rate limit: compacting there discards history without fixing
// the cause, so the status gate matters as much as the phrase list.
func TestIsContextLimitErrorUsesStatusAndMessage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "openai 400 context_length_exceeded",
			err:  &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 400, Message: "context_length_exceeded: maximum context length is 128000 tokens"},
			want: true,
		},
		{
			name: "anthropic 400 prompt is too long",
			err:  &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 400, Message: "prompt is too long: 250000 tokens"},
			want: true,
		},
		{
			name: "413 request too large",
			err:  &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 413, Message: "request too large"},
			want: true,
		},
		{
			name: "plain text too many tokens",
			err:  errors.New("too many tokens in the request"),
			want: true,
		},
		{
			name: "429 mentioning token limit is a rate limit",
			err:  &types.ProviderError{Kind: types.KindRateLimit, StatusCode: 429, Message: "token limit per minute exceeded"},
			want: false,
		},
		{
			name: "500 unrelated",
			err:  &types.ProviderError{Kind: types.KindServerError, StatusCode: 500, Message: "upstream failure"},
			want: false,
		},
		{
			name: "400 unrelated parameter error",
			err:  &types.ProviderError{Kind: types.KindBadRequest, StatusCode: 400, Message: "invalid parameter: temperature"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, isContextLimitError(tc.err))
		})
	}
}

// The persisted request summary must carry the provider stop reason. Without
// it an empty reply, a tool call, and a plain answer produce identical
// ModelRequestStats entries, so team.jsonl cannot be used to tell them apart
// after the debug log has rotated away.
func TestTurnLoopRecordsFinishReasonInRequestStats(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{Text: `{"reply":"ok"}`, FinishReason: "stop"}},
	}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), structuredReplyAgent(types.LoopConfig{MaxRounds: 3}), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Len(t, result.Requests, 1)
	require.Equal(t, "stop", result.Requests[0].FinishReason)
}
