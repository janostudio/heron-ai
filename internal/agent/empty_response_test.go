package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// structuredReplyAgent is the minimal structured agent used by the empty-response
// tests: one required string field, so a non-JSON reply is a schema violation
// while an empty reply is a model-call failure. Keeping them distinct is the
// whole point of the tests below.
func structuredReplyAgent(loop types.LoopConfig) types.AgentConfig {
	return types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  loop,
		Structured: &types.StructuredOutput{
			Type: "json",
			Schema: map[string]any{
				"reply": map[string]any{"type": "string", "required": true},
			},
		},
	}
}

func newEmptyResponseLoop(model *mockModelProvider) *TurnLoop {
	return NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "查看仓库 heron-ai，目前主分支是什么"}}},
	)
}

// An empty reply must be retried with a hint that states what happened. The
// retry must never claim the reply was "not valid JSON" (the model emitted
// nothing), and it must not be byte-identical to the failed attempt: a provider
// that answered empty once answers empty again for the same prompt.
func TestTurnLoopRetriesEmptyModelResponseWithFactualHint(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "", FinishReason: "stop"},
			{Text: `{"reply":"master"}`, FinishReason: "stop"},
		},
	}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), structuredReplyAgent(types.LoopConfig{MaxRounds: 3}), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Equal(t, "master", result.Reply)
	require.Equal(t, 2, model.callCount, "the empty reply must trigger exactly one retry")

	require.Len(t, model.lastMessages, 2, "the retry carries the original message plus the hint")
	require.Equal(t, "查看仓库 heron-ai，目前主分支是什么", model.lastMessages[0].Content)
	require.Contains(t, strings.ToLower(model.lastMessages[1].Content), "previous response was empty")
	require.Contains(t, strings.ToLower(model.lastMessages[1].Content), "json object")
	for _, message := range model.lastMessages {
		require.NotContains(t, strings.ToLower(message.Content), "not valid json",
			"an empty reply must not be described to the model as invalid JSON")
	}
}

// When every attempt comes back empty, the turn fails with the real reason. It
// must not be reported as a completed turn with an empty reply, and the schema
// feedback must never reach the conversation.
func TestTurnLoopFailsWhenModelKeepsReturningEmptyResponse(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "", FinishReason: "stop"},
			{Text: "", FinishReason: "stop"},
			{Text: "", FinishReason: "stop"},
		},
	}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), structuredReplyAgent(types.LoopConfig{MaxRounds: 3, MaxModelRetries: 2}), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "empty response")
	require.Equal(t, 3, model.callCount, "one attempt plus two retries")

	for _, message := range model.lastMessages {
		require.NotContains(t, strings.ToLower(message.Content), "not valid json")
	}
}

// A negative max_model_retries keeps its documented meaning: no retry at all.
func TestTurnLoopEmptyModelResponseRespectsDisabledRetries(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{Text: "", FinishReason: "stop"}},
	}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), structuredReplyAgent(types.LoopConfig{MaxRounds: 3, MaxModelRetries: -1}), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "empty response")
	require.Equal(t, 1, model.callCount)
}

// The same hard failure applies to agents without a structured contract: an
// empty reply used to end such a turn as "completed" with an empty reply, which
// silently reported success for a call that produced nothing.
func TestTurnLoopFailsEmptyModelResponseWithoutStructuredOutput(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "", FinishReason: "stop"},
			{Text: "", FinishReason: "stop"},
			{Text: "", FinishReason: "stop"},
		},
	}
	loop := newEmptyResponseLoop(model)

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 3, MaxModelRetries: 2},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "empty response")
	require.Equal(t, 3, model.callCount)

	require.Contains(t, strings.ToLower(model.lastMessages[len(model.lastMessages)-1].Content), "empty")
	for _, message := range model.lastMessages {
		require.NotContains(t, strings.ToLower(message.Content), "not valid json")
	}
}
