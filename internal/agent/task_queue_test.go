package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serialProbe records how many tasks of one Team ran at the same time.
type serialProbe struct {
	mu        sync.Mutex
	active    int
	maxActive int
	finished  []string
	duration  time.Duration
}

func (p *serialProbe) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	p.mu.Lock()
	p.active++
	if p.active > p.maxActive {
		p.maxActive = p.active
	}
	p.mu.Unlock()

	time.Sleep(p.duration)

	p.mu.Lock()
	p.active--
	if id, _ := args["task_id"].(string); id != "" {
		p.finished = append(p.finished, id)
	}
	p.mu.Unlock()
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

func (p *serialProbe) peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maxActive
}

// TestAsyncExecutorSerializesOneTeamInstance covers the queue a resident Team
// needs: a second task for the same instance waits instead of failing, runs
// after the first, and both finish.
//
// The failure this guards is a refusal. "team X is already running" arriving as
// a failed task tells the caller the work did not happen, when the only thing
// that happened is that it had to wait.
func TestAsyncExecutorSerializesOneTeamInstance(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	probe := &serialProbe{duration: 60 * time.Millisecond}
	async := NewAsyncToolExecutor(store, probe)

	for _, id := range []string{"task-1", "task-2"} {
		require.NoError(t, async.Start(context.Background(), types.ToolTask{
			ID:            id,
			FlowSessionID: "fs_queue",
			TeamID:        "frontdesk",
			ToolCallID:    "call-" + id,
			ToolName:      "TeamRun",
			Arguments:     map[string]any{"team_id": "worker", "task_id": id},
		}))
	}

	require.Eventually(t, func() bool {
		done := 0
		for _, id := range []string{"task-1", "task-2"} {
			task, err := store.Load(context.Background(), id)
			if err == nil && task.Status == types.ToolTaskCompleted {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 20*time.Millisecond, "both queued tasks must finish")

	assert.Equal(t, 1, probe.peak(), "one Team instance runs one task at a time")
}

// TestAsyncExecutorRunsPerTaskKeysConcurrently covers the other half: two
// task_key values are two instances of a per_task Team, so they must not wait
// for each other.
func TestAsyncExecutorRunsPerTaskKeysConcurrently(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	probe := &serialProbe{duration: 120 * time.Millisecond}
	async := NewAsyncToolExecutor(store, probe)

	for _, id := range []string{"task-a", "task-b"} {
		require.NoError(t, async.Start(context.Background(), types.ToolTask{
			ID:            id,
			FlowSessionID: "fs_parallel",
			TeamID:        "frontdesk",
			ToolCallID:    "call-" + id,
			ToolName:      "TeamRun",
			Arguments: map[string]any{
				"team_id": "probe", "task_key": id, "task_id": id,
				"lifecycle": string(types.TeamLifecyclePerTask),
			},
		}))
	}

	require.Eventually(t, func() bool {
		done := 0
		for _, id := range []string{"task-a", "task-b"} {
			task, err := store.Load(context.Background(), id)
			if err == nil && task.Status == types.ToolTaskCompleted {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 20*time.Millisecond, "both per_task instances must finish")

	assert.Equal(t, 2, probe.peak(), "distinct task_key values are distinct instances")
}

// TestAsyncExecutorSerializesResidentTeamWithDistinctTaskKeys covers the
// regression that made two dispatches collide: a model sent a different
// task_key to a resident Team, the queue read the two keys as two instances and
// started both, and the second was then refused by the single execution slot
// behind them — the caller was told its task failed, when the only thing that
// happened is that it had to wait.
//
// A resident Team has one instance per session no matter what the caller wrote
// in task_key, so the queue must serialize on the Team alone.
func TestAsyncExecutorSerializesResidentTeamWithDistinctTaskKeys(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	probe := &serialProbe{duration: 60 * time.Millisecond}
	async := NewAsyncToolExecutor(store, probe)

	for _, id := range []string{"task-x", "task-y"} {
		require.NoError(t, async.Start(context.Background(), types.ToolTask{
			ID:            id,
			FlowSessionID: "fs_resident",
			TeamID:        "frontdesk",
			ToolCallID:    "call-" + id,
			ToolName:      "TeamRun",
			Arguments: map[string]any{
				"team_id": "worker", "task_key": id, "task_id": id,
				"lifecycle": string(types.TeamLifecycleResident),
			},
		}))
	}

	require.Eventually(t, func() bool {
		done := 0
		for _, id := range []string{"task-x", "task-y"} {
			task, err := store.Load(context.Background(), id)
			if err == nil && task.Status == types.ToolTaskCompleted {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 20*time.Millisecond, "both resident tasks must finish, one after the other")

	assert.Equal(t, 1, probe.peak(), "a resident Team runs one task at a time however the caller keyed it")
}

// TestAsyncExecutorRunsDifferentTeamsConcurrently guards the boundary: two
// callees under the same caller must not share a slot.
func TestAsyncExecutorRunsDifferentTeamsConcurrently(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewFileToolTaskStore(files)
	probe := &serialProbe{duration: 120 * time.Millisecond}
	async := NewAsyncToolExecutor(store, probe)

	for id, team := range map[string]string{"task-x": "worker", "task-y": "worker-b"} {
		require.NoError(t, async.Start(context.Background(), types.ToolTask{
			ID:            id,
			FlowSessionID: "fs_two_teams",
			TeamID:        "frontdesk",
			ToolCallID:    "call-" + id,
			ToolName:      "TeamRun",
			Arguments:     map[string]any{"team_id": team, "task_id": id},
		}))
	}

	require.Eventually(t, func() bool {
		done := 0
		for _, id := range []string{"task-x", "task-y"} {
			task, err := store.Load(context.Background(), id)
			if err == nil && task.Status == types.ToolTaskCompleted {
				done++
			}
		}
		return done == 2
	}, 5*time.Second, 20*time.Millisecond)

	assert.Equal(t, 2, probe.peak(), "different callees run in parallel")
}
