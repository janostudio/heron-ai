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

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// fakeSessionWriter captures agent-level session events emitted for spawned
// child turns.
type fakeSessionWriter struct {
	mu     sync.Mutex
	events []storage.SessionEvent
}

func (w *fakeSessionWriter) Append(ctx context.Context, sessionID string, layer storage.EventLayer, event storage.SessionEvent) (storage.SessionEvent, error) {
	w.mu.Lock()
	w.events = append(w.events, event)
	w.mu.Unlock()
	event.Seq = int64(len(w.events))
	return event, nil
}

func (w *fakeSessionWriter) Replay(ctx context.Context, sessionID string) (*storage.SessionReplay, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return &storage.SessionReplay{SessionID: sessionID, Events: append([]storage.SessionEvent(nil), w.events...)}, nil
}

func (w *fakeSessionWriter) Subscribe(ctx context.Context, sessionID string, afterSeq int64) (<-chan storage.SessionEvent, error) {
	return nil, nil
}

func (w *fakeSessionWriter) recorded() []storage.SessionEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]storage.SessionEvent(nil), w.events...)
}

// newAsyncSpawnFixture wires the Spawn tool with a durable task store and the
// shared AsyncToolExecutor routing SpawnChild through the dispatcher.
func newAsyncSpawnFixture(t *testing.T, options ...SpawnOption) *spawnFixture {
	t.Helper()
	fixture := newSpawnFixture(t, options...)
	taskStore := NewFileToolTaskStore(storage.NewFileStore(t.TempDir()))
	taskRunner := NewAsyncToolExecutor(taskStore, NewSpawnTaskDispatcher(fixture.spawn, nil))
	fixture.spawn.SetTaskRunner(taskRunner)
	fixture.taskRunner = taskRunner
	fixture.taskStore = taskStore
	return fixture
}

func (f *spawnFixture) spawnHandles(t *testing.T, result *types.ToolResult) []map[string]any {
	t.Helper()
	require.NotNil(t, result)
	var handles []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &handles))
	return handles
}

func (f *spawnFixture) waitForTask(t *testing.T, taskID string) types.ToolTask {
	t.Helper()
	var task types.ToolTask
	require.Eventually(t, func() bool {
		loaded, err := f.taskStore.Load(context.Background(), taskID)
		if err != nil {
			return false
		}
		task = *loaded
		return toolTaskTerminal(task.Status)
	}, 5*time.Second, 20*time.Millisecond, "spawn child task did not reach a terminal state")
	return task
}

func TestSpawnTool_AsyncReturnsHandlesImmediately(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	release := make(chan struct{})
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		<-release
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "slow child"}, nil
	}

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "k1", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, false, result.Metadata["wait"])

	handles := fixture.spawnHandles(t, result)
	require.Len(t, handles, 1)
	assert.Equal(t, "k1", handles[0]["key"])
	assert.NotEmpty(t, handles[0]["task_id"])

	// The durable task exists while the parent continues executing.
	task, loadErr := fixture.taskStore.Load(context.Background(), handles[0]["task_id"].(string))
	require.NoError(t, loadErr)
	assert.Equal(t, SpawnChildToolName, task.ToolName)
	assert.Contains(t, []types.ToolTaskStatus{types.ToolTaskQueued, types.ToolTaskRunning}, task.Status)
	assert.Equal(t, "k1", task.Arguments["key"])
	assert.Equal(t, "fs-1", task.FlowSessionID)

	close(release)
	final := fixture.waitForTask(t, handles[0]["task_id"].(string))
	assert.Equal(t, types.ToolTaskCompleted, final.Status)
}

