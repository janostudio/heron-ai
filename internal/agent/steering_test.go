package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestSteeringInjectsUserInputMidTurn is the point of the feature: a message the
// user sends while the turn is working joins the conversation before the next
// decision, not after the answer it may already contradict.
func TestSteeringInjectsUserInputMidTurn(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{
			Text: "reading",
			ToolCalls: []types.ToolCall{{
				ID: "call-1", Name: "Read", Arguments: map[string]any{"file": "a.go"},
			}},
		},
		{Text: `{"reply":"ok"}`, FinishReason: "stop"},
	}}
	polls := 0
	steering := func() []types.SteeringInput {
		polls++
		// Nothing on the first round: the message arrives while the turn is
		// already working. From then on it stays delivered, so returning it once
		// is what the real inbox does.
		if polls < 2 {
			return nil
		}
		return []types.SteeringInput{{
			MessageID: "mb_steer",
			Blocks:    []types.ContextBlock{{Kind: "input", Text: "换方向：只看 b.go"}},
		}}
	}

	loop := NewTurnLoop(model, &mockToolExecutor{}, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "看 a.go"}}})
	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "steered",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 4},
	}, types.AgentRequest{
		CallID:        "call",
		AgentID:       "steered",
		AgentTurnID:   "turn",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "看 a.go"}},
		Steering:      steering,
	})
	require.NoError(t, err)

	injected, toolResult := -1, -1
	for index, message := range model.lastMessages {
		if message.Role == "tool" && toolResult < 0 {
			toolResult = index
		}
		if message.Role == "user" && strings.Contains(message.Content, "只看 b.go") {
			injected = index
		}
	}
	require.GreaterOrEqual(t, injected, 0, "the steered message must reach the model as a user message")
	require.Greater(t, injected, toolResult,
		"it has to arrive after the work already done, not be replayed as the turn's opening input")
}

// TestSteeringWithoutSourceKeepsTheOldBehaviour: a turn that was started without
// an inbox (no Steering func) must run exactly as before.
func TestSteeringWithoutSourceKeepsTheOldBehaviour(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{Text: `{"reply":"ok"}`, FinishReason: "stop"},
	}}
	loop := NewTurnLoop(model, &mockToolExecutor{}, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}})
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "plain",
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		CallID:        "call",
		AgentID:       "plain",
		AgentTurnID:   "turn",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hi"}},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
}
