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

// TestBatchRejectionKeepsItsOwnReason covers the reason each unanswered call
// reports. Two things can keep a call from running in the same batch: its own
// policy denial, or another call in the batch waiting for approval. Each has to
// report its own cause — a policy-denied Bash told "another call is waiting for
// approval" is a message the model cannot act on.
func TestBatchRejectionKeepsItsOwnReason(t *testing.T) {
	calls := []types.ToolCall{
		{ID: "call-1", Name: "Bash", Arguments: map[string]any{"command": "rm -rf /"}},
		{ID: "call-2", Name: "REPL", Arguments: map[string]any{"code": "return 1"}},
		{ID: "call-3", Name: "Write", Arguments: map[string]any{"file": "a.txt"}},
	}
	rejected := map[int]string{0: `Bash command contains restricted operation "rm -rf"`}

	results := approvalResults(calls, rejected, 1, types.AgentRequest{CallID: "c"}, "script needs approval")
	require.Len(t, results, 3)

	require.Contains(t, results[0].Error, "restricted operation", "被策略拒绝的调用要保留自己的理由")
	require.Equal(t, "tool approval required", results[1].Error)
	require.NotNil(t, results[1].PendingApproval)
	require.Contains(t, results[2].Error, "waiting for human approval", "同批其他调用说明是被审批挡住")
}

// TestMissingToolCallIDIsSharedByTranscriptAndResult covers the id that used to
// exist only in the events: executeToolCalls synthesised one for pairing while
// the assistant message kept the empty id, so the transcript could not pair the
// tool message back to its call.
func TestMissingToolCallIDIsSharedByTranscriptAndResult(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{
			Text:      "one call without an id",
			ToolCalls: []types.ToolCall{{Name: "Read", Arguments: map[string]any{"file": "a.txt"}}},
		},
		{Text: "done"},
	}}
	loop := NewTurnLoop(model, &mockToolExecutor{}, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}},
	)
	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "tester",
		Model: types.ModelConfig{Model: "test"},
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
	}, types.AgentRequest{CallID: "c1", AgentID: "tester", FlowSessionID: "fs_id"})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Status)

	// The transcript the loop sent on the second round must pair the call and
	// its result by the same non-empty id.
	var assistantID, toolID string
	for _, message := range model.lastMessages {
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			assistantID = message.ToolCalls[0].ID
		}
		if message.Role == "tool" {
			toolID = message.ToolCallID
		}
	}
	require.NotEmpty(t, assistantID, "the assistant message must carry the synthesised id")
	require.Equal(t, assistantID, toolID)
}
