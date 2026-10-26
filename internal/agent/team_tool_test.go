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
		"subagent_type": "owner",
		"prompt":        "查 heron-ai 的主分支",
		"description":   "查主分支",
	})

	require.NoError(t, err)
	require.True(t, result.Success)
	// 子代理工具返回的是正文本身，不是状态包——模型据此判断"这是答案"。
	require.Equal(t, "主分支是 master", result.Content)
	require.Len(t, runner.specs, 1)
	require.Equal(t, "owner", runner.specs[0].TeamID)
}

// 缺参数要直接拒绝，不能把空任务派出去。
func TestTeamToolRejectsMissingArguments(t *testing.T) {
	tool := NewTeamTool(&stubTeamRunner{})

	missingTeam, err := tool.Execute(context.Background(), map[string]any{"prompt": "x"})
	require.NoError(t, err)
	require.False(t, missingTeam.Success)
	require.Contains(t, missingTeam.Error, "subagent_type")

	missingItem, err := tool.Execute(context.Background(), map[string]any{"subagent_type": "owner"})
	require.NoError(t, err)
	require.False(t, missingItem.Success)
	require.Contains(t, missingItem.Error, "prompt")
}

// 没有注入执行器时不能假装接受了调用。
func TestTeamToolWithoutRunner(t *testing.T) {
	tool := NewTeamTool(nil)

	result, err := tool.Execute(context.Background(), map[string]any{"subagent_type": "owner", "prompt": "x", "description": "d"})

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not configured")
}

// Team 的失败要作为调用失败返回，调用方据此决定是重派还是收口。
func TestTeamToolReportsRunnerError(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{Status: "blocked", Error: "缺少日志索引"}}
	tool := NewTeamTool(runner)

	result, err := tool.Execute(context.Background(), map[string]any{"subagent_type": "owner", "prompt": "x", "description": "d"})

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Content, "owner could not complete the task")
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

// 可达性是 flow 的决定：Team 只能派给它所在 binding 声明的目标。模型可以绕开
// schema 硬写一个名字，所以执行时必须校验。
func TestTeamToolRejectsUnreachableTeam(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{Status: "done", Reply: "x"}}
	tool := NewTeamTool(runner)
	ctx := withSpawnIdentity(context.Background(), types.AgentConfig{}, types.AgentRequest{
		TeamID:       "default",
		CallID:       "coordinator",
		AllowedTeams: []string{"owner"},
	})

	outside, err := tool.Execute(ctx, map[string]any{"subagent_type": "sandbox", "prompt": "查日志", "description": "查日志"})

	require.NoError(t, err)
	require.False(t, outside.Success)
	require.Contains(t, outside.Error, "not reachable")
	require.Contains(t, outside.Error, "owner", "错误要说明可以派给谁")
	require.Empty(t, runner.specs, "越界调用不能真的派出去")

	inside, err := tool.Execute(ctx, map[string]any{"subagent_type": "owner", "prompt": "查日志", "description": "查日志"})
	require.NoError(t, err)
	require.True(t, inside.Success)
	require.Len(t, runner.specs, 1)
}

// Team 工具的 team_id 枚举来自当前 binding 的 can_activate：同一份 Agent 定义
// 在不同 flow 里能派的 Team 不同，这个范围属于配置，不该写进提示词。
func TestToolSchemasProjectAllowedTeams(t *testing.T) {
	agent := types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{TeamToolName}},
	}
	loop := &TurnLoop{}

	open := loop.buildToolSchemasFiltered(context.Background(), agent, nil)
	require.True(t, hasTeamSchema(open), "没有身份时仍给出工具定义")

	ctx := withSpawnIdentity(context.Background(), agent, types.AgentRequest{
		TeamID:       "default",
		AllowedTeams: []string{"owner"},
	})
	narrowed := loop.buildToolSchemasFiltered(ctx, agent, nil)
	teamSchema := findTeamSchema(t, narrowed)
	require.Equal(t, []string{"owner"}, teamSchema.Properties["subagent_type"].Enum,
		"枚举必须是这张 binding 的出边")
}

func hasTeamSchema(schemas []types.JSONSchema) bool {
	for _, schema := range schemas {
		if schema.Name == TeamToolName {
			return true
		}
	}
	return false
}

func findTeamSchema(t *testing.T, schemas []types.JSONSchema) types.JSONSchema {
	t.Helper()
	for _, schema := range schemas {
		if schema.Name == TeamToolName {
			return schema
		}
	}
	t.Fatalf("schema for %q not found", TeamToolName)
	return types.JSONSchema{}
}

type stubCatalog struct {
	entries []types.TeamCatalogEntry
	specs   []types.TeamCatalogSpec
}

func (c *stubCatalog) ListTeams(_ context.Context, spec types.TeamCatalogSpec) ([]types.TeamCatalogEntry, error) {
	c.specs = append(c.specs, spec)
	return c.entries, nil
}

// 目录要说明"谁在问"：同一个 Agent 在不同 Team 位置看到的可达集合不同。
func TestListTeamsToolUsesCallerIdentity(t *testing.T) {
	catalog := &stubCatalog{entries: []types.TeamCatalogEntry{
		{TeamID: "repo-info", Responsibility: "读仓库事实", Receives: []string{"user_message"}, Produces: "RepoFacts"},
	}}
	tool := NewListTeamsTool(catalog)
	ctx := withSpawnIdentity(context.Background(), types.AgentConfig{}, types.AgentRequest{
		FlowSessionID: "fs-1",
		TeamID:        "owner",
	})

	result, err := tool.Execute(ctx, nil)

	require.NoError(t, err)
	require.True(t, result.Success)
	require.Contains(t, result.Content, "repo-info")
	require.Contains(t, result.Content, "RepoFacts")
	require.Len(t, catalog.specs, 1)
	require.Equal(t, "fs-1", catalog.specs[0].FlowSessionID)
	require.Equal(t, "owner", catalog.specs[0].CallerTeam)
}

// 没有注入目录时不能假装有一条空目录：那会让模型以为无处可派。
func TestListTeamsToolWithoutCatalog(t *testing.T) {
	tool := NewListTeamsTool(nil)

	result, err := tool.Execute(context.Background(), nil)

	require.NoError(t, err)
	require.False(t, result.Success)
	require.Contains(t, result.Error, "not configured")
}
