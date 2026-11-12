package flow

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ledgerTaskStore is the durable task store without a filesystem: the ledger
// reads it and nothing else, so a map is enough.
type ledgerTaskStore struct {
	mu    sync.Mutex
	tasks map[string]types.ToolTask
}

func newLedgerTaskStore() *ledgerTaskStore {
	return &ledgerTaskStore{tasks: map[string]types.ToolTask{}}
}

func (s *ledgerTaskStore) Save(_ context.Context, task types.ToolTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks[task.ID] = task
	return nil
}

func (s *ledgerTaskStore) Load(_ context.Context, id string) (*types.ToolTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return nil, types.ErrToolTaskNotFound
	}
	return &task, nil
}

func (s *ledgerTaskStore) List(_ context.Context) ([]types.ToolTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]types.ToolTask, 0, len(s.tasks))
	for _, task := range s.tasks {
		out = append(out, task)
	}
	return out, nil
}

func (s *ledgerTaskStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tasks, id)
	return nil
}

func newLedgerRuntime(t *testing.T) (*Runtime, *ledgerTaskStore) {
	t.Helper()
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	store := newLedgerTaskStore()
	rt.SetTaskStore(store)
	return rt, store
}

func dispatchedTeamTask(id, caller, team, goal string, status types.ToolTaskStatus) types.ToolTask {
	return types.ToolTask{
		ID:            id,
		FlowSessionID: "fs-ledger",
		ToolName:      types.ToolNameTeamRun,
		Status:        status,
		CreatedAt:     time.Now().UTC().Add(-5 * time.Minute),
		Arguments: map[string]any{
			"team_id":  team,
			"item":     goal,
			"task_key": team + "-1",
			"dispatched_by": map[string]any{
				"team_session_id": caller,
			},
		},
	}
}

// 任务清单要回答的是"手上还有什么活"：在跑的、以及跑完但还没总结的。
func TestTaskLedgerListsRunningAndPendingSummary(t *testing.T) {
	rt, store := newLedgerRuntime(t)

	require.NoError(t, store.Save(context.Background(),
		dispatchedTeamTask("tt-a", "fs-ledger:frontdesk", "probe",
			"查 CLS 最近一个自然日的请求总数与均值 QPS\n口径：…", types.ToolTaskRunning)))
	require.NoError(t, store.Save(context.Background(),
		dispatchedTeamTask("tt-b", "fs-ledger:frontdesk", "dev",
			"修 heron-ai#61 的并发派发失败", types.ToolTaskCompleted)))

	ledger := rt.TaskLedger(context.Background(), "fs-ledger", "fs-ledger:frontdesk")

	require.Contains(t, ledger, "[任务清单]")
	require.Contains(t, ledger, "查 CLS 最近一个自然日的请求总数与均值 QPS",
		"目标只取首行：整段任务单不该进清单")
	require.NotContains(t, ledger, "口径：", "首行之后的内容不进清单")
	require.Contains(t, ledger, "修 heron-ai#61 的并发派发失败")
	require.Contains(t, ledger, "handle=tt-a")
	require.Contains(t, ledger, "handle=tt-b")

	// 已完成待总结的排在前面：它是唯一需要调用方动手的。
	done := strings.Index(ledger, "handle=tt-b")
	running := strings.Index(ledger, "handle=tt-a")
	require.GreaterOrEqual(t, running, 0)
	require.Less(t, done, running, "待总结的任务应排在运行中的任务之前")
}

// 已经总结过的任务必须消失，否则模型会把同一件事反复总结。
func TestTaskLedgerDropsAcknowledgedTasks(t *testing.T) {
	rt, store := newLedgerRuntime(t)

	task := dispatchedTeamTask("tt-done", "fs-ledger:frontdesk", "dev",
		"修 heron-ai#61", types.ToolTaskCompleted)
	task.Acknowledged = true
	require.NoError(t, store.Save(context.Background(), task))

	require.Empty(t, rt.TaskLedger(context.Background(), "fs-ledger", "fs-ledger:frontdesk"),
		"没有未收尾的任务时不注入清单，不占上下文")
}

// 一个实例只看得到自己派出去的活。
func TestTaskLedgerScopesToTheCaller(t *testing.T) {
	rt, store := newLedgerRuntime(t)

	require.NoError(t, store.Save(context.Background(),
		dispatchedTeamTask("tt-mine", "fs-ledger:frontdesk", "probe", "我的任务", types.ToolTaskRunning)))
	require.NoError(t, store.Save(context.Background(),
		dispatchedTeamTask("tt-other", "fs-ledger:dev-lead", "probe", "别人的任务", types.ToolTaskRunning)))

	ledger := rt.TaskLedger(context.Background(), "fs-ledger", "fs-ledger:frontdesk")

	require.Contains(t, ledger, "handle=tt-mine")
	require.NotContains(t, ledger, "handle=tt-other", "别的实例派出的任务不属于这个调用方")
}

// 非 Team 派发的异步任务（spawn 子任务等）不属于协作清单。
func TestTaskLedgerIgnoresNonTeamTasks(t *testing.T) {
	rt, store := newLedgerRuntime(t)

	spawned := dispatchedTeamTask("tt-spawn", "fs-ledger:frontdesk", "", "子任务", types.ToolTaskRunning)
	spawned.ToolName = "Spawn"
	require.NoError(t, store.Save(context.Background(), spawned))

	require.Empty(t, rt.TaskLedger(context.Background(), "fs-ledger", "fs-ledger:frontdesk"))
}

// 收尾之后任务离开清单，且这一步是幂等的（回调可能重复触发）。
func TestAcknowledgeTaskRetiresItFromTheLedger(t *testing.T) {
	rt, store := newLedgerRuntime(t)
	ctx := context.Background()

	require.NoError(t, store.Save(ctx,
		dispatchedTeamTask("tt-b", "fs-ledger:frontdesk", "dev", "修 heron-ai#61", types.ToolTaskCompleted)))

	require.NoError(t, rt.acknowledgeTask(ctx, "tt-b"))
	require.Empty(t, rt.TaskLedger(ctx, "fs-ledger", "fs-ledger:frontdesk"))

	stored, err := store.Load(ctx, "tt-b")
	require.NoError(t, err)
	require.True(t, stored.Acknowledged)

	require.NoError(t, rt.acknowledgeTask(ctx, "tt-b"), "重复标记不应报错")
}

// 已完成的任务要把结论带在行里：否则"这活干完没有、结果是什么"只能靠读
// 任务记录文件回答，每个任务一次文件往返。
func TestTaskLedgerCarriesConclusionForFinishedTask(t *testing.T) {
	rt, store := newLedgerRuntime(t)

	task := dispatchedTeamTask("tt-c", "fs-ledger:frontdesk", "dev-lead",
		"查 heron-ai 仓库的主分支", types.ToolTaskCompleted)
	task.Result = &types.ToolResult{
		Success: true,
		Content: "master（依据 origin/HEAD → refs/remotes/origin/master）\n更多细节略",
	}
	require.NoError(t, store.Save(context.Background(), task))

	ledger := rt.TaskLedger(context.Background(), "fs-ledger", "fs-ledger:frontdesk")

	require.Contains(t, ledger, "结果：master", "结论进清单，模型不必再翻任务记录")
	require.NotContains(t, ledger, "更多细节略", "只取结论首行")
}
