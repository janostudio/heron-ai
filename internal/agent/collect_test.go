package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func newToolRegistryWith(t *testing.T, tools ...types.Tool) *tool.ToolRegistry {
	t.Helper()
	registry := tool.NewToolRegistry()
	for _, registered := range tools {
		registry.Register(registered)
	}
	return registry
}

func newRegistryExecutorWith(t *testing.T, registry *tool.ToolRegistry) *tool.ToolExecutor {
	t.Helper()
	return tool.NewToolExecutor(registry)
}

func newCollectFixture(t *testing.T) (*spawnFixture, *CollectTool) {
	t.Helper()
	fixture := newAsyncSpawnFixture(t)
	return fixture, NewCollectTool(fixture.taskRunner)
}

// asyncSpawnOne starts one async child with the given key and returns its
// task id.
func asyncSpawnOne(t *testing.T, fixture *spawnFixture, key string) string {
	t.Helper()
	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "item-for-" + key, "key": key, "wait": false,
	})
	require.NoError(t, err)
	require.True(t, result.Success)
	handles := fixture.spawnHandles(t, result)
	require.Len(t, handles, 1)
	id, _ := handles[0]["task_id"].(string)
	require.NotEmpty(t, id)
	return id
}

func TestCollectTool_RequiresHandles(t *testing.T) {
	_, collect := newCollectFixture(t)

	result, err := collect.Execute(context.Background(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires handles")

	result, err = collect.Execute(context.Background(), map[string]any{"handles": []any{}})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "must not be empty")

	result, err = collect.Execute(context.Background(), map[string]any{"handles": "turn-1:spawn:k:1"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "must be an array")
}

func TestCollectTool_HandleForms(t *testing.T) {
	_, collect := newCollectFixture(t)

	result, err := collect.Execute(context.Background(), map[string]any{
		"handles": []any{map[string]any{"task_id": "x", "key": "k"}},
	})
	require.NoError(t, err)
	assert.False(t, result.Success) // unknown task id, but the form is accepted

	result, err = collect.Execute(context.Background(), map[string]any{
		"handles": []any{map[string]any{"key": "k"}},
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "missing task_id")
}

func TestCollectTool_NotConfigured(t *testing.T) {
	collect := NewCollectTool(nil)

	result, err := collect.Execute(context.Background(), map[string]any{"handles": []any{"x"}})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not configured")
}

func TestCollectTool_CompletedHandleReturnsImmediately(t *testing.T) {
	fixture, collect := newCollectFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "quick child"}, nil
	}
	taskID := asyncSpawnOne(t, fixture, "k1")
	fixture.waitForTask(t, taskID)

	result, err := collect.Execute(context.Background(), map[string]any{
		"handles": []any{taskID},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, taskID, entries[0]["task_id"])
	assert.Equal(t, "k1", entries[0]["key"])
	assert.Equal(t, "completed", entries[0]["status"])
	assert.Equal(t, "quick child", entries[0]["reply"])
}

func TestCollectTool_BlocksUntilChildrenFinish(t *testing.T) {
	fixture, collect := newCollectFixture(t)
	release := make(chan struct{})
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		<-release
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "slow child reply"}, nil
	}
	taskID := asyncSpawnOne(t, fixture, "k1")

	type collectOutcome struct {
		result *types.ToolResult
		err    error
	}
	done := make(chan collectOutcome, 1)
	go func() {
		result, err := collect.Execute(context.Background(), map[string]any{
			"handles": []any{taskID},
		})
		done <- collectOutcome{result: result, err: err}
	}()

	// Collect is still waiting while the child runs.
	select {
	case outcome := <-done:
		t.Fatalf("Collect returned before the child finished: %+v", outcome)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case outcome := <-done:
		require.NoError(t, outcome.err)
		require.NotNil(t, outcome.result)
		assert.True(t, outcome.result.Success)
		var entries []map[string]any
		require.NoError(t, json.Unmarshal([]byte(outcome.result.Content), &entries))
		require.Len(t, entries, 1)
		assert.Equal(t, "slow child reply", entries[0]["reply"])
	case <-time.After(5 * time.Second):
		t.Fatal("Collect did not return after the child finished")
	}
}

func TestCollectTool_PerHandleErrorsDoNotFailCollect(t *testing.T) {
	fixture, collect := newCollectFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		if call.req.CallID == "call-1/bad" {
			return &types.AgentResult{Status: types.TurnFailed, Error: "child exploded"}, nil
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "good child"}, nil
	}

	good := asyncSpawnOne(t, fixture, "good")
	bad := asyncSpawnOne(t, fixture, "bad")
	fixture.waitForTask(t, good)
	fixture.waitForTask(t, bad)

	result, err := collect.Execute(context.Background(), map[string]any{
		"handles": []any{good, bad},
	})
	require.NoError(t, err)
	// Collecting succeeded even though one child failed: the failure is a
	// per-handle entry, not a tool failure.
	assert.True(t, result.Success)

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &entries))
	require.Len(t, entries, 2)
	assert.Equal(t, "good", entries[0]["key"])
	assert.Equal(t, "good child", entries[0]["reply"])
	assert.Equal(t, "bad", entries[1]["key"])
	assert.Equal(t, "child exploded", entries[1]["error"])
	assert.NotContains(t, entries[1], "reply")
}

func TestCollectTool_UnknownHandleFails(t *testing.T) {
	_, collect := newCollectFixture(t)

	result, err := collect.Execute(context.Background(), map[string]any{
		"handles": []any{"no-such-task"},
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "1 of 1")

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &entries))
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0]["error"], "not found")
}

