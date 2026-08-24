package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type batchToolMock struct {
	mu        sync.Mutex
	active    int
	maxActive int
	classes   map[string]types.ToolExecutionSpec
}

func (m *batchToolMock) Execute(_ context.Context, name string, _ map[string]any) (*types.ToolResult, error) {
	m.mu.Lock()
	m.active++
	if m.active > m.maxActive {
		m.maxActive = m.active
	}
	m.mu.Unlock()
	time.Sleep(15 * time.Millisecond)
	m.mu.Lock()
	m.active--
	m.mu.Unlock()
	return &types.ToolResult{Success: true, Content: name}, nil
}

func (m *batchToolMock) ExecutionSpec(name string) types.ToolExecutionSpec {
	return m.classes[name]
}

func TestToolBatchExecutorRunsReadOnlyInParallelAndPreservesOrder(t *testing.T) {
	mock := &batchToolMock{
		classes: map[string]types.ToolExecutionSpec{
			"ReadA": {Class: types.ToolReadOnly},
			"ReadB": {Class: types.ToolReadOnly},
			"Write": {Class: types.ToolSerial},
		},
	}
	executor := NewToolBatchExecutor(mock, 2, true)
	results := executor.Execute(context.Background(), []types.ToolCall{
		{ID: "1", Name: "ReadA"},
		{ID: "2", Name: "ReadB"},
		{ID: "3", Name: "Write"},
	})

	require.Len(t, results, 3)
	require.Equal(t, "ReadA", results[0].Content)
	require.Equal(t, "ReadB", results[1].Content)
	require.Equal(t, "Write", results[2].Content)
	require.Equal(t, 2, mock.maxActive)
}

func TestToolBatchExecutorSerialModeDoesNotOverlap(t *testing.T) {
	mock := &batchToolMock{
		classes: map[string]types.ToolExecutionSpec{
			"ReadA": {Class: types.ToolReadOnly},
			"ReadB": {Class: types.ToolReadOnly},
		},
	}
	executor := NewToolBatchExecutor(mock, 5, false)
	executor.Execute(context.Background(), []types.ToolCall{
		{ID: "1", Name: "ReadA"},
		{ID: "2", Name: "ReadB"},
	})
	require.Equal(t, 1, mock.maxActive)
}
