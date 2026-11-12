package agent

import (
	"context"
	"fmt"
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

// 相同的读取反复回来时，回合要判停：模型已经拿不到任何新信息了。
//
// 读取本身也会产生 read 操作，所以"写后重置"必须只认写操作，否则第二次读取
// 就被当成"写过东西了"，判停永远不会触发。
func TestRepeatedReadTripsStuckGuard(t *testing.T) {
	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{readToolCall("c1", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c2", "/a.go")}},
		{ToolCalls: []types.ToolCall{readToolCall("c3", "/a.go")}},
		{Text: "done"},
	}}
	loop := newRepeatReadLoop(model)

	result, err := loop.Run(context.Background(), repeatReadAgent(6), types.AgentRequest{})
	require.NoError(t, err)
	require.Equal(t, types.TurnFailed, result.Status, "重复读取超过阈值要判停")
	require.Contains(t, result.Error, "same read returned 3 times")
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
