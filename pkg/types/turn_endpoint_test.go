package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// 终态是两维：同一个 stop reason 可以配不同的 awaiting，枚举不允许再把它们
// 压成一个值。
func TestTurnTerminalFieldsAreIndependent(t *testing.T) {
	require.NotEqual(t, AwaitingUser, AwaitingApproval)
	require.NotEmpty(t, StopEndTurn)
	for _, awaiting := range []TurnAwaiting{AwaitingUser, AwaitingTool, AwaitingApproval} {
		require.NotEmpty(t, awaiting)
	}
}

// stop reason 覆盖「正常收口 + 预算耗尽 + 取消 + 拒绝 + 故障」五类，新增一类
// 只需要加常量，不需要改状态机。
func TestTurnStopReasons(t *testing.T) {
	reasons := []TurnStopReason{
		StopEndTurn, StopMaxRounds, StopMaxToolCalls, StopMaxTokens,
		StopCancelled, StopRefusal, StopFailed,
	}
	seen := make(map[TurnStopReason]bool, len(reasons))
	for _, reason := range reasons {
		require.NotEmpty(t, string(reason))
		require.False(t, seen[reason], "duplicate stop reason %q", reason)
		seen[reason] = true
	}
}
