package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// approvalTurnLoop builds a loop whose model asks for two Tool calls in one
// response, only the second of which needs human approval. The first call must
// still produce a Tool message: without it the resumed request pairs two
// tool_calls with one tool reply.
func approvalTurnLoop(t *testing.T) (*TurnLoop, *mockModelProvider, *memoryCheckpointStore, types.AgentConfig, types.AgentRequest) {
	t.Helper()

	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "two calls",
				ToolCalls: []types.ToolCall{
					{ID: "call-read", Name: "Read", Arguments: map[string]any{"file": "README.md"}},
					{ID: "call-write", Name: "Write", Arguments: map[string]any{"file": ".env.local", "content": "x=1"}},
				},
			},
			{Text: "finished"},
		},
	}
	checkpoints := &memoryCheckpointStore{}
	loop := NewTurnLoop(model, &mockToolExecutor{}, nil, NewRouteParser(), &HITLGate{}, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "system", Content: "system prompt"}}},
	)
	loop.SetCheckpointStore(checkpoints)

	agent := types.AgentConfig{
		Name:  "tester",
		Model: types.ModelConfig{Model: "test-model"},
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write"}},
		HITL:  &types.HITLConfig{Enabled: true},
	}
	req := types.AgentRequest{
		FlowSessionID: "fs_approval_batch",
		CallID:        "call-1",
		AgentID:       "tester",
		AgentTurnID:   "turn-1",
	}
	return loop, model, checkpoints, agent, req
}

func TestApprovalBatchLeavesEveryToolCallWithAResult(t *testing.T) {
	loop, _, checkpoints, agent, req := approvalTurnLoop(t)

	result, err := loop.Run(context.Background(), agent, req)
	require.NoError(t, err)
	require.Equal(t, types.TurnWaitingApproval, result.Status)
	require.NotNil(t, result.Checkpoint)

	checkpoint := checkpoints.items[result.Checkpoint.ID]
	toolMessages := 0
	assistantCalls := 0
	for _, message := range checkpoint.Messages {
		if message.Role == "assistant" {
			assistantCalls += len(message.ToolCalls)
		}
		if message.Role == "tool" {
			toolMessages++
		}
	}
	require.Equal(t, 2, assistantCalls, "the assistant message carries both Tool calls")
	require.Equal(t, 1, toolMessages, "the call that did not need approval must already have a result")

	// The unexecuted call must say why, or the model reads it as a Tool bug.
	for _, message := range checkpoint.Messages {
		if message.Role == "tool" && message.ToolCallID == "call-read" {
			require.Contains(t, message.Content, "not executed")
		}
	}
}

func TestApprovalBatchResumePairsEveryToolCall(t *testing.T) {
	loop, model, _, agent, req := approvalTurnLoop(t)

	first, err := loop.Run(context.Background(), agent, req)
	require.NoError(t, err)
	require.Equal(t, types.TurnWaitingApproval, first.Status)
	require.NotNil(t, first.PendingApproval)

	req.ResumeCheckpointID = first.Checkpoint.ID
	req.ResumeApprovalID = first.PendingApproval.RequestID
	req.ResumeApproval = &types.HITLResponse{RequestID: first.PendingApproval.RequestID, Approved: true}

	resumed, err := loop.Run(context.Background(), agent, req)
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, resumed.Status)

	// lastMessages is the conversation the loop sent on the final round: it
	// must pair every tool_call with a tool message, which is exactly what a
	// strict provider validates before answering.
	pendingCalls := 0
	toolReplies := 0
	for _, message := range model.lastMessages {
		if message.Role == "assistant" {
			pendingCalls += len(message.ToolCalls)
		}
		if message.Role == "tool" {
			toolReplies++
		}
	}
	require.Equal(t, 2, pendingCalls)
	require.Equal(t, 2, toolReplies, "each tool_call needs its own tool message after resume")
}

// TestPolicyDenialReportsItsOwnReason covers the rejection message: a policy
// denial blanks the Tool name before the call reaches rejectToolCall, so the
// reason has to travel in the arguments. The model used to be told the call was
// "not allowed for this Agent" even when Bash had been refused for a restricted
// command, and that is the message it reasons from on the next round.
func TestPolicyDenialReportsItsOwnReason(t *testing.T) {
	call := types.ToolCall{
		ID:        "call-1",
		Name:      "",
		Arguments: map[string]any{"_policy_error": `Bash command contains restricted operation "rm -rf"`},
	}
	require.Contains(t, rejectionReason(call), "restricted operation")
	require.Contains(t, rejectionReason(types.ToolCall{Arguments: map[string]any{}}), "not allowed")
}