func TestSpawnTool_AsyncItemsOneTaskPerItem(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"items": []any{"one", "two", "three"}, "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	handles := fixture.spawnHandles(t, result)
	require.Len(t, handles, 3)
	taskIDs := map[string]bool{}
	for _, handle := range handles {
		id, _ := handle["task_id"].(string)
		assert.NotEmpty(t, id)
		assert.NotEmpty(t, handle["key"])
		taskIDs[id] = true
	}
	assert.Len(t, taskIDs, 3)

	for _, handle := range handles {
		task := fixture.waitForTask(t, handle["task_id"].(string))
		assert.Equal(t, types.ToolTaskCompleted, task.Status)
	}
	entities, listErr := fixture.registry.List(context.Background(), "parent-agent")
	require.NoError(t, listErr)
	assert.Len(t, entities, 3)
}

func TestSpawnTool_AsyncDownstreamRequiresTeamCallChannel(t *testing.T) {
	// Batch B leftover #4: a durable SpawnChild task runs detached from the
	// Team Run (AsyncToolExecutor uses context.Background()), so its context
	// carries no insertion channel. The same applies to any Spawn executed
	// outside a Team-scheduled call.
	fixture := newAsyncSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "wait": false, "deliver": "downstream",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "scheduled by a Team")
	assert.Contains(t, result.Error, "no Team call channel")
}

func TestSpawnTool_AsyncRequiresTaskRunner(t *testing.T) {
	fixture := newSpawnFixture(t) // no SetTaskRunner

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "wait": false,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "task runner")
}

func TestSpawnTool_AsyncChildResultCollected(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{
			Status: types.TurnCompleted,
			Reply:  "child reply for " + call.req.CallID,
			Usage:  types.TokenUsage{TotalTokens: 7},
		}, nil
	}

	spawnResult, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "k1", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, spawnResult.Success)
	handles := fixture.spawnHandles(t, spawnResult)
	taskID := handles[0]["task_id"].(string)

	task := fixture.waitForTask(t, taskID)
	require.NotNil(t, task.Result)
	assert.True(t, task.Result.Success)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(task.Result.Content), &payload))
	assert.Equal(t, "k1", payload["key"])
	assert.Equal(t, "completed", payload["status"])
	assert.Contains(t, payload["reply"], "child reply for call-1/k1")

	// Entity state is persisted by the async child, exactly like sync ones.
	snapshot, loadErr := fixture.states.LoadEntity(context.Background(), "parent-agent", "k1")
	require.NoError(t, loadErr)
	assert.Contains(t, snapshot.Confirmed[0], "child reply for")
}

func TestSpawnTool_AsyncChildFailureStoredPerChild(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnFailed, Error: "child exploded"}, nil
	}

	spawnResult, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "bad", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, spawnResult.Success) // starting the async child succeeded

	handles := fixture.spawnHandles(t, spawnResult)
	task := fixture.waitForTask(t, handles[0]["task_id"].(string))
	// The task itself completed; the child failure is carried in the result.
	assert.Equal(t, types.ToolTaskCompleted, task.Status)
	require.NotNil(t, task.Result)
	assert.False(t, task.Result.Success)
	assert.Equal(t, "child exploded", task.Result.Error)

	// A failed child does not write entity state.
	snapshot, loadErr := fixture.states.LoadEntity(context.Background(), "parent-agent", "bad")
	require.NoError(t, loadErr)
	assert.Empty(t, snapshot.Confirmed)
}

func TestSpawnTool_AsyncChildReceivesItemAndDepth(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		assert.Equal(t, 1, spawnDepthFromContext(call.ctx))
		block := contextBlock(call.req.ContextBlocks, "fanout_item")
		require.NotNil(t, block)
		assert.JSONEq(t, `{"file":"a.go"}`, block.Text)
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "ok"}, nil
	}

	spawnResult, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": map[string]any{"file": "a.go"}, "key": "k1", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, spawnResult.Success)

	handles := fixture.spawnHandles(t, spawnResult)
	fixture.waitForTask(t, handles[0]["task_id"].(string))
	require.Len(t, fixture.runner.recorded(), 1)
}

