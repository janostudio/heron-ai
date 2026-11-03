package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func newPerTaskRuntimeWithLimits(t *testing.T, limits types.RuntimeLimits) (*Runtime, *fakeTeamRuntime) {
	t.Helper()
	defs := perTaskDefinitions()
	defs.Limits = limits
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	fake := &fakeTeamRuntime{}
	rt := newTestRuntime(defs, fake, writer, nil)
	return rt, fake
}

// TestCloseTaskCascadesAndReopenKeepsTheInstance is the P1 lifecycle contract:
// closing is soft (status + timestamp, nothing deleted), and dispatching the
// same task_key afterwards reopens the same instance rather than creating a new
// one.
func TestCloseTaskCascadesAndReopenKeepsTheInstance(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	first := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, "done", first.Status)
	spec := types.TaskCatalogSpec{FlowSessionID: session.ID, CallerTeamSessionID: "fs:default"}

	closed, err := rt.CloseTask(ctx, spec, "repoA-143")
	require.NoError(t, err)
	require.Equal(t, types.SessionClosed, closed.Status)

	info, err := rt.GetTask(ctx, spec, "repoA-143")
	require.NoError(t, err)
	require.Equal(t, types.SessionClosed, info.Status)

	reopened := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, first.TeamSessionID, reopened.TeamSessionID,
		"reopening must reuse the instance, not create a new one")
	require.True(t, reopened.Reopened)

	info, err = rt.GetTask(ctx, spec, "repoA-143")
	require.NoError(t, err)
	require.Equal(t, 1, info.ReopenCount)
	require.Equal(t, types.SessionCompleted, info.Status,
		"the reopened instance ran again and finished")
}

// TestCloseTaskRejectsForeignInstance keeps closing behind the same ownership
// rule as continuing.
func TestCloseTaskRejectsForeignInstance(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	spec := types.TaskCatalogSpec{FlowSessionID: session.ID, CallerTeamSessionID: "fs:someone-else"}
	_, err := rt.CloseTask(ctx, spec, "repoA-143")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found")
}

// TestActiveTaskLimitBoundsInFlightWork pins the ceiling: closed tasks do not
// count, so a session that finishes its work can keep dispatching.
func TestActiveTaskLimitBoundsInFlightWork(t *testing.T) {
	rt, _ := newPerTaskRuntimeWithLimits(t, types.RuntimeLimits{MaxActiveTeamSessions: 1})
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	second := dispatchTask(t, rt, session.ID, "repoB-207", "fs:default")
	require.Equal(t, "failed", second.Status)
	require.Contains(t, second.Error, "limit 1")

	spec := types.TaskCatalogSpec{FlowSessionID: session.ID, CallerTeamSessionID: "fs:default"}
	_, err := rt.CloseTask(ctx, spec, "repoA-143")
	require.NoError(t, err)

	third := dispatchTask(t, rt, session.ID, "repoB-207", "fs:default")
	require.Equal(t, "done", third.Status,
		"a closed task must free its slot")
}

// TestTaskDepthLimitStopsRunawayNesting pins the second ceiling: a task may
// hand work to a sub-task, but only so deep.
func TestTaskDepthLimitStopsRunawayNesting(t *testing.T) {
	rt, _ := newPerTaskRuntimeWithLimits(t, types.RuntimeLimits{MaxTaskContextDepth: 1})
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	first := dispatchTask(t, rt, session.ID, "repoA-143", "fs:default")
	require.Equal(t, "done", first.Status)

	// The first task instance now dispatches a sub-task: depth 2 > limit 1.
	nested := dispatchTask(t, rt, session.ID, "sub-1", first.TeamSessionID)
	require.Equal(t, "failed", nested.Status)
	require.Contains(t, nested.Error, "nesting depth 2")
}

// TestAuthorizationIsFrozen: a task keeps the permission it was created with.
// Continuing it may repeat that grant, never widen it.
func TestAuthorizationIsFrozen(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)

	grant := &types.AuthorizationGrant{Scopes: []string{"read"}}
	outcome, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "dev",
		TaskKey:       "repoA-143",
		DispatchedBy:  &types.TeamSessionRef{TeamSessionID: "fs:default"},
		Authorization: grant,
	})
	require.NoError(t, err)
	require.Equal(t, "done", outcome.Status)

	wider := &types.AuthorizationGrant{Scopes: []string{"read", "push"}}
	escalated, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "dev",
		TaskKey:       "repoA-143",
		DispatchedBy:  &types.TeamSessionRef{TeamSessionID: "fs:default"},
		Authorization: wider,
	})
	require.NoError(t, err)
	require.Equal(t, "failed", escalated.Status)
	require.Contains(t, escalated.Error, "authorization")

	same := &types.AuthorizationGrant{Scopes: []string{"read"}}
	again, err := rt.RunTeam(context.Background(), types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "dev",
		TaskKey:       "repoA-143",
		DispatchedBy:  &types.TeamSessionRef{TeamSessionID: "fs:default"},
		Authorization: same,
	})
	require.NoError(t, err)
	require.Equal(t, "done", again.Status)
}

// TestPendingUserInputIsVisible pins the signal the scheduler uses to let a
// person's follow-up run before more work is started.
func TestPendingUserInputIsVisible(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, turn := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	require.False(t, rt.hasPendingUserInput(ctx, session.ID))
	msg, err := rt.enqueueMessage(ctx, session, turn, addr("default", ""), mailboxKindUser, map[string]any{"input": "hello"})
	require.NoError(t, err)
	require.True(t, rt.hasPendingUserInput(ctx, session.ID))

	_, err = rt.claimMessage(ctx, session, msg)
	require.NoError(t, err)
	require.False(t, rt.hasPendingUserInput(ctx, session.ID))
}

// TestSubscriptionDeliveryIsDeduped: a subscribed source event reaches the task
// once; a retry with the same event id does not run it again.
func TestSubscriptionDeliveryIsDeduped(t *testing.T) {
	rt, _ := newPerTaskRuntime(t)
	session, _ := mailboxTestSession(t)
	writeFlowSession(t, rt, session)
	ctx := context.Background()

	outcome, err := rt.RunTeam(ctx, types.TeamCallSpec{
		FlowSessionID: session.ID,
		TeamID:        "dev",
		TaskKey:       "repoA-143",
		DispatchedBy:  &types.TeamSessionRef{TeamSessionID: "fs:default"},
		Subscriptions: []string{"issue-143"},
	})
	require.NoError(t, err)
	require.Equal(t, "done", outcome.Status)

	tasks, err := rt.FindTasksBySubscription(ctx, session.ID, "issue-143")
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	require.Equal(t, "repoA-143", tasks[0].TaskKey)

	target, err := rt.DeliverSubscriptionEvent(ctx, session.ID, "issue-143", "evt-1", "new comment")
	require.NoError(t, err)
	require.Equal(t, "repoA-143", target.TaskKey)

	// Same event id again: no second dispatch.
	_, err = rt.DeliverSubscriptionEvent(ctx, session.ID, "issue-143", "evt-1", "new comment")
	require.NoError(t, err)
	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, box, 1, "a retried event must not be delivered twice")

	_, err = rt.DeliverSubscriptionEvent(ctx, session.ID, "issue-143", "evt-2", "another comment")
	require.NoError(t, err)
	box, err = rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, box, 2)

	_, err = rt.DeliverSubscriptionEvent(ctx, session.ID, "issue-999", "evt-3", "unrelated")
	require.Error(t, err)
}
