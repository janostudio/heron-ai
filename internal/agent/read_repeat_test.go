package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// opToolExecutor returns the operations real read/write tools report. The
// distinction matters for the repeat guard: a read carries a read operation, so
// only a write may reset the streak.
type opToolExecutor struct{}

func (e *opToolExecutor) Execute(_ context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	target := fmt.Sprint(args["file"])
	switch name {
	case "Write":
		return &types.ToolResult{
			Success:      true,
			Content:      "written " + target,
			WorkspaceOps: []types.WorkspaceOperation{{Kind: "write", Path: target}},
		}, nil
	default:
		return &types.ToolResult{
			Success:      true,
			Content:      "body of " + target,
			WorkspaceOps: []types.WorkspaceOperation{{Kind: "read", Path: target}},
		}, nil
	}
}

func readToolCall(id, file string) types.ToolCall {
	return types.ToolCall{ID: id, Name: "Read", Arguments: map[string]any{"file": file}}
}

func writeToolCall(id, file string) types.ToolCall {
	return types.ToolCall{ID: id, Name: "Write", Arguments: map[string]any{"file": file}}
}

func newRepeatReadLoop(model *mockModelProvider) *TurnLoop {
	return NewTurnLoop(
		model,
		&opToolExecutor{},
		nil,
		NewRouteParser(),
		nil,
		NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "看看这个文件"}}},
	)
}

func repeatReadAgent(maxRounds int) types.AgentConfig {
	return types.AgentConfig{
		Model: types.ModelConfig{Model: "test-model"},
		Tools: types.ToolConfig{Builtin: []string{"Read", "Write"}},
		Loop:  types.LoopConfig{MaxRounds: maxRounds},
	}
}

func toolMessages(messages []types.Message) []types.Message {
	var tools []types.Message
	for _, message := range messages {
		if message.Role == "tool" {
			tools = append(tools, message)
		}
	}
	return tools
}

// 同一个只读调用重复出现时，早先那份结果退化成指针。
//
// 两次读取返回的字节完全一样，两份都留在对话里等于让后面每一次请求都为同样的
// 内容再付一次钱；工具调用的配对关系靠 call id 维持，所以只换正文不删消息。
func TestRepeatedReadReplacesEarlierResult(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{readToolCall("c1", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c2", "/a.go")}},
		{Text: "done"},
	}}
	loop := newRepeatReadLoop(model)

	_, err := loop.Run(context.Background(), repeatReadAgent(5), types.AgentRequest{})
	require.NoError(t, err)

	tools := toolMessages(model.lastMessages)
	require.Len(t, tools, 2)
	require.Contains(t, tools[0].Content, "repeated later in this conversation",
		"旧结果退化成指针")
	require.Equal(t, "body of /a.go", tools[1].Content,
		"最新一份保留原文")
}

// 相同的读取反复回来时，回合最终要判停：模型已经拿不到任何新信息了。
//
// 读取本身也会产生 read 操作，所以"写后重置"必须只认写操作，否则第二次读取
// 就被当成"写过东西了"，判停永远不会触发。
//
// 第一次触顶只提醒、不判停（给模型一次收口机会），所以判停发生在第二次触顶：
// 3 次读 -> 警告并清零 -> 再 3 次读 -> 判停。
func TestRepeatedReadTripsStuckGuard(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{readToolCall("c1", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c2", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c3", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c4", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c5", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c6", "/a.go")}},
		{Text: "done"},
	}}
	loop := newRepeatReadLoop(model)

	result, err := loop.Run(context.Background(), repeatReadAgent(10), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status, "警告后仍然重复，要判停")
	require.Contains(t, result.Error, "same read returned 3 times")
}

// 第一次触顶时只提醒模型换手段，回合继续；模型收口则正常完成。
func TestRepeatedReadWarnsBeforeFailing(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{readToolCall("c1", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c2", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c3", "/a.go")}},
		{Text: "结论：文件内容没有变化，按已有信息收口"},
	}}
	loop := newRepeatReadLoop(model)

	result, err := loop.Run(context.Background(), repeatReadAgent(6), types.AgentRequest{})
	require.NoError(t, err)
	require.NotEqual(t, types.TurnFailed, result.Status, "首次触顶只提醒，不判停")

	var warned bool
	for _, message := range model.lastMessages {
		if message.Role == "user" && strings.Contains(message.Content, "has now returned the same content") {
			warned = true
		}
	}
	require.True(t, warned, "触发上限时要给模型一条换手段的提示")
}

// 相同的调用之间发生过写操作时，重复是编辑-校验循环，不算卡住。
func TestRepeatedReadAfterWriteIsNotStuck(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{readToolCall("c1", "/a.go")}},
		{ToolCalls: []types.ToolCall{writeToolCall("w1", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c2", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c3", "/a.go")}},
		{Text: "done"},
	}}
	loop := newRepeatReadLoop(model)

	result, err := loop.Run(context.Background(), repeatReadAgent(6), types.AgentRequest{})
	require.NoError(t, err)
	require.NotEqual(t, types.TurnFailed, result.Status, "写之后的重复读取是校验，不是卡住")
}
