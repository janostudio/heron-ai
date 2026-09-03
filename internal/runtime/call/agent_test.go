package call

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/pkg/types"
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

type spawnCollectingRunner struct {
	request    types.AgentRequest
	collector  *agentstore.RecordCollector
	collectSet bool
}

func (r *spawnCollectingRunner) Run(
	ctx context.Context,
	_ types.AgentConfig,
	req types.AgentRequest,
) (*types.AgentResult, error) {
	r.request = req
	// Simulate the Spawn tool publishing a downstream record mid-run.
	r.collector = agentstore.RecordCollectorFromContext(ctx)
	r.collectSet = r.collector != nil
	if r.collector != nil && r.collector.Enabled() {
		r.collector.Add("spawn_result", "child done", map[string]any{"key": "e-1"})
	}
	return &types.AgentResult{
		Status: types.TurnCompleted,
		Reply:  "ok",
		Next:   &types.Route{Action: types.NextProceed},
	}, nil
}

func agentExecutorRequest(recordName string) types.CallRequest {
	return types.CallRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "team-1"},
		Call: types.Call{
			ID:             "answer",
			Type:           types.CallAgent,
			AgentID:        "assistant",
			Responsibility: "answer",
			Output:         types.OutputSpec{Record: recordName},
		},
		AgentDefinition: &types.AgentConfig{Name: "assistant"},
		CallTurnID:      "ct-1",
	}
}

func TestAgentExecutorDrainsSpawnRecords(t *testing.T) {
	runner := &spawnCollectingRunner{}
	executor := NewAgentExecutor(runner)

	result, err := executor.Execute(context.Background(), agentExecutorRequest("FixReport"))
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)

	// The runner observed the collector injected by the executor.
	require.True(t, runner.collectSet)
	require.True(t, runner.collector.Enabled())

	// The call publishes its own agent_result record plus the spawned
	// children's records under the same name, so downstream consumers
	// aggregate them through the existing same-name mechanism.
	require.Len(t, result.Records, 2)
	assert.Equal(t, "agent_result", result.Records[0].Kind)
	assert.Equal(t, "FixReport", result.Records[0].Name)
	assert.Equal(t, "spawn_result", result.Records[1].Kind)
	assert.Equal(t, "FixReport", result.Records[1].Name)
	assert.Equal(t, "answer", result.Records[1].Producer.CallID)
	assert.Equal(t, "fs-1", result.Records[1].Producer.FlowSessionID)
	assert.Equal(t, "ft-1", result.Records[1].Producer.FlowTurnID)
	assert.Equal(t, "e-1", result.Records[1].Data["key"])
}

func TestAgentExecutorCollectorDisabledWithoutOutputRecord(t *testing.T) {
	runner := &spawnCollectingRunner{}
	executor := NewAgentExecutor(runner)

	result, err := executor.Execute(context.Background(), agentExecutorRequest(""))
	require.NoError(t, err)
	require.True(t, runner.collectSet)
	assert.False(t, runner.collector.Enabled())
	assert.Empty(t, result.Records)
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
