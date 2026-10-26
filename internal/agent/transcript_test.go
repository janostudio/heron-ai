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

// Agent 定义变了，历史不能继续：那些消息是在另一套规则下产生的。
func TestContinueTranscriptDiscardsOnSystemChange(t *testing.T) {
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

	rendered := renderedFor("新规则", "查看仓库主分支")
	got, previous := loop.continueTranscript(context.Background(), transcriptReq("tt-2"), rendered)

	require.Nil(t, previous, "system 变了要重建")
	require.Len(t, got, 2)
	require.Equal(t, "新规则", got[0].Content)
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
