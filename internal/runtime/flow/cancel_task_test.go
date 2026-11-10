package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// recordingTaskCanceller records which task ids were cancelled.
type recordingTaskCanceller struct {
	mu  sync.Mutex
	ids []string
}

func (c *recordingTaskCanceller) Cancel(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ids = append(c.ids, id)
	return nil
}

func (c *recordingTaskCanceller) cancelled() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.ids...)
}

func newCancelFixture(t *testing.T) (*Runtime, *agent.FileToolTaskStore, *recordingTaskCanceller, types.FlowSession) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)

	session := types.FlowSession{
		ID:        "fs_cancel_tasks",
		FlowID:    "code-fix",
		Status:    types.SessionWaitingTool,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	appendSessionStatus(t, writer, session)

	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, storage.NewJSONLEvidenceStore(files))
	runtime.SetTaskStore(tasks)
	control := &recordingTaskCanceller{}
	runtime.SetTaskControl(control)
	return runtime, tasks, control, session
}

// A durable Task runs on its own context, so cancelling the turn never reaches
// it. This is the half Cancel has to do by id.
func TestCancelStopsSessionToolTasks(t *testing.T) {
	ctx := context.Background()
	runtime, tasks, control, session := newCancelFixture(t)

	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "t-running", FlowSessionID: session.ID, Status: types.ToolTaskRunning,
		UpdatedAt: time.Now().UTC(),
	}))
	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "t-queued", FlowSessionID: session.ID, Status: types.ToolTaskQueued,
		UpdatedAt: time.Now().UTC(),
	}))
	// Already finished: nothing to stop, and re-cancelling would rewrite a
	// result that already landed.
	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "t-done", FlowSessionID: session.ID, Status: types.ToolTaskCompleted,
		UpdatedAt: time.Now().UTC(),
	}))
	// Another conversation's work must not be touched.
	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "t-other", FlowSessionID: "fs_someone_else", Status: types.ToolTaskRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	require.NoError(t, runtime.Cancel(ctx, session.ID))

	cancelled := control.cancelled()
	require.ElementsMatch(t, []string{"t-running", "t-queued"}, cancelled)
}

// A runtime built without the canceller still cancels what it can. It must not
// panic, and it must not claim the tasks were stopped.
func TestCancelWithoutTaskControlIsSafe(t *testing.T) {
	ctx := context.Background()
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	tasks := agent.NewFileToolTaskStore(files)
	session := types.FlowSession{
		ID: "fs_cancel_no_control", FlowID: "code-fix", Status: types.SessionWaitingTool,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	appendSessionStatus(t, writer, session)
	require.NoError(t, tasks.Save(ctx, types.ToolTask{
		ID: "t-1", FlowSessionID: session.ID, Status: types.ToolTaskRunning,
		UpdatedAt: time.Now().UTC(),
	}))

	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, storage.NewJSONLEvidenceStore(files))
	runtime.SetTaskStore(tasks)

	require.NoError(t, runtime.Cancel(ctx, session.ID))
}
