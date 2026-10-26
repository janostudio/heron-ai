package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type stubTeamRunner struct {
	specs  []types.TeamCallSpec
	answer types.TeamCallOutcome
	err    error
}

func (r *stubTeamRunner) RunTeam(_ context.Context, spec types.TeamCallSpec) (types.TeamCallOutcome, error) {
	r.specs = append(r.specs, spec)
	if r.err != nil {
		return types.TeamCallOutcome{}, r.err
	}
	return r.answer, nil
}

// 协作是一次工具调用：模型给出目标 Team 和任务，工具把调用方的坐标补全。
func TestTeamToolRunsInlineWithoutTaskRunner(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{
		Status: "done",
		Reply:  "主分支是 master",
	}}
	tool := NewTeamTool(runner)

	result, err := tool.Execute(context.Background(), map[string]any{
		"team_id": "owner",
		"item":    "查 heron-ai 的主分支",
	})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "主分支是 master")
	require.Contains(t, result.Content, "done")
	require.Len(t, runner.specs, 1)
	require.Equal(t, "owner", runner.specs[0].TeamID)
}

// 缺参数要直接拒绝，不能把空任务派出去。
func TestTeamToolRejectsMissingArguments(t *testing.T) {
	tool := NewTeamTool(&stubTeamRunner{})

	missingTeam, err := tool.Execute(context.Background(), map[string]any{"item": "x"})
	require.NoError(t, err)
	require.False(t, missingTeam.Success)
	require.Contains(t, missingTeam.Error, "team_id")

	missingItem, err := tool.Execute(context.Background(), map[string]any{"team_id": "owner"})
	require.NoError(t, err)
	require.False(t, missingItem.Success)
	require.Contains(t, missingItem.Error, "item")
}

// 没有注入执行器时不能假装接受了调用。
func TestTeamToolWithoutRunner(t *testing.T) {
	tool := NewTeamTool(nil)

	result, err := tool.Execute(context.Background(), map[string]any{"team_id": "owner", "item": "x"})

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not configured")
}

// Team 的失败要作为调用失败返回，调用方据此决定是重派还是收口。
func TestTeamToolReportsRunnerError(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{Status: "blocked", Error: "缺少日志索引"}}
	tool := NewTeamTool(runner)

	result, err := tool.Execute(context.Background(), map[string]any{"team_id": "owner", "item": "x"})

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Content, "blocked")
	require.Contains(t, result.Content, "缺少日志索引")
}

// TeamRun 是内部任务名，模型不能直接调用；dispatcher 把它路由到 Team 工具。
func TestTeamTaskDispatcherRoutesTeamRun(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{Status: "done", Reply: "已完成"}}
	dispatcher := NewTeamTaskDispatcher(NewTeamTool(runner), nil)

	result, err := dispatcher.Execute(context.Background(), TeamRunToolName, map[string]any{
		"team_id":         "owner",
		"flow_session_id": "fs-1",
		"caller_team":     "default",
		"item":            "查主分支",
	})

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "已完成")
	require.Len(t, runner.specs, 1)
	require.Equal(t, "fs-1", runner.specs[0].FlowSessionID)
	require.Equal(t, "default", runner.specs[0].CallerTeam)
}

// 未注册的工具名仍然走基础执行器，dispatcher 不吞掉别的工具。
func TestTeamTaskDispatcherForwardsOtherTools(t *testing.T) {
	base := &teamForwardExecutor{}
	dispatcher := NewTeamTaskDispatcher(NewTeamTool(&stubTeamRunner{}), base)

	_, err := dispatcher.Execute(context.Background(), "Bash", map[string]any{"command": "ls"})

	require.NoError(t, err)
	require.Equal(t, []string{"Bash"}, base.names)
}

type teamForwardExecutor struct {
	names []string
}

func (e *teamForwardExecutor) Execute(_ context.Context, name string, _ map[string]any) (*types.ToolResult, error) {
	e.names = append(e.names, name)
	return &types.ToolResult{Success: true}, nil
}
