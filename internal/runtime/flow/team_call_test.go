package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// memoryTaskStore is the minimum the pending-call check needs.
type memoryTaskStore struct {
	tasks []types.ToolTask
}

func (s *memoryTaskStore) Save(_ context.Context, task types.ToolTask) error {
	s.tasks = append(s.tasks, task)
	return nil
}

func (s *memoryTaskStore) Load(_ context.Context, id string) (*types.ToolTask, error) {
	for i := range s.tasks {
		if s.tasks[i].ID == id {
			task := s.tasks[i]
			return &task, nil
		}
	}
	return nil, nil
}

func (s *memoryTaskStore) List(_ context.Context) ([]types.ToolTask, error) {
	return s.tasks, nil
}

func (s *memoryTaskStore) Delete(_ context.Context, _ string) error { return nil }

// "这轮在等下游"和"这轮给了结论"只能由引擎判断：它看得到还有没有未完成的 Team
// 调用。客户端如果按 origin 或文本猜，会把最终结论也当成进度。
func TestHasPendingTeamCall(t *testing.T) {
	store := &memoryTaskStore{}
	rt := &Runtime{}
	rt.SetTaskStore(store)

	require.False(t, rt.hasPendingTeamCall(context.Background(), "fs-1"),
		"没有任务时不算等待")

	store.tasks = []types.ToolTask{
		{ID: "t1", FlowSessionID: "fs-1", ToolName: types.ToolNameTeamRun, Status: types.ToolTaskRunning},
	}
	require.True(t, rt.hasPendingTeamCall(context.Background(), "fs-1"),
		"运行中的 Team 调用说明本轮是进度")

	store.tasks = []types.ToolTask{
		{ID: "t1", FlowSessionID: "fs-1", ToolName: types.ToolNameTeamRun, Status: types.ToolTaskCompleted},
	}
	require.False(t, rt.hasPendingTeamCall(context.Background(), "fs-1"),
		"已完成时本轮就是结论")

	store.tasks = []types.ToolTask{
		{ID: "t2", FlowSessionID: "fs-other", ToolName: types.ToolNameTeamRun, Status: types.ToolTaskRunning},
	}
	require.False(t, rt.hasPendingTeamCall(context.Background(), "fs-1"),
		"别的会话的任务不影响本会话")
}
