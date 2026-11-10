package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// cancellingModel cancels the turn's context from inside Chat, which is the
// boundary being tested: the provider returns a perfectly good answer, but by
// the time the engine looks at it the user has already cancelled.
type cancellingModel struct {
	cancel context.CancelFunc
	text   string
}

func (m *cancellingModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.cancel()
	return &types.ChatResponse{Text: m.text, Usage: types.TokenUsage{TotalTokens: 1}}, nil
}

func (m *cancellingModel) ChatStream(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, nil
}

// TestLateResponseAfterCancelIsNotRecorded covers the window between "the model
// answered" and "the turn ended": a cancel landing there used to let the answer
// into the conversation. The next turn replays that answer as something the agent
// said, so a cancelled question resurfaces as the next reply.
func TestLateResponseAfterCancelIsNotRecorded(t *testing.T) {
	writer := &fakeSessionWriter{}
	ctx, cancel := context.WithCancel(context.Background())
	model := &cancellingModel{cancel: cancel, text: "被取消的答复"}

	loop := NewTurnLoop(model, &mockToolExecutor{}, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "问题"}}})
	loop.SetSessionWriter(writer)

	_, err := loop.Run(ctx, types.AgentConfig{Name: "a", Loop: types.LoopConfig{MaxRounds: 1}},
		types.AgentRequest{FlowSessionID: "fs-cancel", CallID: "call-1", AgentID: "assistant"})
	require.Error(t, err, "取消后到达的响应不能让回合成功结束")

	// Nothing from that response may reach the session log: model_response is
	// what a later turn replays into its context.
	for _, event := range writer.recorded() {
		require.NotEqual(t, types.EventAgentModelResponse, event.Type,
			"被取消的响应不应写出 model_response 事件")
		payload, _ := json.Marshal(event.Payload)
		require.NotContains(t, string(payload), "被取消的答复")
	}
}
