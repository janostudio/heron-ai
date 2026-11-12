package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func sessionEvent(seq int64, typ, callTurnID string, payload map[string]any) storage.SessionEvent {
	return storage.SessionEvent{
		EventHeader: types.EventHeader{Seq: seq, Type: typ, CallTurnID: callTurnID},
		Payload:     payload,
	}
}

func toolCallPayload(id, name string, args map[string]any) map[string]any {
	return map[string]any{
		"text": "",
		"tool_calls": []any{
			map[string]any{"id": id, "name": name, "arguments": args},
		},
	}
}

// 事件流能完整重建一段对话：用户说了什么、模型回了什么、工具返回了什么。
// 这是"日志是记录"的前提——重不出来就只是一堆日志。
func TestReplayConversationBuildsBothViews(t *testing.T) {
	events := []storage.SessionEvent{
		sessionEvent(1, types.EventAgentTurnStarted, "tt-1:receptionist", map[string]any{"input": "查一下主分支"}),
		sessionEvent(2, types.EventAgentModelResponse, "tt-1:receptionist", toolCallPayload("c1", "Read", map[string]any{"file": "/a.go"})),
		sessionEvent(3, types.EventToolCallCompleted, "tt-1:receptionist", map[string]any{"tool_call_id": "c1", "content": "master"}),
		// 唤醒回合没有用户消息，不能凭空造一条空的。
		sessionEvent(4, types.EventAgentTurnStarted, "tt-1:receptionist", map[string]any{"input": ""}),
		sessionEvent(5, types.EventAgentModelResponse, "tt-1:receptionist", map[string]any{"text": "主分支是 master"}),
	}

	conversation := ReplayConversation(events, "tt-1:receptionist")

	require.Len(t, conversation.Full, 4)
	require.Equal(t, []string{"user", "assistant", "tool", "assistant"}, rolesOf(conversation.Full))
	require.Equal(t, "查一下主分支", conversation.Full[0].Content)
	require.Len(t, conversation.Full[1].ToolCalls, 1)
	require.Equal(t, "Read", conversation.Full[1].ToolCalls[0].Name)
	require.Equal(t, "/a.go", conversation.Full[1].ToolCalls[0].Arguments["file"])
	require.Equal(t, "master", conversation.Full[2].Content)
	require.Equal(t, "c1", conversation.Full[2].ToolCallID)

	require.Equal(t, conversation.Full, conversation.Messages, "没有压缩时两个视图一致")
	require.Empty(t, conversation.Compactions)
}

// 压缩边界只改模型视图：历史仍然完整，上下文换成"摘要 + 幸存的组"。
func TestReplayConversationAppliesCompactionBoundary(t *testing.T) {
	events := []storage.SessionEvent{
		sessionEvent(1, types.EventAgentTurnStarted, "tt-1:receptionist", map[string]any{"input": "查一下主分支"}),
		sessionEvent(2, types.EventAgentModelResponse, "tt-1:receptionist", toolCallPayload("c1", "Read", map[string]any{"file": "/a.go"})),
		sessionEvent(3, types.EventToolCallCompleted, "tt-1:receptionist", map[string]any{"tool_call_id": "c1", "content": "master"}),
		sessionEvent(4, types.EventAgentModelResponse, "tt-1:receptionist", map[string]any{"text": "主分支是 master"}),
		sessionEvent(5, types.EventContextCompacted, "tt-1:receptionist", map[string]any{
			"summary":       "用户问的是主分支，工具确认是 master。",
			"dropped_count": 1,
		}),
	}

	conversation := ReplayConversation(events, "tt-1:receptionist")

	require.Len(t, conversation.Full, 4, "历史不因压缩而丢失")
	require.Len(t, conversation.Compactions, 1)

	// dropped_count=1 丢掉第一个组（user），摘要顶在最前面。
	require.Len(t, conversation.Messages, 4)
	require.Equal(t, "user", conversation.Messages[0].Role)
	require.Contains(t, conversation.Messages[0].Content, "## Compacted Agent Context")
	require.Contains(t, conversation.Messages[0].Content, "工具确认是 master")
	require.Equal(t, []string{"assistant", "tool", "assistant"}, rolesOf(conversation.Messages[1:]))
}

// 只重建指定 agent 的对话：同一个会话里跑着多个 agent，串流会让每个都看到别人说过的话。
func TestReplayConversationFiltersByCallTurn(t *testing.T) {
	events := []storage.SessionEvent{
		sessionEvent(1, types.EventAgentTurnStarted, "tt-1:receptionist", map[string]any{"input": "前台的话"}),
		sessionEvent(2, types.EventAgentTurnStarted, "tt-1:dev-lead", map[string]any{"input": "开发侧的话"}),
	}

	conversation := ReplayConversation(events, "tt-1:dev-lead")

	require.Len(t, conversation.Full, 1)
	require.Equal(t, "开发侧的话", conversation.Full[0].Content)
}

func rolesOf(messages []types.Message) []string {
	roles := make([]string, 0, len(messages))
	for _, message := range messages {
		roles = append(roles, message.Role)
	}
	return roles
}

// 对话记录是缓存：文件没了，就从事件流重建，而不是从空白开始。
func TestContinueTranscriptRebuildsFromEventsWhenSnapshotMissing(t *testing.T) {
	loop, _ := newTranscriptLoop(t)
	writer := &fakeSessionWriter{}
	_, err := writer.Append(t.Context(), "fs-1", storage.LayerAgent, sessionEvent(0,
		types.EventAgentTurnStarted, "tt-1:coordinator", map[string]any{"input": "上一轮的问题"}))
	require.NoError(t, err)
	_, err = writer.Append(t.Context(), "fs-1", storage.LayerAgent, sessionEvent(0,
		types.EventAgentModelResponse, "tt-1:coordinator", map[string]any{"text": "上一轮的回答"}))
	require.NoError(t, err)
	loop.SetSessionWriter(writer)

	req := transcriptReq("tt-2")
	req.CallTurnID = "tt-1:coordinator"
	got, previous := loop.continueTranscript(t.Context(), req, []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "这一轮的问题"},
	})

	require.NotNil(t, previous, "快照缺失时应当从事件流重建出一份")
	joined := strings.Join(messageContents(got), "\n")
	require.Contains(t, joined, "上一轮的问题")
	require.Contains(t, joined, "上一轮的回答")
	require.Contains(t, joined, "这一轮的问题")
}
