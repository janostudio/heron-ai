package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// 被唤醒回合的窗口取"本次任务链起点"（最近一次用户回合），而不是邮箱消息时间。
// 邮箱时间只比它通告的记录晚一两毫秒，用它做边界会把刚送达的结果挡在窗口外。
func TestTaskChainSinceUsesLatestUserTurn(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)
	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)

	userTurnAt := time.Now().UTC().Add(-2 * time.Minute)
	for _, turn := range []types.FlowTurn{
		{ID: "ft-user-1", FlowSessionID: session.ID, Origin: turnOriginUser, StartedAt: userTurnAt.Add(-10 * time.Minute)},
		{ID: "ft-mailbox", FlowSessionID: session.ID, Origin: turnOriginMailbox, StartedAt: userTurnAt.Add(time.Minute)},
		{ID: "ft-user-2", FlowSessionID: session.ID, Origin: turnOriginUser, StartedAt: userTurnAt},
	} {
		require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowTurnStarted,
				FlowSessionID: session.ID,
				FlowTurnID:    turn.ID,
			},
			Payload: map[string]any{"turn": turn},
		}))
	}

	fallback := time.Now().UTC()
	require.True(t, runtime.taskChainSince(ctx, session.ID, fallback).Equal(userTurnAt),
		"窗口必须取最近一次用户回合的开始时间，忽略更晚的后台回合")

	// 没有任何用户回合时退回调用方给的边界，而不是放开全部历史。
	empty := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)
	require.True(t, runtime.taskChainSince(ctx, empty.ID, fallback).Equal(fallback))
}

// s15 回归：OwnerReply 在 05:47:17.234 发布，结果消息在 05:47:17.235 入队。
// 用邮箱时间做边界时，这条记录被判成"更早问题的答案"而丢弃，协调方只看到用户
// 原话，于是重新派发并回了一句"已转给执行侧…稍后给结论"。
func TestRecordPublishedJustBeforeMailboxStaysInScope(t *testing.T) {
	userTurnAt := time.Date(2026, 10, 23, 5, 45, 15, 0, time.UTC)
	recordAt := time.Date(2026, 10, 23, 5, 47, 17, 234_000_000, time.UTC)
	mailboxAt := time.Date(2026, 10, 23, 5, 47, 17, 235_000_000, time.UTC)

	records := []types.SharedRecord{{
		RecordID:  "rec-owner",
		Name:      "OwnerReply",
		CreatedAt: recordAt,
	}}
	spec := types.InputSpec{FlowRecords: []string{"OwnerReply"}}

	require.Empty(t, selectFlowRecords(records, spec, mailboxAt),
		"邮箱时间会把刚送达的结果挡在窗口外，这正是回归的原因")
	require.Len(t, selectFlowRecords(records, spec, userTurnAt), 1,
		"任务链起点的窗口必须包含本次任务产生的记录")

	// 上一轮问题留下的记录仍然排除：跨轮隔离不能因此失效。
	older := []types.SharedRecord{{
		RecordID:  "rec-old",
		Name:      "RepoFacts",
		CreatedAt: userTurnAt.Add(-3 * time.Minute),
	}}
	specWithFacts := types.InputSpec{FlowRecords: []string{"OwnerReply", "RepoFacts"}}
	selected := selectFlowRecords(older, specWithFacts, userTurnAt)
	require.Empty(t, selected, "上一轮产生的记录不得进入本轮窗口")
}
