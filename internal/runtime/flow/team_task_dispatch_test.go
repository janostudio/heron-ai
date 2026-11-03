package flow

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func perTaskDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "code-fix",
			EntryTeamID: "default",
			Teams: map[string]types.FlowTeamBinding{
				"default": {
					ID:          "default",
					TeamID:      "default-team",
					CanActivate: []string{"dev"},
					Inputs:      types.InputSpec{UserMessage: true},
				},
				"dev": {
					ID:        "dev",
					TeamID:    "dev-team",
					Lifecycle: types.TeamLifecyclePerTask,
					Inputs:    types.InputSpec{UserMessage: true},
				},
			},
		},
		Teams: map[string]types.Team{
			"default-team": {ID: "default-team"},
			"dev-team":     {ID: "dev-team"},
		},
	}
}

func newPerTaskRuntime(t *testing.T) (*Runtime, *fakeTeamRuntime) {
	t.Helper()
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	fake := &fakeTeamRuntime{}
	rt := newTestRuntime(perTaskDefinitions(), fake, writer, nil)
	return rt, fake
}

func writeFlowSession(t *testing.T, rt *Runtime, session types.FlowSession) {
	t.Helper()
	require.NoError(t, rt.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": session},
	}))
}

func dispatchTask(t *testing.T, rt *Runtime, sessionID, taskKey, dispatcher string) types.TeamCallOutcome {
	t.Helper()
	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: sessionID,
		TeamID:        "dev",
		TaskKey:       taskKey,
		DispatchedBy:  &types.TeamSessionRef{TeamSessionID: dispatcher},
	})
	require.NoError(t, err)
	return outcome
}

// TestRunTeamPerTaskReusesTheSameInstance is the core of "回到同一个任务":
// one business key keeps one Team instance across dispatches, a second key runs
// in parallel, and the handle comes back to the caller.
func TestRunTeamPerTaskReusesTheSameInstance(t *testing.T) {
	rt, fake := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	first := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, "done", first.Status)
	require.Equal(t, teamSessionID(session.ID, "dev", 1), first.TeamSessionID)
	require.Equal(t, "repoA-143", first.TaskKey)
	require.False(t, first.Reopened)

	second := dispatchTask(t, rt, session.ID, "repoB-207", "fs:default")
	require.Equal(t, teamSessionID(session.ID, "dev", 2), second.TeamSessionID,
		"a different task must get its own instance")

	again := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, teamSessionID(session.ID, "dev", 1), again.TeamSessionID,
		"the same task_key must return to the same instance")
	require.False(t, again.Reopened)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.devSessions, 3)
	require.Equal(t, 2, fake.devSessions[2].DispatchCount,
		"the reused instance counts its rounds")
	require.Equal(t, teamSessionID(session.ID, "dev", 1), fake.devSessions[2].TaskContextID,
		"the reused instance keeps its task context")
}

// TestRunTeamPerTaskRequiresTaskKey keeps the failure loud: without a key the
// task cannot be found again, so the dispatch is refused instead of creating an
// orphan instance.
func TestRunTeamPerTaskRequiresTaskKey(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "dev",
	})
	require.NoError(t, err)
	require.Equal(t, "failed", outcome.Status)
	require.Contains(t, outcome.Error, "task_key is required")
}

// TestRunTeamPerTaskRefusesForeignDispatcher pins ownership: the instance that
// dispatched a task owns it; another instance reusing the key is refused.
func TestRunTeamPerTaskRefusesForeignDispatcher(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	first := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, "done", first.Status)

	outcome := dispatchTask(t, rt, session.ID, "repoA-143", "fs:someone-else")
	require.Equal(t, "failed", outcome.Status)
	require.Contains(t, outcome.Error, "owned by")
}

// TestRunTeamResidentKeepsWorking guards the default: a resident Team still
// runs through the same path with no task_key at all.
func TestRunTeamResidentKeepsWorking(t *testing.T) {
	rt, fake := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "default",
	})
	require.NoError(t, err)
	require.Equal(t, session.ID+":default", outcome.TeamSessionID)
	require.Empty(t, outcome.TaskKey)

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.devSessions, 0)
}

// TestTaskCatalogSeesOnlyOwnTasks is the query side: an instance lists the
// tasks it dispatched, not anybody else's.
func TestTaskCatalogSeesOnlyOwnTasks(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	dispatchTask(t, rt, session.ID, "repoB-207", "fs:default")
	dispatchTask(t, rt, session.ID, "other-1", "fs:another")

	spec := types.TaskCatalogSpec{FlowSessionID: session.ID, CallerTeamSessionID: "fs:default"}
	tasks, err := rt.ListTasks(ctx, spec)
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	keys := []string{tasks[0].TaskKey, tasks[1].TaskKey}
	require.Contains(t, keys, "repoA-143")
	require.Contains(t, keys, "repoB-207")

	info, err := rt.GetTask(ctx, spec, "repoA-143")
	require.NoError(t, err)
	require.Equal(t, teamSessionID(session.ID, "dev", 1), info.TeamSessionID)
	require.Equal(t, types.SessionCompleted, info.Status)

	_, err = rt.GetTask(ctx, spec, "never-dispatched")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not found"))
}

// TestScenarioTwoReposThenReturn is the S1 acceptance scenario end to end at
// the flow boundary: two unrelated repos run as two task instances, the caller
// comes back to the first one, and abandoning the second leaves the first alone.
func TestScenarioTwoReposThenReturn(t *testing.T) {
	rt, fake := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	// 1. 两个仓库并行。
	a := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	b := dispatchTask(t, rt, session.ID, "repoB-207", "fs:default")
	require.Equal(t, "done", a.Status)
	require.Equal(t, "done", b.Status)
	require.NotEqual(t, a.TeamSessionID, b.TeamSessionID)

	// 2. 回到 A 继续：同一实例，轮次 +1。
	again := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, a.TeamSessionID, again.TeamSessionID)
	require.False(t, again.Reopened)

	// 3. 放弃 B：A 不受影响，B 变 closed。
	spec := types.TaskCatalogSpec{FlowSessionID: session.ID, CallerTeamSessionID: "fs:default"}
	closed, err := rt.CloseTask(ctx, spec, "repoB-207")
	require.NoError(t, err)
	require.Equal(t, types.SessionClosed, closed.Status)

	aInfo, err := rt.GetTask(ctx, spec, "repoA-143")
	require.NoError(t, err)
	require.Equal(t, types.SessionCompleted, aInfo.Status, "closing B must not touch A")
	require.Equal(t, 2, aInfo.DispatchCount)

	tasks, err := rt.ListTasks(ctx, spec)
	require.NoError(t, err)
	require.Len(t, tasks, 2, "both tasks remain visible after the close")

	fake.mu.Lock()
	defer fake.mu.Unlock()
	require.Len(t, fake.devSessions, 3)
	require.Equal(t, a.TeamSessionID, fake.devSessions[2].ID,
		"the third dispatch is A's instance again")
	require.Equal(t, 2, fake.devSessions[2].DispatchCount)
}
