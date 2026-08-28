package call

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/require"
)

type captureAgentRunner struct {
	request types.AgentRequest
}

func (r *captureAgentRunner) Run(
	_ context.Context,
	_ types.AgentConfig,
	req types.AgentRequest,
) (*types.AgentResult, error) {
	r.request = req
	return &types.AgentResult{
		Status: types.TurnCompleted,
		Reply:  "ok",
		Next:   &types.Route{Action: types.NextProceed},
	}, nil
}

func TestAgentExecutorPassesStructuredContextBlocks(t *testing.T) {
	runner := &captureAgentRunner{}
	executor := NewAgentExecutor(runner)
	blocks := []types.ContextBlock{{
		Kind:         "input",
		Text:         "inspect project",
		Source:       "user",
		Stability:    "dynamic",
		Priority:     80,
		Compressible: true,
	}}

	result, err := executor.Execute(context.Background(), types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call: types.Call{
			ID:             "answer",
			Type:           types.CallAgent,
			AgentID:        "assistant",
			Responsibility: "answer",
		},
		AgentDefinition: &types.AgentConfig{Name: "assistant"},
		ContextBlocks:   blocks,
	})

	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)
	require.Equal(t, blocks, runner.request.ContextBlocks)
}
