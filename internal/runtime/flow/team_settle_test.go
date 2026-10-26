package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// 等待收口时读的是被调用方自己的结论，不能把调用方的转述当成答案：两者都在
// 事件流里，TeamID 不同。
func TestLastTeamReplyPicksCalleeConclusion(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-reply", types.SessionWaitingInput)
	ctx := context.Background()

	for _, event := range []struct{ team, reply string }{
		{"default", "已转给执行侧核实…"},
		{"owner", "已把这一步派给 repo-info…"},
		{"owner", "heron-connect 的主分支是 main"},
	} {
		require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowTurnWaitingInput,
				FlowSessionID: session.ID,
				FlowTurnID:    "ft-" + event.team,
				TeamID:        event.team,
			},
			Payload: map[string]any{"reply": event.reply},
		}))
	}

	reply, ok := rt.lastTeamReply(ctx, session.ID, "owner")
	require.True(t, ok)
	require.Equal(t, "heron-connect 的主分支是 main", reply,
		"要取被调用方最新的结论，而不是它早期的派发回执")
}

// 被调用方把活交给下一层后，调用方要等它收口，拿到的必须是它最终的结论。
func TestAwaitTeamSettlementReturnsAfterPendingClears(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	store := &memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "t1",
		FlowSessionID: "fs-settle",
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskRunning,
		Arguments:     map[string]any{"caller_team": "owner"},
	}}}
	rt.SetTaskStore(store)
	session := seedMailboxSession(t, rt, "fs-settle", types.SessionWaitingInput)

	go func() {
		time.Sleep(300 * time.Millisecond)
		_ = rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowTurnWaitingInput,
				FlowSessionID: session.ID,
				FlowTurnID:    "ft-c",
				TeamID:        "owner",
			},
			Payload: map[string]any{"reply": "主分支是 main"},
		})
		store.mu.Lock()
		store.tasks[0].Status = types.ToolTaskCompleted
		store.mu.Unlock()
	}()

	outcome := rt.awaitTeamSettlement(context.Background(), session.ID, "owner")

	require.NotNil(t, outcome, "收口后必须拿到结论")
	require.Equal(t, "done", outcome.Status)
	require.Equal(t, "主分支是 main", outcome.Reply)
}

// 等不到收口时不能无限挂住，也不能把中间状态当结论。
func TestAwaitTeamSettlementGivesUpOnCancel(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	store := &memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "t1",
		FlowSessionID: "fs-forever",
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskRunning,
		Arguments:     map[string]any{"caller_team": "owner"},
	}}}
	rt.SetTaskStore(store)
	session := seedMailboxSession(t, rt, "fs-forever", types.SessionWaitingInput)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	outcome := rt.awaitTeamSettlement(ctx, session.ID, "owner")

	require.Nil(t, outcome, "没等到结论就返回空，由调用方决定怎么报")
}

// 下游任务完成的那一刻，被调用方的 pending 就清空了，但它还要跑一轮才能给出
// 结论。此时读到的仍是派发回执，必须继续等——这是 18:20:52 那次"等到了却拿到
// 回执"的直接原因。
func TestAwaitTeamSettlementWaitsForConclusionNotReceipt(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	store := &memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "t1",
		FlowSessionID: "fs-receipt",
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskRunning,
		Arguments:     map[string]any{"caller_team": "owner"},
	}}}
	rt.SetTaskStore(store)
	session := seedMailboxSession(t, rt, "fs-receipt", types.SessionWaitingInput)
	ctx := context.Background()

	// 被调用方此刻已经说过"我派下去了"。
	require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowTurnWaitingInput,
			FlowSessionID: session.ID,
			FlowTurnID:    "ft-a",
			TeamID:        "owner",
		},
		Payload: map[string]any{"reply": "已把这一步派给 repo-info"},
	}))

	go func() {
		time.Sleep(200 * time.Millisecond)
		// 下游完成：pending 立刻清空，但结论还没来。
		store.mu.Lock()
		store.tasks[0].Status = types.ToolTaskCompleted
		store.mu.Unlock()
		time.Sleep(300 * time.Millisecond)
		// 收口回合跑完，结论落地。
		_ = rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventFlowTurnWaitingInput,
				FlowSessionID: session.ID,
				FlowTurnID:    "ft-b",
				TeamID:        "owner",
			},
			Payload: map[string]any{"reply": "主分支是 main"},
		})
	}()

	outcome := rt.awaitTeamSettlement(context.Background(), session.ID, "owner")

	require.NotNil(t, outcome)
	require.Equal(t, "主分支是 main", outcome.Reply,
		"要等结论落地，不能把派发回执当成答复")
}

// 中间回合没有答案，就不该对外发布正文。
//
// s19 页面上的两条"已转给执行侧"就是这么来的：引擎在 intermediate 的回合里
// 也带了 reply，客户端不认 intermediate 标记时就会把它当答复渲染。让 reply 为空，
// 任何客户端都不会再犯这个错——过程话术没有第二个来源。
func TestIntermediateTurnPublishesNoReply(t *testing.T) {
	require.Equal(t, "", replyPayloadField("已转给执行侧核实主分支，稍后给结论", true),
		"中间回合不发布正文")
	require.Equal(t, "主分支是 master", replyPayloadField("主分支是 master", false),
		"结论回合照常发布")
}

// 被派活的团队一个工具都没调用，说明它什么也没做。它那句"已转给执行侧"不能
// 当结论交回去——s19 第二问就是这样：8 秒、tools=0、任务从没离开调用方。
func TestRunTeamRejectsIdleCallee(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-idle", types.SessionWaitingInput)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		CallerTeam:    "default",
		TeamID:        "verify",
		Item:          "核实主分支",
	})

	require.NoError(t, err)
	require.Equal(t, "failed", outcome.Status, "空转的回合不能算完成")
	require.Contains(t, outcome.Error, "without calling any tool")
}

// 入口团队接收用户消息时不适用这条：它是被用户唤醒的，不是被别的团队派活，
// 回一句"你好"是合法结果。
func TestRunTeamIdleCheckOnlyAppliesToTeamCalls(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-idle-entry", types.SessionWaitingInput)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "verify",
		Item:          "核实主分支",
	})

	require.NoError(t, err)
	require.NotEqual(t, "failed", outcome.Status, "没有调用方时不判空转")
}

// 中间回合的答复不能在任何一个出口留下：事件 payload 和 JSON-RPC 响应都要为空。
// 只清 payload 时，客户端从响应里仍然拿得到"已转给执行侧"，照样渲染成消息。
func TestIntermediateTurnClearsResponseReply(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	recorder := &inputRecordingTeamRuntime{}
	rt := newTestRuntime(testDefinitions(), recorder, writer, nil)
	session := seedMailboxSession(t, rt, "fs-resp", types.SessionWaitingInput)

	// 这个会话里还有一个 Team 调用在跑，所以本轮结束后它仍是"等待中"。
	store := &memoryTaskStore{tasks: []types.ToolTask{{
		ID:            "t1",
		FlowSessionID: session.ID,
		ToolName:      types.ToolNameTeamRun,
		Status:        types.ToolTaskRunning,
		Arguments:     map[string]any{"caller_team": "default"},
	}}}
	rt.SetTaskStore(store)

	result, err := rt.HandleInput(context.Background(), session.ID, "再查一个仓库")

	require.NoError(t, err)
	require.True(t, result.Turn.Intermediate, "还有调用在跑，本轮是进度")
	require.Empty(t, result.Reply, "响应里也不能带过程话术")
	require.Empty(t, result.FinalReply)
}
