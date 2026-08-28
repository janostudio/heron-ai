package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/require"
)

func TestDefaultToolPolicyRequiresApprovalForDangerousBash(t *testing.T) {
	policy := NewDefaultToolPolicy()
	decision, reason, err := policy.Check(context.Background(), ToolPolicyRequest{
		Agent: types.AgentConfig{HITL: &types.HITLConfig{Enabled: true}},
		Call: types.ToolCall{
			Name:      "Bash",
			Arguments: map[string]any{"command": "rm -rf project/tmp"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, ToolRequireApproval, decision)
	require.Contains(t, reason, "restricted")
}

func TestDefaultContextPolicyNormalizesBlocks(t *testing.T) {
	policy := NewDefaultContextPolicy()
	blocks := policy.Build(types.AgentRequest{
		ContextBlocks: []types.ContextBlock{
			{Kind: "input", Source: "user", Text: "first", Priority: 1},
			{Kind: "input", Source: "user", Text: "duplicate", Priority: 2},
			{Kind: "knowledge", Source: "kb", Text: "guide", Priority: 3},
		},
	})
	require.Len(t, blocks, 2)
	require.Equal(t, "knowledge", blocks[0].Kind)
	require.Equal(t, "input", blocks[1].Kind)
}

type approvalToolExecutor struct {
	executed int
}

func (e *approvalToolExecutor) Execute(_ context.Context, _ string, _ map[string]any) (*types.ToolResult, error) {
	e.executed++
	return &types.ToolResult{Success: true, Content: "approved execution"}, nil
}

func (e *approvalToolExecutor) NeedsApproval(string, map[string]any) (bool, error) {
	return true, nil
}

func TestTurnLoopToolApprovalCreatesDurableWaitingCheckpointAndResumes(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{ToolCalls: []types.ToolCall{{
				ID: "danger-1", Name: "Bash",
				Arguments: map[string]any{"command": "echo protected"},
			}}},
			{Text: "completed"},
		},
	}
	executor := &approvalToolExecutor{}
	checkpoints := &memoryCheckpointStore{}
	loop := NewTurnLoop(
		model, executor, nil, NewRouteParser(), NewHITLGate(0),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{
			{Role: "system", Content: "stable"},
			{Role: "user", Content: "run"},
		}},
	)
	loop.SetCheckpointStore(checkpoints)

	agentConfig := types.AgentConfig{
		Name:  "approval-agent",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		HITL:  &types.HITLConfig{Enabled: true},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}
	first, err := loop.Run(context.Background(), agentConfig, types.AgentRequest{
		AgentID: "approval-agent", AgentTurnID: "turn-approval",
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnWaitingApproval, first.Status)
	require.NotNil(t, first.PendingApproval)
	require.NotNil(t, first.Checkpoint)
	require.Equal(t, 0, executor.executed)

	resumed, err := loop.Run(context.Background(), agentConfig, types.AgentRequest{
		AgentID:            "approval-agent",
		AgentTurnID:        "turn-approval",
		ResumeCheckpointID: first.Checkpoint.ID,
		ResumeApprovalID:   first.PendingApproval.RequestID,
		ResumeApproval: &types.HITLResponse{
			RequestID: first.PendingApproval.RequestID,
			Approved:  true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, resumed.Status)
	require.Equal(t, 1, executor.executed)
	require.Equal(t, "completed", resumed.Reply)
}

func TestTurnLoopToolApprovalDeniedFailsWithoutExecuting(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{{
			ToolCalls: []types.ToolCall{{
				ID: "danger-denied", Name: "Bash",
				Arguments: map[string]any{"command": "echo protected"},
			}},
		}},
	}
	executor := &approvalToolExecutor{}
	checkpoints := &memoryCheckpointStore{}
	loop := NewTurnLoop(
		model, executor, nil, NewRouteParser(), NewHITLGate(0),
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "run"}}},
	)
	loop.SetCheckpointStore(checkpoints)

	first, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "approval-denied",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		HITL:  &types.HITLConfig{Enabled: true},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{AgentID: "approval-denied", AgentTurnID: "turn-denied"})
	require.NoError(t, err)
	require.Equal(t, types.TurnWaitingApproval, first.Status)

	resumed, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "approval-denied",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{Builtin: []string{"Bash"}},
		HITL:  &types.HITLConfig{Enabled: true},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		AgentID:            "approval-denied",
		AgentTurnID:        "turn-denied",
		ResumeCheckpointID: first.Checkpoint.ID,
		ResumeApprovalID:   first.PendingApproval.RequestID,
		ResumeApproval: &types.HITLResponse{
			RequestID: first.PendingApproval.RequestID,
			Approved:  false,
			Reason:    "operator rejected",
		},
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, resumed.Status)
	require.Contains(t, resumed.Error, "approval denied")
	require.Equal(t, 0, executor.executed)
}

func TestTurnLoopCheckpointCompatibilityRejectsFutureVersion(t *testing.T) {
	checkpoints := &memoryCheckpointStore{}
	checkpoints.items = map[string]types.AgentCheckpoint{
		"future": {
			ID:      "future",
			Version: types.CurrentAgentCheckpointVersion + 1,
			Status:  types.TurnWaitingInput,
		},
	}
	loop := NewTurnLoop(
		&mockModelProvider{}, &mockToolExecutor{}, nil, NewRouteParser(), nil,
		NewHookExecutor(), &mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "resume"}}},
	)
	loop.SetCheckpointStore(checkpoints)
	_, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "future-checkpoint", Model: types.ModelConfig{Model: "test"},
	}, types.AgentRequest{
		AgentID: "future-checkpoint", ResumeCheckpointID: "future",
	})
	require.ErrorContains(t, err, "unsupported version")
}

func TestTurnLoopRetriesTransientModelError(t *testing.T) {
	model := &mockModelProvider{
		err:       errors.New("temporary service unavailable"),
		responses: []types.ChatResponse{{Text: "recovered"}},
	}
	loop := NewTurnLoop(
		model, &mockToolExecutor{}, nil, NewRouteParser(), nil,
		NewHookExecutor(), &mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "retry"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Loop: types.LoopConfig{MaxRounds: 1, MaxModelRetries: 1},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, "recovered", result.Reply)
	require.Equal(t, 2, model.attempts)
	require.Len(t, result.Requests, 2)
}

func TestTurnLoopCompletionPolicyRequiresToolEvidence(t *testing.T) {
	loop := NewTurnLoop(
		&mockModelProvider{responses: []types.ChatResponse{{Text: "done"}}},
		&mockToolExecutor{}, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "task"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Loop:       types.LoopConfig{MaxRounds: 1},
		Completion: types.CompletionConfig{RequireTool: true},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "Tool call is required")
}

func TestTurnLoopStuckDetectionStopsRepeatedToolCalls(t *testing.T) {
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{ToolCalls: []types.ToolCall{{ID: "r1", Name: "Read", Arguments: map[string]any{"file": "a"}}}},
			{ToolCalls: []types.ToolCall{{ID: "r2", Name: "Read", Arguments: map[string]any{"file": "a"}}}},
		},
	}
	loop := NewTurnLoop(
		model, &mockToolExecutor{}, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "task"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop: types.LoopConfig{
			MaxRounds:        3,
			MaxSameToolCalls: 2,
			StuckAction:      "fail",
		},
	}, types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status)
	require.Contains(t, result.Error, "stuck")
}

func TestIsContextLimitErrorDoesNotClassifyUnrelatedError(t *testing.T) {
	require.False(t, isContextLimitError(errors.New("network unavailable")))
}