func TestCollectTool_ContextCancelledWhileWaiting(t *testing.T) {
	fixture, collect := newCollectFixture(t)
	release := make(chan struct{})
	defer close(release)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		<-release
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "never"}, nil
	}
	taskID := asyncSpawnOne(t, fixture, "k1")

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	result, err := collect.Execute(ctx, map[string]any{
		"handles": []any{taskID},
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "could not be resolved")

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &entries))
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0]["error"], "context")
}

func TestCollectTool_DurableAcrossExecutorLifetimes(t *testing.T) {
	// A Collect in a later parent turn (or a new process) resolves tasks from
	// the durable store with a fresh executor instance.
	fixture, _ := newCollectFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "durable child"}, nil
	}
	taskID := asyncSpawnOne(t, fixture, "k1")
	fixture.waitForTask(t, taskID)

	freshRunner := NewAsyncToolExecutor(fixture.taskStore, NewSpawnTaskDispatcher(fixture.spawn, nil))
	freshCollect := NewCollectTool(freshRunner)
	result, err := freshCollect.Execute(context.Background(), map[string]any{
		"handles": []any{taskID},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	var entries []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &entries))
	require.Len(t, entries, 1)
	assert.Equal(t, "durable child", entries[0]["reply"])
}

func TestCollectTool_ToolContract(t *testing.T) {
	_, collect := newCollectFixture(t)

	assert.Equal(t, "Collect", collect.Name())
	assert.False(t, collect.NeedsApproval())
	assert.Equal(t, types.ToolExecutionSpec{Class: types.ToolSerial}, collect.Execution())
	params := collect.Parameters()
	require.Contains(t, params, "handles")
}

// spawnCollectModel drives the parent TurnLoop through an asynchronous Spawn
// followed by a Collect of the returned handle, parsing the handle out of the
// Spawn tool result in the conversation.
type spawnCollectModel struct {
	mockModelProvider
	mu       sync.Mutex
	round    int
	messages []types.Message
	tools    []types.JSONSchema
}

func (m *spawnCollectModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.round++
	m.messages = messages
	m.tools = tools
	switch m.round {
	case 1:
		return &types.ChatResponse{ToolCalls: []types.ToolCall{{
			ID:        "tc-1",
			Name:      "Spawn",
			Arguments: map[string]any{"item": "task-a", "key": "role-a", "wait": false},
		}}}, nil
	case 2:
		handle := spawnHandleFromMessages(tMessages(messages))
		return &types.ChatResponse{ToolCalls: []types.ToolCall{{
			ID:        "tc-2",
			Name:      "Collect",
			Arguments: map[string]any{"handles": []any{handle}},
		}}}, nil
	default:
		return &types.ChatResponse{Text: "collected"}, nil
	}
}

func tMessages(messages []types.Message) []types.Message { return messages }

// spawnHandleFromMessages extracts the first task id from the latest Spawn
// tool result in the conversation (the content carries the JSON handle array
// followed by a Metadata block).
func spawnHandleFromMessages(messages []types.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "tool" {
			continue
		}
		content := messages[i].Content
		if idx := strings.Index(content, "\nMetadata:"); idx >= 0 {
			content = content[:idx]
		}
		var handles []map[string]any
		if json.Unmarshal([]byte(content), &handles) == nil && len(handles) > 0 {
			if id, _ := handles[0]["task_id"].(string); id != "" {
				return id
			}
		}
	}
	return ""
}

func TestTurnLoop_AsyncSpawnThenCollectEndToEnd(t *testing.T) {
	fixture, _ := newCollectFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "child finished task-a"}, nil
	}

	registry := newToolRegistryWith(t, fixture.spawn, NewCollectTool(fixture.taskRunner))
	executor := newRegistryExecutorWith(t, registry)

	model := &spawnCollectModel{}
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "spawn and collect"}}})

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "parent-agent",
		Tools: types.ToolConfig{Builtin: []string{"Spawn", "Collect"}},
		Loop:  types.LoopConfig{MaxRounds: 5},
	}, fixture.parent)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "collected", result.Reply)

	// The model saw both tool schemas.
	var sawSpawn, sawCollect bool
	for _, schema := range model.tools {
		switch schema.Name {
		case "Spawn":
			sawSpawn = true
		case "Collect":
			sawCollect = true
		}
	}
	assert.True(t, sawSpawn, "Spawn schema was not exposed")
	assert.True(t, sawCollect, "Collect schema was not exposed")

	// Exactly one child ran and its reply reached the parent via Collect.
	require.Len(t, fixture.runner.recorded(), 1)
	collectContent := collectResultContent(t, model.messages)
	assert.Contains(t, collectContent, "child finished task-a")
	assert.Contains(t, collectContent, "role-a")
}

// collectResultContent returns the latest Collect tool result content.
func collectResultContent(t *testing.T, messages []types.Message) string {
	t.Helper()
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			return messages[i].Content
		}
	}
	return ""
}

func TestTurnLoop_CollectUndeclaredIsFiltered(t *testing.T) {
	fixture, _ := newCollectFixture(t)

	registry := newToolRegistryWith(t, fixture.spawn, NewCollectTool(fixture.taskRunner))
	executor := newRegistryExecutorWith(t, registry)

	model := &mockModelProvider{responses: []types.ChatResponse{
		{ToolCalls: []types.ToolCall{{
			ID:        "tc-1",
			Name:      "Collect",
			Arguments: map[string]any{"handles": []any{"whatever"}},
		}}},
		{Text: "done"},
	}}
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}})

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "parent-agent",
		// No Collect in builtin: the call must be rejected exactly like any
		// other undeclared tool, with zero task-store interaction.
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, fixture.parent)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, types.TurnCompleted, result.Status)

	tasks, listErr := fixture.taskStore.List(context.Background())
	require.NoError(t, listErr)
	assert.Empty(t, tasks, "undeclared Collect must not touch the task store")
}
