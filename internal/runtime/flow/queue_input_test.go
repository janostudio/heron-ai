package flow

import (
	"context"
	"testing"
	"time"

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
	_, clear := rt.markTurnRunning(context.Background(), session.ID)
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

// 会话正在跑的时候收到的消息可能带附件（context blocks）。排队必须把它们一起存下去：
// 只存文字会让排队的回合跑在一条用户从没发过的消息上——图没了，用户却以为发出去了。
func TestQueuedInputKeepsItsContextBlocks(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-blocks", types.SessionRunning)

	_, clear := rt.markTurnRunning(context.Background(), session.ID)
	defer clear()

	blocks := []types.ContextBlock{{
		Kind: "input", Text: "这张图", Source: "http",
		Stability: "dynamic", Priority: 80, Compressible: true,
		Parts: []types.ContentPart{{
			Type:  "image",
			Media: &types.MediaAttachment{SourceType: "base64", DataBase64: "aGk=", Kind: "image"},
		}},
	}}

	result, err := rt.HandleInputWithContext(context.Background(), session.ID, "这张图", blocks)
	require.NoError(t, err)
	require.True(t, result.Queued, "正在跑的时候收到，应排队")

	box, err := rt.loadMailbox(context.Background(), session.ID)
	require.NoError(t, err)
	var payload map[string]any
	for _, msg := range box {
		if msg.Kind == mailboxKindUser && msg.Status == types.MailboxPending {
			payload = msg.Payload
		}
	}
	require.NotNil(t, payload, "没找到排队的用户消息")

	restored := contextBlocksOf(payload)
	require.Len(t, restored, 1, "排队的消息丢了 context blocks")
	require.Equal(t, "这张图", restored[0].Text)
	require.Len(t, restored[0].Parts, 1)
	require.NotNil(t, restored[0].Parts[0].Media, "附件在排队时被丢掉了")
	require.Equal(t, "aGk=", restored[0].Parts[0].Media.DataBase64)
}

// 排队消息按到达顺序执行：mailbox 是 map，遍历顺序随机，所以"取第一条看到的"
// 会让连问三个问题时答案顺序乱掉（实测出现过 三、一、二）。
func TestOldestPendingUserInputIsFIFO(t *testing.T) {
	base := time.Now().UTC()
	box := map[string]types.MailboxMessage{
		"mb_c": {MessageID: "mb_c", Kind: mailboxKindUser, Status: types.MailboxPending, CreatedAt: base.Add(2 * time.Second)},
		"mb_a": {MessageID: "mb_a", Kind: mailboxKindUser, Status: types.MailboxPending, CreatedAt: base},
		"mb_b": {MessageID: "mb_b", Kind: mailboxKindUser, Status: types.MailboxPending, CreatedAt: base.Add(time.Second)},
		// 不参与选择：不是用户消息、或已经不是 pending。
		"mb_work": {MessageID: "mb_work", Kind: "work", Status: types.MailboxPending, CreatedAt: base.Add(-time.Second)},
		"mb_done": {MessageID: "mb_done", Kind: mailboxKindUser, Status: types.MailboxDelivered, CreatedAt: base.Add(-2 * time.Second)},
	}

	first, ok := oldestPendingUserInput(box)
	require.True(t, ok)
	require.Equal(t, "mb_a", first.MessageID, "最早排队的那条应先执行")

	delete(box, "mb_a")
	second, ok := oldestPendingUserInput(box)
	require.True(t, ok)
	require.Equal(t, "mb_b", second.MessageID, "第二条排队消息应紧接其后")

	// 同一时间戳时用 MessageID 兜底，保证顺序稳定而不是随机。
	tie := map[string]types.MailboxMessage{
		"mb_y": {MessageID: "mb_y", Kind: mailboxKindUser, Status: types.MailboxPending, CreatedAt: base},
		"mb_x": {MessageID: "mb_x", Kind: mailboxKindUser, Status: types.MailboxPending, CreatedAt: base},
	}
	tied, ok := oldestPendingUserInput(tie)
	require.True(t, ok)
	require.Equal(t, "mb_x", tied.MessageID)
}
