package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestStructuredModelConfigBoundsOutputAndDisablesInheritedReasoning(t *testing.T) {
	agent := types.AgentConfig{
		Model: types.ModelConfig{
			Model: "hy3-ioa",
		},
		Structured: &types.StructuredOutput{
			Type:            "json",
			MaxOutputTokens: 2048,
		},
	}

	config := structuredModelConfig(agent)
	require.NotNil(t, config.MaxOutputTokens)
	require.Equal(t, 2048, *config.MaxOutputTokens)
	require.NotNil(t, config.Reasoning)
	require.Equal(t, "none", config.Reasoning.Type)
	require.NotNil(t, config.Temperature)
	require.Equal(t, 0.0, *config.Temperature)
	require.NotNil(t, config.TopP)
	require.Equal(t, 1.0, *config.TopP)
	require.Equal(t, agent.Structured, config.ResponseFormat)
}

func TestStructuredModelConfigUsesSafeDefaultOutputBudget(t *testing.T) {
	config := structuredModelConfig(types.AgentConfig{
		Model:      types.ModelConfig{Model: "hy3-ioa"},
		Structured: &types.StructuredOutput{Type: "json"},
	})
	require.NotNil(t, config.MaxOutputTokens)
	require.Equal(t, defaultStructuredOutputTokens, *config.MaxOutputTokens)
}

func TestTurnLoopStructuredOutputStopsOnProviderLength(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{
			Text:         `{"reply":"partial"`,
			FinishReason: "length",
		}},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "review"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 1},
		Structured: &types.StructuredOutput{
			Type:            "json",
			MaxOutputTokens: 1024,
			Schema: map[string]any{
				"reply": map[string]any{"type": "string", "required": true},
			},
		},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "structured output truncated")
	require.NotNil(t, model.lastConfig.MaxOutputTokens)
	require.Equal(t, 1024, *model.lastConfig.MaxOutputTokens)
	require.NotNil(t, model.lastConfig.Reasoning)
	require.Equal(t, "none", model.lastConfig.Reasoning.Type)
}

func TestTurnLoopRetriesTruncatedStructuredResponse(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: `{"reply":"partial"`, FinishReason: "length"},
			{Text: `{"reply":"recovered"}`, FinishReason: "stop"},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "review"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 2},
		Structured: &types.StructuredOutput{
			Type: "json",
			Schema: map[string]any{
				"reply": map[string]any{"type": "string", "required": true},
			},
		},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Equal(t, "recovered", result.Reply)
	require.Equal(t, 2, model.callCount)
}

func TestTurnLoopRetriesOneInvalidStructuredResponse(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{Text: "I cannot provide that as JSON."},
			{Text: `{"reply":"ok"}`},
		},
	}
	loop := NewTurnLoop(
		model,
		&mockToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "review"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Model: types.ModelConfig{Model: "hy3-ioa"},
		Loop:  types.LoopConfig{MaxRounds: 2},
		Structured: &types.StructuredOutput{
			Type: "json",
			Schema: map[string]any{
				"reply": map[string]any{"type": "string", "required": true},
			},
		},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Equal(t, "ok", result.Reply)
	require.Equal(t, 2, model.callCount)
}

// Progress consumers show text to a user, so the JSON envelope a structured
// Agent emits has to be reduced to its readable field.
func TestStructuredReplyExtractsReplyField(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "envelope with routing fields",
			raw:  `{"reply":"主分支是 master","repo":"heron-ai","next":{"action":"activate"}}`,
			want: "主分支是 master",
		},
		{name: "reply only", raw: `{"reply":"ok"}`, want: "ok"},
		{name: "trims surrounding space", raw: `{"reply":"  ok  "}`, want: "ok"},
		{name: "no reply field", raw: `{"next":{"action":"proceed"}}`, want: ""},
		{name: "reply is not a string", raw: `{"reply":{"text":"nested"}}`, want: ""},
		{name: "empty reply", raw: `{"reply":""}`, want: ""},
		{name: "not json", raw: "已经核实完毕，主分支是 master", want: ""},
		{name: "json array", raw: `["ok"]`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, structuredReply(tc.raw))
		})
	}
}
