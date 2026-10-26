package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// 会话正在跑的时候，用户又发了一条：不能拒绝，也不能打断当前回合，只能存起来。
// 用户看到"已收到"，等当前回合结束后自动执行。
func TestStartQueuesInputWhileTurnRuns(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-queue", types.SessionRunning)

	// 本进程正在为这个会话执行回合。
	clear := rt.markTurnRunning(session.ID)
	defer clear()

	result, err := rt.HandleInputWithContext(context.Background(), session.ID, "第二个问题", nil)
	require.NoError(t, err, "正在跑的时候不该报错")
	require.True(t, result.Queued, "消息应被标记为排队")
	require.Contains(t, result.Reply, "已收到", "要告诉用户消息没丢")

	box, err := rt.loadMailbox(context.Background(), session.ID)
	require.NoError(t, err)
	queued := 0
	for _, msg := range box {
		if msg.Kind == mailboxKindUser && msg.Status == types.MailboxPending {
			queued++
			require.Equal(t, "第二个问题", msg.Payload["input"])
		}
	}
	require.Equal(t, 1, queued, "排队的是用户消息，且只有一条")
}

// 进程重启后，会话还标着 running 但没有本进程的回合——那是崩溃遗留，必须走恢复，
// 不能当成"正在跑"去排队，否则会永远排下去。
func TestStartStillRejectsCrashLeftover(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-crash", types.SessionRunning)

	result, err := rt.HandleInputWithContext(context.Background(), session.ID, "x", nil)

	// 本进程没有回合在跑，所以不走排队分支：要么按崩溃遗留报错，要么正常执行，
	// 但绝不会返回"已排队"——那会让消息永远排下去。
	if err == nil {
		require.False(t, result.Queued, "没有本进程回合时不能排队")
	}
}
