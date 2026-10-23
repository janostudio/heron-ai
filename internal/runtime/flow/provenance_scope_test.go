package flow

import (
	"strings"
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

// 结果是一等交付物：引擎不看协调方的回复写了什么，只把投递结果原样交给用户。
func TestUserDeliveriesCarryProducedText(t *testing.T) {
	messages := []types.MailboxMessage{
		{MessageID: "mb-1", Kind: mailboxKindWork, Payload: map[string]any{"input": "查看cls日志"}},
		{MessageID: "mb-2", Kind: "result", To: types.MailboxAddress{TeamID: "default"},
			Payload: map[string]any{"team_result": map[string]any{"Reply": "缺 CLS 索引/地域/时间范围"}}},
	}

	deliveries := userDeliveries(messages, "default")

	require.Len(t, deliveries, 1)
	require.Equal(t, "mb-2", deliveries[0].MessageID)
	require.Contains(t, deliveries[0].Text, "缺 CLS 索引")
}

// s16：diagnose 把 11832 字的 @decision 协议交给 owner，中间层之间的交接不是
// 给用户的答复，不能原样透传。判据只看收件人是不是入口团队。
func TestUserDeliveriesSkipIntermediateHandOver(t *testing.T) {
	messages := []types.MailboxMessage{
		{MessageID: "mb-4", Kind: "result", To: types.MailboxAddress{TeamID: "owner"},
			Payload: map[string]any{"team_result": map[string]any{"Reply": "@decision\nstatus: needs_input"}}},
	}

	require.Empty(t, userDeliveries(messages, "default"),
		"投递给中间团队的结果由该团队转述，不能直达用户")
	require.Len(t, userDeliveries(messages, "owner"), 1,
		"收件人就是入口团队时仍然交付")
}

// 超长结果截断并标注，避免一条消息淹没整个会话记录。
func TestUserDeliveriesTruncateLongResult(t *testing.T) {
	long := strings.Repeat("证据", maxDeliveryChars)
	messages := []types.MailboxMessage{
		{MessageID: "mb-5", Kind: "result", To: types.MailboxAddress{TeamID: "default"},
			Payload: map[string]any{"team_result": map[string]any{"Reply": long}}},
	}

	deliveries := userDeliveries(messages, "default")
	require.Len(t, deliveries, 1)
	require.LessOrEqual(t, len([]rune(deliveries[0].Text)), maxDeliveryChars+64)
	require.Contains(t, deliveries[0].Text, "已截断")
}

func TestUserDeliveriesSkipEmptyResult(t *testing.T) {
	messages := []types.MailboxMessage{
		{MessageID: "mb-3", Kind: "result", Payload: map[string]any{}},
	}
	require.Empty(t, userDeliveries(messages, "default"))
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
