package flow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// 记录跨轮留存。上一轮问题产生的结论如果进入本轮上下文，Team 会判定"事实
// 已确认"而跳过工作，直接复述旧答案（s13：问 cls 日志，回了 genie-agent 的
// 主分支）。
func TestSelectFlowRecordsDropsEarlierTurns(t *testing.T) {
	scopeSince := time.Now().UTC()
	stale := types.SharedRecord{
		Name: "RepoFacts", Summary: "genie-agent 主分支 main",
		CreatedAt: scopeSince.Add(-6 * time.Minute),
	}
	current := types.SharedRecord{
		Name: "OwnerReply", Summary: "workbuddy.host 流量已归零",
		CreatedAt: scopeSince.Add(time.Second),
	}
	spec := types.InputSpec{FlowRecords: []string{"RepoFacts", "OwnerReply"}}

	selected := selectFlowRecords([]types.SharedRecord{stale, current}, spec, scopeSince)

	require.Len(t, selected, 1)
	require.Equal(t, "OwnerReply", selected[0].Name)
}

func TestSelectFlowRecordsKeepsUnscopedRecordsWhenNoBoundary(t *testing.T) {
	record := types.SharedRecord{Name: "RepoFacts", CreatedAt: time.Now().UTC().Add(-time.Hour)}
	spec := types.InputSpec{FlowRecords: []string{"RepoFacts"}}

	require.Len(t, selectFlowRecords([]types.SharedRecord{record}, spec, time.Time{}), 1)
}

func TestIsProgressOnlyReply(t *testing.T) {
	for _, reply := range []string{
		"",
		"   ",
		"任务正在进行中：已派给执行侧，目前还没有返回结论。",
		"已转给执行侧核实，稍后给你结论。",
	} {
		require.True(t, isProgressOnlyReply(reply), "should be treated as progress-only: %q", reply)
	}
	for _, reply := range []string{
		"workbuddy.host 的流量在 14:20 后归零。",
		"genie-agent 的主分支是 main。",
	} {
		require.False(t, isProgressOnlyReply(reply), "real answer must not be replaced: %q", reply)
	}
}

// 被唤醒的回合只回"进行中"时，用户拿不到任何结果，且回合已经结束、不会再
// 有后续推送（s13）。兜底把执行侧的原话补上。
func TestRelayDeliveredResultsAppendsWhenReplyOnlyReportsProgress(t *testing.T) {
	messages := []types.MailboxMessage{
		{Kind: mailboxKindWork, Payload: map[string]any{"input": "查看cls日志"}},
		{Kind: "result", Payload: map[string]any{"team_result": map[string]any{"Reply": "14:20 之后流量归零"}}},
	}
	result := types.FlowTurnResult{Reply: "任务正在进行中，稍后给你结论。"}

	got := relayDeliveredResults(result, messages)

	require.Contains(t, got.Reply, "任务正在进行中")
	require.Contains(t, got.Reply, "执行侧返回")
	require.Contains(t, got.Reply, "14:20 之后流量归零")
}

func TestRelayDeliveredResultsKeepsRealAnswer(t *testing.T) {
	messages := []types.MailboxMessage{
		{Kind: "result", Payload: map[string]any{"team_result": map[string]any{"Reply": "14:20 之后流量归零"}}},
	}
	result := types.FlowTurnResult{Reply: "结论：workbuddy.host 流量已于 14:20 归零。"}

	require.Equal(t, result.Reply, relayDeliveredResults(result, messages).Reply)
}

func TestRelayDeliveredResultsFillsEmptyReply(t *testing.T) {
	messages := []types.MailboxMessage{
		{Kind: "result", Payload: map[string]any{"team_result": map[string]any{"Reply": "主分支是 main"}}},
	}

	got := relayDeliveredResults(types.FlowTurnResult{}, messages)

	require.Contains(t, got.Reply, "执行侧返回")
	require.Contains(t, got.Reply, "主分支是 main")
}

func TestMailboxScopeSinceUsesOldestMessage(t *testing.T) {
	early := time.Now().UTC().Add(-2 * time.Minute)
	late := time.Now().UTC()
	messages := []types.MailboxMessage{
		{CreatedAt: late},
		{CreatedAt: early},
	}

	require.Equal(t, early, mailboxScopeSince(messages))
	require.True(t, mailboxScopeSince(nil).IsZero())
}
