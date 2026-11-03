package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// callerIdentity builds the context a tool sees inside one AgentTurn: who is
// calling, from which Team instance.
func callerIdentity(flowSessionID, teamID, teamSessionID string) context.Context {
	return withSpawnIdentity(context.Background(),
		types.AgentConfig{Name: "dev-lead"},
		types.AgentRequest{
			FlowSessionID: flowSessionID,
			TeamID:        teamID,
			TeamSessionID: teamSessionID,
			CallID:        "code",
			AgentTurnID:   "at-1",
		})
}

// TestTeamToolForwardsTaskKeyAndOwnership is the model boundary: task_key the
// model passes must reach the callee, and the caller's instance must travel
// with it so the task has an owner.
func TestTeamToolForwardsTaskKeyAndOwnership(t *testing.T) {
	runner := &stubTeamRunner{answer: types.TeamCallOutcome{
		Status:        "done",
		Reply:         "改完了",
		TaskKey:       "repoA-143",
		TeamSessionID: "fs:dev:1",
	}}
	tool := NewTeamTool(runner)

	result, err := tool.Execute(
		callerIdentity("fs", "dev-lead", "fs:dev-lead"),
		map[string]any{
			"subagent_type": "dev",
			"prompt":        "改 A 仓库的登录 bug",
			"description":   "改 A",
			"task_key":      "repoA-143",
		})
	require.NoError(t, err)
	require.True(t, result.Success)

	require.Len(t, runner.specs, 1)
	require.Equal(t, "repoA-143", runner.specs[0].TaskKey)
	require.NotNil(t, runner.specs[0].DispatchedBy)
	require.Equal(t, "fs:dev-lead", runner.specs[0].DispatchedBy.TeamSessionID,
		"the dispatching instance owns the task")

	// The handle is the only place the caller learns how to continue the task.
	require.Contains(t, result.Content, "task_key=repoA-143")
	require.Contains(t, result.Content, "team_session_id=fs:dev:1")
	require.Equal(t, "repoA-143", result.Metadata["task_key"])
	require.Equal(t, "fs:dev:1", result.Metadata["team_session_id"])
}

// TestTeamRunArgumentsKeepTaskKeyAcrossRestart pins the durable-task round trip:
// a Team call becomes a TaskRun whose arguments are persisted, and after a
// restart those arguments are all the engine has. task_key and the dispatcher
// must survive that trip, otherwise a task loses its identity (or its owner).
func TestTeamRunArgumentsKeepTaskKeyAcrossRestart(t *testing.T) {
	spec := types.TeamCallSpec{
		FlowSessionID: "fs",
		CallerTeam:    "dev-lead",
		TeamID:        "dev",
		CallID:        "code",
		AgentTurnID:   "at-1",
		TaskKey:       "repoA-143",
		DispatchedBy: &types.TeamSessionRef{
			TeamSessionID: "fs:dev-lead",
			CallID:        "code",
			AgentTurnID:   "at-1",
		},
		Item: "改 A 仓库的登录 bug",
	}

	// What the durable task stores, then replays.
	arguments := teamRunArguments(spec)

	runner := &stubTeamRunner{answer: types.TeamCallOutcome{Status: "done", Reply: "改完了"}}
	dispatcher := NewTeamTaskDispatcher(NewTeamTool(runner), nil)
	result, err := dispatcher.Execute(context.Background(), TeamRunToolName, arguments)
	require.NoError(t, err)
	require.True(t, result.Success)

	require.Len(t, runner.specs, 1)
	replayed := runner.specs[0]
	require.Equal(t, "repoA-143", replayed.TaskKey)
	require.NotNil(t, replayed.DispatchedBy)
	require.Equal(t, "fs:dev-lead", replayed.DispatchedBy.TeamSessionID)
	require.Equal(t, "改 A 仓库的登录 bug", replayed.Item)
}

// stubTaskCatalog is the read side the TeamSession tool talks to.
type stubTaskCatalog struct {
	tasks []types.TaskInfo
	trees []types.TaskTree
}

func (c *stubTaskCatalog) ListTasks(_ context.Context, _ types.TaskCatalogSpec) ([]types.TaskInfo, error) {
	return c.tasks, nil
}

func (c *stubTaskCatalog) ListTaskTrees(_ context.Context, _ types.TaskCatalogSpec) ([]types.TaskTree, error) {
	return c.trees, nil
}

func (c *stubTaskCatalog) GetTask(_ context.Context, _ types.TaskCatalogSpec, taskKey string) (types.TaskInfo, error) {
	for _, task := range c.tasks {
		if task.TaskKey == taskKey {
			return task, nil
		}
	}
	return types.TaskInfo{}, fmt.Errorf("task %q not found", taskKey)
}

// TestTeamSessionToolReadsDispatchedTasks is the read-back half of the chain:
// the model asks by task_key (not by session id) and gets status/list/tree.
func TestTeamSessionToolReadsDispatchedTasks(t *testing.T) {
	catalog := &stubTaskCatalog{
		tasks: []types.TaskInfo{
			{TaskKey: "repoA-143", TeamID: "dev", TeamSessionID: "fs:dev:1", Status: types.SessionCompleted},
			{TaskKey: "repoB-207", TeamID: "dev", TeamSessionID: "fs:dev:2", Status: types.SessionRunning},
		},
		trees: []types.TaskTree{{TaskContextID: "fs:dev:1", Tasks: nil}},
	}
	tool := NewTeamSessionTool(catalog)
	ctx := callerIdentity("fs", "dev-lead", "fs:dev-lead")

	status, err := tool.Execute(ctx, map[string]any{"action": "status", "task_key": "repoA-143"})
	require.NoError(t, err)
	require.True(t, status.Success)
	require.Contains(t, status.Content, "repoA-143")
	require.Contains(t, status.Content, "fs:dev:1")

	list, err := tool.Execute(ctx, map[string]any{"action": "list"})
	require.NoError(t, err)
	require.True(t, list.Success)
	require.Contains(t, list.Content, "repoB-207")

	tree, err := tool.Execute(ctx, map[string]any{"action": "tree"})
	require.NoError(t, err)
	require.True(t, tree.Success)
	require.Contains(t, tree.Content, "fs:dev:1")

	missing, err := tool.Execute(ctx, map[string]any{"action": "status", "task_key": "nope"})
	require.NoError(t, err)
	require.False(t, missing.Success)

	// Without a caller identity there is no "my tasks" to answer for.
	noIdentity, err := tool.Execute(context.Background(), map[string]any{"action": "list"})
	require.NoError(t, err)
	require.False(t, noIdentity.Success)
}