func TestSpawnTool_AsyncDepthAndChildrenLimits(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)

	result, err := fixture.spawn.Execute(withSpawnDepth(fixture.ctx(), 3), map[string]any{
		"item": "a", "wait": false,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "depth")

	items := make([]any, 9)
	for i := range items {
		items[i] = i
	}
	result, err = fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"items": items, "wait": false,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds")
}

func TestSpawnTool_AsyncEntityBusySameKey(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		started <- struct{}{}
		<-release
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "done"}, nil
	}

	first, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "shared", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, first.Success)
	// Wait until the first child actually executes: it now holds the entity
	// lock for the duration of its turn.
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first spawned child never started")
	}

	second, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "b", "key": "shared", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, second.Success)

	// While the first child still holds the entity lock, the second child's
	// task must terminate with the "already executing" per-child error.
	secondTask := fixture.waitForTask(t, fixture.spawnHandles(t, second)[0]["task_id"].(string))
	assert.Equal(t, types.ToolTaskCompleted, secondTask.Status)
	require.NotNil(t, secondTask.Result)
	assert.False(t, secondTask.Result.Success)
	assert.Contains(t, secondTask.Result.Error, "already executing")

	close(release)
	firstTask := fixture.waitForTask(t, fixture.spawnHandles(t, first)[0]["task_id"].(string))
	assert.Equal(t, types.ToolTaskCompleted, firstTask.Status)
	require.NotNil(t, firstTask.Result)
	assert.True(t, firstTask.Result.Success)
}

func TestSpawnTool_AsyncChildEmitsSessionEvents(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	writer := &fakeSessionWriter{}
	fixture.spawn.SetSessionWriter(writer)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{
			Status: types.TurnCompleted,
			Reply:  "async child reply",
			Usage:  types.TokenUsage{TotalTokens: 11},
			Requests: []types.ModelRequestStats{{
				Round: 0, MessageCount: 2, EstimatedPromptTokens: 5,
				Usage: types.TokenUsage{TotalTokens: 11},
			}},
		}, nil
	}

	spawnResult, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "k1", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, spawnResult.Success)
	handles := fixture.spawnHandles(t, spawnResult)
	fixture.waitForTask(t, handles[0]["task_id"].(string))

	require.Eventually(t, func() bool {
		for _, event := range writer.recorded() {
			if event.Type == types.EventAgentTurnCompleted {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "child completed event was not emitted")

	events := writer.recorded()
	var started, completed bool
	for _, event := range events {
		switch event.Type {
		case types.EventAgentTurnStarted:
			started = true
		case types.EventAgentTurnCompleted:
			completed = true
			assert.Equal(t, "call-1/k1", event.CallID)
			assert.Equal(t, types.CallAgent, event.CallType)
			assert.Equal(t, "fs-1", event.FlowSessionID)
			assert.Equal(t, "team-1", event.TeamID)
			spawn, _ := event.Payload["spawn"].(map[string]any)
			require.NotNil(t, spawn, "payload.spawn missing")
			assert.Equal(t, "parent-agent", spawn["agent"])
			assert.Equal(t, "k1", spawn["key"])
			assert.Equal(t, "call-1", spawn["parent_call_id"])
			callResult, _ := event.Payload["call_result"].(types.CallResult)
			require.NotNil(t, callResult, "payload.call_result missing")
			assert.Equal(t, types.TurnCompleted, callResult.Status)
			assert.Equal(t, "async child reply", callResult.Reply)
			assert.Equal(t, 11, callResult.Usage.TotalTokens)
			require.Len(t, callResult.Requests, 1)
			assert.Equal(t, 11, callResult.Requests[0].Usage.TotalTokens)
		}
	}
	assert.True(t, started, "child started event missing")
	assert.True(t, completed, "child completed event missing")
}

func TestSpawnTool_SyncChildEmitsSessionEvents(t *testing.T) {
	fixture := newSpawnFixture(t)
	writer := &fakeSessionWriter{}
	fixture.spawn.SetSessionWriter(writer)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a", "key": "k1",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	events := writer.recorded()
	require.Len(t, events, 2)
	assert.Equal(t, types.EventAgentTurnStarted, events[0].Type)
	assert.Equal(t, "call-1/k1", events[0].CallID)
	assert.Equal(t, types.EventAgentTurnCompleted, events[1].Type)
	callResult, _ := events[1].Payload["call_result"].(types.CallResult)
	require.NotNil(t, callResult)
	assert.Equal(t, types.TurnCompleted, callResult.Status)
}

func TestSpawnTool_ChildEventsSkippedWithoutSession(t *testing.T) {
	// No session writer and no flow session: emission must be a no-op, not
	// an error.
	fixture := newSpawnFixture(t)
	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "a"})
	require.NoError(t, err)
	assert.True(t, result.Success)

	orphan := newSpawnFixture(t)
	orphan.parent.FlowSessionID = ""
	_, err = orphan.spawn.Execute(withSpawnIdentity(context.Background(), types.AgentConfig{Name: "parent-agent"}, orphan.parent), map[string]any{"item": "a"})
	require.NoError(t, err)
}

