package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func newTranscriptLoop(t *testing.T) (*TurnLoop, *FileTranscriptStore) {
	t.Helper()
	store := NewFileTranscriptStore(storage.NewFileStore(t.TempDir()))
	loop := &TurnLoop{}
	loop.SetTranscriptStore(store)
	return loop, store
}

func transcriptReq(turnID string) types.AgentRequest {
	return types.AgentRequest{
		FlowSessionID: "fs-1",
		TeamID:        "default",
		CallID:        "coordinator",
		AgentID:       "coordinator",
		AgentTurnID:   turnID,
	}
}

func renderedFor(system, user string) []types.Message {
	return []types.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
}

// 第一次没有转录：原样返回渲染结果，不编造历史。
func TestContinueTranscriptWithoutPrevious(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	rendered := renderedFor("sys", "查看仓库主分支")

	got, previous := loop.continueTranscript(context.Background(), transcriptReq("tt-1"), rendered)

	require.Nil(t, previous)
	require.Len(t, got, 2)
}

// 第二个回合必须接着上一轮的对话，而不是重新渲染一份。
func TestContinueTranscriptAppendsToPrevious(t *testing.T) {
	loop, store := newTranscriptLoop(t)
	req := transcriptReq("tt-1")

	// 第一轮结束后落盘：system + 用户输入 + 模型回答。
	loop.saveTranscript(context.Background(), req,
		[]types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看仓库主分支"},
			{Role: "assistant", Content: "已转给执行侧"},
		},
		nil,
		renderedFor("sys", "查看仓库主分支"),
	)

	// 第二轮：渲染出同一份 system、同一句用户输入，再加一条新消息。
	second := transcriptReq("tt-2")
	rendered := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "查看仓库主分支"},
		{Role: "assistant", TeamID: "owner", Content: "主分支是 master"},
	}

	got, previous := loop.continueTranscript(context.Background(), second, rendered)

	require.NotNil(t, previous)
	require.Len(t, got, 4, "历史三条 + 新增一条；重复的用户输入不再追加")
	require.Equal(t, "assistant", got[2].Role)
	require.Equal(t, "已转给执行侧", got[2].Content, "上一轮的产出仍在")
	require.Equal(t, "owner", got[3].TeamID)
	require.Equal(t, "主分支是 master", got[3].Content)

	// 保存后能读回同一个数组。
	saved, err := store.Load(context.Background(), transcriptID(second))
	require.NoError(t, err)
	require.NotNil(t, saved)
}

// Agent 定义变了，换掉 system，历史留着。
//
// 之前是整份丢弃：模型从空白重来，把已经做完的事再派一遍。定义改的是提示词，
// 不是发生过的事。
func TestContinueTranscriptKeepsHistoryWhenSystemChanges(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")
	loop.saveTranscript(context.Background(), req,
		[]types.Message{
			{Role: "system", Content: "旧规则"},
			{Role: "user", Content: "查看仓库主分支"},
			{Role: "assistant", Content: "主分支是 master"},
		},
		nil,
		renderedFor("旧规则", "查看仓库主分支"),
	)

	rendered := renderedFor("新规则", "接着看")
	got, previous := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), rendered)

	require.NotNil(t, previous, "历史还在，就要按续聊处理")
	require.Equal(t, "新规则", got[0].Content, "system 用本轮的")
	require.Contains(t, messageContents(got), "查看仓库主分支", "历史不能丢")
	require.Contains(t, messageContents(got), "主分支是 master")
	require.Contains(t, messageContents(got), "接着看", "本轮输入照常追加")
}

// 同一回合重试不能把输入追加两次。
func TestContinueTranscriptRetryDoesNotDuplicateInput(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")
	loop.saveTranscript(context.Background(), req,
		[]types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看仓库主分支"},
		},
		nil,
		renderedFor("sys", "查看仓库主分支"),
	)

	got, previous := loop.continueTranscript(context.Background(), req, renderedFor("sys", "查看仓库主分支"))

	require.NotNil(t, previous)
	require.Len(t, got, 2, "同一回合重试不应再次追加")
}

