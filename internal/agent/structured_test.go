package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Two-phase default: the tool loop runs with the model's own sampling settings
// and no provider-level JSON mode, because response_format suppresses
// tool_calls on some gateways — a routing agent must be able to call Team
// before it emits its envelope.
func TestStructuredModelConfigKeepsToolLoopUnconstrained(t *testing.T) {
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
	require.Nil(t, config.ResponseFormat, "loop rounds must not carry provider JSON mode")
	require.Nil(t, config.Temperature, "sampling overrides belong to the enforce path only")
	require.Nil(t, config.TopP)
	require.Nil(t, config.Reasoning)
}

// Opt-in for gateways that cannot produce JSON without response_format: the
// single-phase behaviour (provider JSON mode + sampling overrides) is restored.
func TestStructuredModelConfigEnforceProviderFormatRestoresOverrides(t *testing.T) {
	agent := types.AgentConfig{
		Model: types.ModelConfig{
			Model: "hy3-ioa",
		},
		Structured: &types.StructuredOutput{
			Type:                  "json",
			MaxOutputTokens:       2048,
			EnforceProviderFormat: true,
		},
	}

	config := structuredModelConfig(agent)
	require.NotNil(t, config.MaxOutputTokens)
	require.Equal(t, 2048, *config.MaxOutputTokens)
	require.Equal(t, agent.Structured, config.ResponseFormat)
	require.NotNil(t, config.Reasoning)
	require.Equal(t, "none", config.Reasoning.Type)
	require.NotNil(t, config.Temperature)
	require.Equal(t, 0.0, *config.Temperature)
	require.NotNil(t, config.TopP)
	require.Equal(t, 1.0, *config.TopP)
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