func TestSpawnTool_ExecuteChildTaskFromArguments(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	parent := fixture.parent
	args := spawnChildArguments("child-agent", parent, map[string]any{"file": "b.go"}, "role-b", 1)

	result, err := fixture.spawn.executeChildTask(context.Background(), args)
	require.NoError(t, err)
	assert.True(t, result.Success)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	call := calls[0]
	assert.Equal(t, "child-agent", call.agent.Name)
	assert.Equal(t, "child-agent", call.req.AgentID)
	assert.Equal(t, "fs-1", call.req.FlowSessionID)
	assert.Equal(t, "team-1", call.req.TeamID)
	assert.Equal(t, "call-1/role-b", call.req.CallID)
	assert.Equal(t, 12, call.req.MaxAgentRounds)
	assert.Equal(t, 1, spawnDepthFromContext(call.ctx))
	block := contextBlock(call.req.ContextBlocks, "fanout_item")
	require.NotNil(t, block)
	assert.JSONEq(t, `{"file":"b.go"}`, block.Text)

	var payload map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &payload))
	assert.Equal(t, "role-b", payload["key"])
}

func TestSpawnTool_ExecuteChildTaskUnknownAgent(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	args := spawnChildArguments("ghost", fixture.parent, "a", "k", 1)

	result, err := fixture.spawn.executeChildTask(context.Background(), args)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "ghost")
}

func TestSpawnTool_ExecuteChildTaskSurvivesJSONRoundTrip(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	args := spawnChildArguments("parent-agent", fixture.parent, map[string]any{"file": "c.go"}, "role-c", 1)
	// The durable store persists arguments as JSON: simulate the round-trip.
	data, err := json.Marshal(args)
	require.NoError(t, err)
	var roundTripped map[string]any
	require.NoError(t, json.Unmarshal(data, &roundTripped))

	result, err := fixture.spawn.executeChildTask(context.Background(), roundTripped)
	require.NoError(t, err)
	assert.True(t, result.Success)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, 1, spawnDepthFromContext(calls[0].ctx))
	block := contextBlock(calls[0].req.ContextBlocks, "fanout_item")
	require.NotNil(t, block)
	assert.JSONEq(t, `{"file":"c.go"}`, block.Text)
}

// recordingBaseExecutor records forwarded tool executions for the dispatcher.
type recordingBaseExecutor struct {
	mu    sync.Mutex
	names []string
}

func (e *recordingBaseExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	e.mu.Lock()
	e.names = append(e.names, name)
	e.mu.Unlock()
	return &types.ToolResult{Success: true, Content: "base handled " + name}, nil
}

func (e *recordingBaseExecutor) recorded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.names...)
}

