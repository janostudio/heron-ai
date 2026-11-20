package flow

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// 「等谁」由会话状态决定，「怎么结束」由错误决定：两者独立取值，不再挤在
// 一个枚举里二选一。
func TestTurnEndpoint(t *testing.T) {
	cases := []struct {
		name     string
		status   types.SessionStatus
		err      error
		reason   types.TurnStopReason
		awaiting types.TurnAwaiting
	}{
		{"idle waits for user", types.SessionIdle, nil, types.StopEndTurn, types.AwaitingUser},
		{"tool", types.SessionWaitingTool, nil, types.StopEndTurn, types.AwaitingTool},
		{"approval", types.SessionWaitingApproval, nil, types.StopEndTurn, types.AwaitingApproval},
		{"cancelled", types.SessionCancelled, errors.New("turn cancelled"), types.StopCancelled, ""},
		{"failed", types.SessionFailed, errors.New("model exploded"), types.StopFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reason, awaiting := turnEndpoint(tc.status, tc.err)
			require.Equal(t, tc.reason, reason)
			require.Equal(t, tc.awaiting, awaiting)
		})
	}
}

// 失败要按原因细分：预算停与执行失败对调用方意味着不同动作。
func TestStopReasonFromError(t *testing.T) {
	cases := []struct {
		err  error
		want types.TurnStopReason
	}{
		{errors.New("agent budget exceeded: max_model_rounds=30"), types.StopMaxRounds},
		{errors.New("agent loop reached the round limit (14) without a final answer"), types.StopMaxRounds},
		{errors.New("agent budget exceeded: max_tool_calls=100"), types.StopMaxToolCalls},
		{errors.New("agent budget exceeded: max_input_tokens=100000"), types.StopMaxTokens},
		{errors.New("context limit exceeded after compaction"), types.StopMaxTokens},
		{errors.New("provider refusal: blocked by safety policy"), types.StopRefusal},
		{errors.New("dial tcp: connection refused"), types.StopFailed},
		{nil, types.StopFailed},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, stopReasonFromError(tc.err), "err=%v", tc.err)
	}
}