// 同一条记录在两个回合各交一次，只应出现在历史里一次。
func TestContinueTranscriptDeduplicatesRepeatedRecords(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	first := transcriptReq("tt-1")
	record := types.Message{Role: "assistant", TeamID: "owner", Content: "主分支是 master"}

	loop.saveTranscript(context.Background(), first,
		[]types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看仓库主分支"},
			record,
		},
		nil,
		[]types.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "查看仓库主分支"},
			record,
		},
	)

	got, previous := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "查看仓库主分支"},
		record,
	})

	require.NotNil(t, previous)
	require.Len(t, got, 3, "重复的记录不再追加")
}

// 每轮重建的提醒块必须被替换，不能再追加一份。
//
// 清单里写着"派出多久"，内容每轮都变，按内容哈希去重永远命中不了；不替换的话
// 对话里会同时留着 15 分钟、18 分钟、27 分钟三个版本的任务状态。
func TestContinueTranscriptReplacesRebuiltBlocks(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")

	first := "<system-reminder>\n[任务清单] dev-lead failed 15m\n</system-reminder>"
	second := "<system-reminder>\n[任务清单] dev-lead failed 18m\n</system-reminder>"

	loop.saveTranscript(context.Background(), req, []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: first, BlockType: types.BlockTypeHarnessReminder},
		{Role: "user", Content: "查看仓库主分支"},
	}, nil, nil)

	got, previous := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: second, BlockType: types.BlockTypeHarnessReminder},
	})

	require.NotNil(t, previous)
	reminders := 0
	for _, message := range got {
		if message.BlockType == types.BlockTypeHarnessReminder {
			reminders++
		}
	}
	require.Equal(t, 1, reminders, "旧提醒块要被替换，不能累积")
	require.Contains(t, got[len(got)-1].Content, "18m", "留下的是本回合重建的那份")
}

// 本轮没有重建提醒块时，不能把历史里那份删掉：它是模型唯一的知识库与待办指针。
func TestContinueTranscriptKeepsReminderWhenTurnHasNone(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")

	reminder := "<system-reminder>\n[任务清单] dev-lead failed 15m\n</system-reminder>"
	loop.saveTranscript(context.Background(), req, []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: reminder, BlockType: types.BlockTypeHarnessReminder},
		{Role: "user", Content: "查看仓库主分支"},
	}, nil, nil)

	got, _ := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "接着看测试"},
	})

	reminders := 0
	for _, message := range got {
		if message.BlockType == types.BlockTypeHarnessReminder {
			reminders++
		}
	}
	require.Equal(t, 1, reminders, "本轮没有新提醒块时，旧的那份要留着")
}

// 提醒块内容连续两轮一模一样时（清单按整分钟显示年龄），旧的那份被删掉后
// 新的一定要补上：内容哈希去重如果还记着旧 key，就会把新的也跳过，最后一条
// 提醒都不剩。
func TestContinueTranscriptKeepsIdenticalReminder(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")

	reminder := "<system-reminder>\n[任务清单] dev-lead failed 15m\n</system-reminder>"
	loop.saveTranscript(context.Background(), req, []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: reminder, BlockType: types.BlockTypeHarnessReminder},
		{Role: "user", Content: "查看仓库主分支"},
	}, nil, nil)

	got, _ := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: reminder, BlockType: types.BlockTypeHarnessReminder},
	})

	reminders := 0
	for _, message := range got {
		if message.BlockType == types.BlockTypeHarnessReminder {
			reminders++
		}
	}
	require.Equal(t, 1, reminders, "内容相同也要替换成新的一份，不能一条都不剩")
}

// 同一回合重试时也要换 system：否则重试会把旧定义再写回落盘文件。
func TestContinueTranscriptSwapSystemOnRetry(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	req := transcriptReq("tt-1")

	loop.saveTranscript(context.Background(), req, []types.Message{
		{Role: "system", Content: "旧定义"},
		{Role: "user", Content: "查 heron-ai 主分支"},
	}, nil, nil)

	got, previous := loop.continueTranscript(context.Background(), req, renderedFor("新定义", "查 heron-ai 主分支"))

	require.NotNil(t, previous)
	require.Equal(t, 2, len(got), "重试不追加重复输入")
	require.Equal(t, "新定义", got[0].Content)
}