func TestSpawnTaskDispatcher_Routes(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	base := &recordingBaseExecutor{}
	dispatcher := NewSpawnTaskDispatcher(fixture.spawn, base)

	// Ordinary tools go to the base executor.
	result, err := dispatcher.Execute(context.Background(), "Bash", map[string]any{})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, []string{"Bash"}, base.recorded())

	// SpawnChild routes into the Spawn tool (one child turn runs).
	childArgs := spawnChildArguments("parent-agent", fixture.parent, "item", "k-dispatch", 1)
	result, err = dispatcher.Execute(context.Background(), SpawnChildToolName, childArgs)
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Empty(t, base.recorded()[1:])
	require.Len(t, fixture.runner.recorded(), 1)
	assert.Equal(t, "k-dispatch", fixture.runner.recorded()[0].req.CallID[strings.Index(fixture.runner.recorded()[0].req.CallID, "/")+1:])
}

func TestSpawnTaskDispatcher_ExecutionSpec(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	base := &recordingBaseExecutor{}
	dispatcher := NewSpawnTaskDispatcher(fixture.spawn, base)

	spec := dispatcher.ExecutionSpec(SpawnChildToolName)
	assert.Equal(t, types.ToolSerial, spec.Class)
	assert.False(t, spec.RestartSafe, "interrupted child turns must be failed, not re-run")

	// Unknown tools fall back to serial, matching the registry default.
	spec = dispatcher.ExecutionSpec("Bash")
	assert.Equal(t, types.ToolSerial, spec.Class)
}

func TestSpawnTool_AsyncRecoversQueuedTaskAfterRestart(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "recovered child"}, nil
	}

	// A SpawnChild task left queued by a dead process before it started.
	task := types.ToolTask{
		ID:            "turn-1:spawn:k-restart:1",
		FlowSessionID: "fs-1",
		TeamID:        "team-1",
		ToolName:      SpawnChildToolName,
		Arguments:     spawnChildArguments("parent-agent", fixture.parent, "item", "k-restart", 1),
		Status:        types.ToolTaskQueued,
		RestartSafe:   false,
	}
	require.NoError(t, fixture.taskStore.Save(context.Background(), task))

	// A new process: fresh executor + dispatcher over the same durable store.
	restarted := NewAsyncToolExecutor(fixture.taskStore, NewSpawnTaskDispatcher(fixture.spawn, nil))
	require.NoError(t, restarted.Recover(context.Background()))

	final := fixture.waitForTask(t, task.ID)
	assert.Equal(t, types.ToolTaskCompleted, final.Status)
	require.Len(t, fixture.runner.recorded(), 1)
	assert.Equal(t, "call-1/k-restart", fixture.runner.recorded()[0].req.CallID)
}

func TestSpawnTool_AsyncRunningTaskFailsOnRestart(t *testing.T) {
	fixture := newAsyncSpawnFixture(t)

	// A SpawnChild task left running when the process died: children may have
	// side effects, so recovery must fail it instead of re-running.
	task := types.ToolTask{
		ID:            "turn-1:spawn:k-interrupted:1",
		FlowSessionID: "fs-1",
		ToolName:      SpawnChildToolName,
		Arguments:     spawnChildArguments("parent-agent", fixture.parent, "item", "k-interrupted", 1),
		Status:        types.ToolTaskRunning,
		RestartSafe:   false,
	}
	require.NoError(t, fixture.taskStore.Save(context.Background(), task))

	restarted := NewAsyncToolExecutor(fixture.taskStore, NewSpawnTaskDispatcher(fixture.spawn, nil))
	require.NoError(t, restarted.Recover(context.Background()))

	final := fixture.waitForTask(t, task.ID)
	assert.Equal(t, types.ToolTaskFailed, final.Status)
	assert.Contains(t, final.Error, "interrupted")
	assert.Empty(t, fixture.runner.recorded(), "an interrupted child must not re-run")
}
