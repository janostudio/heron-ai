package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// A Team parked on a Tool whose task record is gone can never continue: resume
// loads the task to decide, and a missing record makes it fail. Before this, the
// session had no path at all — resume failed, HandleInput said "resume the
// pending Tool task", and recovery reported nothing to recover. Reporting it as
// interrupted is what gives it one.
func TestOrphanedWaitingTeamIsReportedAsInterrupted(t *testing.T) {
	ctx := context.Background()
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)
	// The fixture Team waits on task-a and task-b; only task-a exists.
	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "task-a", Status: types.ToolTaskRunning, UpdatedAt: time.Now().UTC(),
	}))
	runtime := newTestRuntime(aggregateResumeDefinitions(), &aggregateResumeTeamRuntime{}, sessions, nil)
	runtime.SetTaskStore(tasks)

	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "aggregate"})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingTool, first.Session.Status)

	status, err := runtime.RecoveryStatus(ctx, first.Session.ID)
	require.NoError(t, err)
	require.NotEmpty(t, status.Interrupted,
		"a waiting turn whose task is gone must be recoverable, not silently stuck")
	require.Equal(t, "team_turn", status.Interrupted[0].Kind)
	require.True(t, status.Interrupted[0].SafeToRetry)
	// The reason has to name what is missing: "recovery" alone does not tell an
	// operator whether the task is lost or merely slow.
	require.Contains(t, status.Interrupted[0].RetryReason, "task-b")
	require.Equal(t, types.SessionInterrupted, status.Session.Status)

	// HandleInput must point at recovery, not at a resume that cannot succeed.
	_, err = runtime.HandleInput(ctx, first.Session.ID, "continue anyway")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unfinished execution")
}

// The same session with every pending task present is resumable, and must not be
// reported as interrupted: this is the case the waiting state exists for.
func TestWaitingTeamWithLiveTasksIsNotInterrupted(t *testing.T) {
	ctx := context.Background()
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)
	for _, id := range []string{"task-a", "task-b"} {
		require.NoError(t, tasks.Save(ctx, types.ToolTask{
			ID: id, Status: types.ToolTaskRunning, UpdatedAt: time.Now().UTC(),
		}))
	}
	runtime := newTestRuntime(aggregateResumeDefinitions(), &aggregateResumeTeamRuntime{}, sessions, nil)
	runtime.SetTaskStore(tasks)

	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "aggregate"})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingTool, first.Session.Status)

	status, err := runtime.RecoveryStatus(ctx, first.Session.ID)
	require.NoError(t, err)
	require.Empty(t, status.Interrupted)
	require.Equal(t, types.SessionWaitingTool, status.Session.Status)
}

// An unreadable task store is not evidence the task is gone. Reporting a
// transient IO failure as "your session is unrecoverable" would be worse than
// reporting nothing, so no interrupted entry is produced.
func TestUnreadableTaskStoreIsNotOrphaned(t *testing.T) {
	ctx := context.Background()
	files := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(files)
	runtime := newTestRuntime(aggregateResumeDefinitions(), &aggregateResumeTeamRuntime{}, sessions, nil)
	runtime.SetTaskStore(failingTaskStore{})

	first, err := runtime.Start(ctx, types.StartFlowRequest{FlowID: "aggregate"})
	require.NoError(t, err)

	status, err := runtime.RecoveryStatus(ctx, first.Session.ID)
	require.NoError(t, err)
	require.Empty(t, status.Interrupted)
}

// failingTaskStore answers every read with a plain error, standing in for a
// store that cannot be read rather than one that says "no such task".
type failingTaskStore struct{}

func (failingTaskStore) Save(context.Context, types.ToolTask) error { return nil }
func (failingTaskStore) Load(context.Context, string) (*types.ToolTask, error) {
	return nil, context.DeadlineExceeded
}
func (failingTaskStore) List(context.Context) ([]types.ToolTask, error) { return nil, nil }
func (failingTaskStore) Delete(context.Context, string) error           { return nil }
