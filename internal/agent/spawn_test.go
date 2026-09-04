package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// spawnRunnerCall records one child execution issued by the Spawn tool.
type spawnRunnerCall struct {
	ctx   context.Context
	agent types.AgentConfig
	req   types.AgentRequest
}

type fakeSpawnRunner struct {
	mu     sync.Mutex
	calls  []spawnRunnerCall
	result func(call spawnRunnerCall) (*types.AgentResult, error)
}

func (r *fakeSpawnRunner) Run(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error) {
	r.mu.Lock()
	call := spawnRunnerCall{ctx: ctx, agent: agent, req: req}
	r.calls = append(r.calls, call)
	r.mu.Unlock()
	if r.result != nil {
		return r.result(call)
	}
	return &types.AgentResult{Status: types.TurnCompleted, Reply: "child reply for " + call.req.CallID}, nil
}

func (r *fakeSpawnRunner) recorded() []spawnRunnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]spawnRunnerCall(nil), r.calls...)
}

type spawnFixture struct {
	runner     *fakeSpawnRunner
	registry   *agentstore.Registry
	states     *state.Store
	spawn      *SpawnTool
	agents     map[string]types.AgentConfig
	parent     types.AgentRequest
	taskStore  types.ToolTaskStore
	taskRunner *AsyncToolExecutor
}

func newSpawnFixture(t *testing.T, options ...SpawnOption) *spawnFixture {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	fixture := &spawnFixture{
		runner:   &fakeSpawnRunner{},
		registry: agentstore.NewRegistry(files),
		states:   state.NewStore(files, state.Limits{}),
		agents: map[string]types.AgentConfig{
			"parent-agent": {Name: "parent-agent"},
			"child-agent":  {Name: "child-agent", Persona: types.PersonaConfig{Role: "child"}},
		},
	}
	fixture.spawn = NewSpawnTool(fixture.runner, fixture.agents, fixture.registry, fixture.states, options...)
	fixture.parent = types.AgentRequest{
		FlowSessionID:  "fs-1",
		TeamID:         "team-1",
		TeamTurnID:     "tt-1",
		CallID:         "call-1",
		CallTurnID:     "ct-1",
		AgentID:        "parent-agent",
		AgentTurnID:    "turn-1",
		MaxAgentRounds: 12,
	}
	return fixture
}

func (f *spawnFixture) ctx() context.Context {
	return withSpawnIdentity(context.Background(), types.AgentConfig{Name: "parent-agent"}, f.parent)
}

func (f *spawnFixture) ctxWithCollector(name string) context.Context {
	return agentstore.WithRecordCollector(f.ctx(), agentstore.NewRecordCollector(name, types.ProducerRef{
		FlowSessionID: "fs-1",
		FlowTurnID:    "ft-1",
		TeamID:        "team-1",
		TeamTurnID:    "tt-1",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
	}))
}

func TestSpawnTool_SingleItemDeliverParent(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item":    map[string]any{"file": "a.go"},
		"deliver": "parent",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Empty(t, result.Error)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	call := calls[0]
	assert.Equal(t, "parent-agent", call.agent.Name)
	assert.Equal(t, "parent-agent", call.req.AgentID)
	assert.Equal(t, "fs-1", call.req.FlowSessionID)
	assert.Equal(t, "team-1", call.req.TeamID)
	assert.Equal(t, "tt-1", call.req.TeamTurnID)
	assert.Equal(t, "ct-1", call.req.CallTurnID)
	assert.Equal(t, 12, call.req.MaxAgentRounds)
	assert.Contains(t, call.req.CallID, "call-1/")

	// The item is delivered as a fanout_item context block with item JSON.
	block := contextBlock(call.req.ContextBlocks, "fanout_item")
	require.NotNil(t, block, "fanout_item block missing")
	assert.Equal(t, 85, block.Priority)
	assert.JSONEq(t, `{"file":"a.go"}`, block.Text)

	// The tool returns one aggregated JSON array with the child reply.
	var payload []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &payload))
	require.Len(t, payload, 1)
	assert.Equal(t, "child reply for "+call.req.CallID, payload[0]["reply"])
	assert.NotEmpty(t, payload[0]["key"])
}

func TestSpawnTool_ItemsRunOneChildPerItem(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"items": []any{"one", "two", "three"},
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 3)
	keys := map[string]bool{}
	for _, call := range calls {
		require.NotNil(t, contextBlock(call.req.ContextBlocks, "fanout_item"))
		keys[call.req.CallID] = true
	}
	assert.Len(t, keys, 3)

	var payload []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &payload))
	assert.Len(t, payload, 3)

	entities, listErr := fixture.registry.List(context.Background(), "parent-agent")
	require.NoError(t, listErr)
	assert.Len(t, entities, 3)
}

func TestSpawnTool_ItemAndItemsAreMutuallyExclusive(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item":  "a",
		"items": []any{"b"},
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "either item or items")
}

func TestSpawnTool_EmptyItemsRejected(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"items": []any{}})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "items must not be empty")
}

func TestSpawnTool_MissingItemAndItemsRejected(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "requires item or items")
}

func TestSpawnTool_WaitFalseWithoutRunnerFails(t *testing.T) {
	// wait=false needs the durable async task runner; without it the tool
	// fails loudly instead of silently dropping children.
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "a",
		"wait": false,
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "task runner")
}

func TestSpawnTool_KeyWithMultipleItemsRejected(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"items": []any{"a", "b"},
		"key":   "fixed",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "key")
}

func TestSpawnTool_DeliverDownstreamPublishesRecords(t *testing.T) {
	fixture := newSpawnFixture(t)
	ctx := fixture.ctxWithCollector("FixReport")

	result, err := fixture.spawn.Execute(ctx, map[string]any{
		"items":   []any{"a", "b"},
		"deliver": "downstream",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	collector := agentstore.RecordCollectorFromContext(ctx)
	require.NotNil(t, collector)
	records := collector.Records()
	require.Len(t, records, 2)
	for _, record := range records {
		assert.Equal(t, "FixReport", record.Name)
		assert.Equal(t, "spawn_result", record.Kind)
		assert.Equal(t, "call-1", record.Producer.CallID)
		assert.Equal(t, "fs-1", record.Producer.FlowSessionID)
		assert.NotEmpty(t, record.Data["key"])
		assert.NotEmpty(t, record.Data["reply"])
	}

	// The parent sees only a compact summary, not the child replies.
	var payload []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &payload))
	require.Len(t, payload, 2)
	assert.NotContains(t, result.Content, "child reply")
}

func TestSpawnTool_DeliverDownstreamRequiresCollector(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item":    "a",
		"deliver": "downstream",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "collector")
}

func TestSpawnTool_DeliverDownstreamRequiresRecordName(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctxWithCollector(""), map[string]any{
		"item":    "a",
		"deliver": "downstream",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "output.record")
}

func TestSpawnTool_DepthLimit(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(withSpawnDepth(fixture.ctx(), 3), map[string]any{"item": "a"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "depth")

	result, err = fixture.spawn.Execute(withSpawnDepth(fixture.ctx(), 2), map[string]any{"item": "a"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, 3, result.Metadata["depth"])
}

func TestSpawnTool_ChildSpawnSeesIncrementedDepth(t *testing.T) {
	fixture := newSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		// A child that immediately tries to spawn again must observe depth 1.
		assert.Equal(t, 1, spawnDepthFromContext(call.ctx))
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "ok"}, nil
	}

	_, err := fixture.spawn.Execute(withSpawnDepth(fixture.ctx(), 0), map[string]any{"item": "a"})
	require.NoError(t, err)
	require.Len(t, fixture.runner.recorded(), 1)
}

func TestSpawnTool_TooManyItemsRejected(t *testing.T) {
	fixture := newSpawnFixture(t)

	items := make([]any, 9)
	for i := range items {
		items[i] = i
	}
	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"items": items})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "exceeds")

	// Exactly the limit still works.
	items = items[:8]
	result, err = fixture.spawn.Execute(fixture.ctx(), map[string]any{"items": items})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func TestSpawnTool_UnknownAgentRejected(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item":  "a",
		"agent": "ghost",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "ghost")
}

func TestSpawnTool_ExplicitTargetAgent(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item":  "a",
		"agent": "child-agent",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "child-agent", calls[0].agent.Name)
	assert.Equal(t, types.PersonaConfig{Role: "child"}, calls[0].agent.Persona)
	assert.Equal(t, "child-agent", calls[0].req.AgentID)

	entities, listErr := fixture.registry.List(context.Background(), "child-agent")
	require.NoError(t, listErr)
	require.Len(t, entities, 1)
}

func TestSpawnTool_ReusesEntityByKey(t *testing.T) {
	fixture := newSpawnFixture(t)

	_, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "a", "key": "role-a"})
	require.NoError(t, err)
	_, err = fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "b", "key": "role-a"})
	require.NoError(t, err)

	entities, listErr := fixture.registry.List(context.Background(), "parent-agent")
	require.NoError(t, listErr)
	require.Len(t, entities, 1)
	assert.Equal(t, "role-a", entities[0].Key)
}

func TestSpawnTool_EntityStatePersistsAcrossSpawns(t *testing.T) {
	fixture := newSpawnFixture(t)

	_, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "first", "key": "role-a"})
	require.NoError(t, err)

	// Second spawn of the same entity must receive its persisted state.
	_, err = fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "second", "key": "role-a"})
	require.NoError(t, err)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 2)
	block := contextBlock(calls[1].req.ContextBlocks, "entity_state")
	require.NotNil(t, block, "entity_state block missing on reuse")
	assert.Contains(t, block.Text, "child reply for "+calls[0].req.CallID)

	snapshot, loadErr := fixture.states.LoadEntity(context.Background(), "parent-agent", "role-a")
	require.NoError(t, loadErr)
	assert.Equal(t, `"first"`, snapshot.Goal)
	assert.Contains(t, snapshot.Confirmed, "child reply for "+calls[0].req.CallID)
}

func TestSpawnTool_EntityStateIsolatedBetweenEntities(t *testing.T) {
	fixture := newSpawnFixture(t)

	_, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "a", "key": "one"})
	require.NoError(t, err)
	_, err = fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "b", "key": "two"})
	require.NoError(t, err)

	calls := fixture.runner.recorded()
	for _, call := range calls {
		assert.Nil(t, contextBlock(call.req.ContextBlocks, "entity_state"))
	}
}

func TestSpawnTool_ChildFailureReportedPerChild(t *testing.T) {
	fixture := newSpawnFixture(t)
	var callCount int64
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		if atomic.AddInt64(&callCount, 1) == 1 {
			return &types.AgentResult{Status: types.TurnCompleted, Reply: "fine"}, nil
		}
		return &types.AgentResult{Status: types.TurnFailed, Error: "child exploded"}, nil
	}

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"items": []any{"ok", "bad"}})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "1 of 2")
	assert.Contains(t, result.Error, "child exploded")

	var payload []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &payload))
	require.Len(t, payload, 2)
	var sawFailure, sawSuccess bool
	for _, entry := range payload {
		if entry["error"] == "child exploded" {
			sawFailure = true
		}
		if entry["reply"] == "fine" {
			sawSuccess = true
		}
	}
	assert.True(t, sawFailure)
	assert.True(t, sawSuccess)
}

func TestSpawnTool_FailedChildSkipsStateWrite(t *testing.T) {
	fixture := newSpawnFixture(t)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		return &types.AgentResult{Status: types.TurnFailed, Error: "boom"}, nil
	}

	_, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{"item": "a", "key": "k"})
	require.NoError(t, err)

	snapshot, loadErr := fixture.states.LoadEntity(context.Background(), "parent-agent", "k")
	require.NoError(t, loadErr)
	assert.Empty(t, snapshot.Confirmed)
	assert.Empty(t, snapshot.NextSteps)
}

func TestSpawnTool_RequiresParentIdentity(t *testing.T) {
	fixture := newSpawnFixture(t)

	result, err := fixture.spawn.Execute(context.Background(), map[string]any{"item": "a"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "execution context")
}

func TestSpawnTool_NestedDownstreamRecordCollectorPropagates(t *testing.T) {
	fixture := newSpawnFixture(t)
	collector := agentstore.NewRecordCollector("Out", types.ProducerRef{CallID: "call-1"})
	ctx := agentstore.WithRecordCollector(withSpawnDepth(fixture.ctx(), 1), collector)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		// A nested spawn inside the child (depth 2) publishes to the
		// inherited collector of the outermost call; deeper children just
		// complete without further nesting.
		if spawnDepthFromContext(call.ctx) == 2 {
			assert.Same(t, collector, agentstore.RecordCollectorFromContext(call.ctx))
			nested := NewSpawnTool(fixture.runner, fixture.agents, fixture.registry, fixture.states)
			nestedResult, nestedErr := nested.Execute(call.ctx, map[string]any{
				"item": "nested", "deliver": "downstream",
			})
			assert.NoError(t, nestedErr)
			assert.True(t, nestedResult.Success)
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "outer done"}, nil
	}

	result, err := fixture.spawn.Execute(ctx, map[string]any{"item": "outer", "deliver": "downstream"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	records := collector.Records()
	require.Len(t, records, 2)
	for _, record := range records {
		assert.Equal(t, "Out", record.Name)
		assert.Equal(t, "call-1", record.Producer.CallID)
	}
}

func TestSpawnTool_ToolContract(t *testing.T) {
	fixture := newSpawnFixture(t)

	assert.Equal(t, "Spawn", fixture.spawn.Name())
	assert.False(t, fixture.spawn.NeedsApproval())
	assert.Equal(t, types.ToolExecutionSpec{Class: types.ToolSerial}, fixture.spawn.Execution())
	params := fixture.spawn.Parameters()
	require.Contains(t, params, "agent")
	require.Contains(t, params, "item")
	require.Contains(t, params, "items")
	require.Contains(t, params, "wait")
	require.Contains(t, params, "deliver")
	require.Contains(t, params, "key")
}

func TestSpawnTool_RegisteredThroughToolExecutor(t *testing.T) {
	// End-to-end: parameter validation of the registry-backed executor
	// accepts the Spawn contract.
	fixture := newSpawnFixture(t)
	registry := tool.NewToolRegistry()
	registry.Register(fixture.spawn)
	executor := tool.NewToolExecutor(registry)

	result, err := executor.Execute(fixture.ctx(), "Spawn", map[string]any{"item": "a"})
	require.NoError(t, err)
	assert.True(t, result.Success)
}

func contextBlock(blocks []types.ContextBlock, kind string) *types.ContextBlock {
	for i := range blocks {
		if blocks[i].Kind == kind {
			return &blocks[i]
		}
	}
	return nil
}

// spawnCaptureModel records the tool schemas the TurnLoop exposes to the model.
type spawnCaptureModel struct {
	mockModelProvider
	mu    sync.Mutex
	tools []types.JSONSchema
}

func (m *spawnCaptureModel) Chat(ctx context.Context, messages []types.Message, tools []types.JSONSchema, config types.ModelConfig) (*types.ChatResponse, error) {
	m.mu.Lock()
	m.tools = tools
	m.mu.Unlock()
	return m.mockModelProvider.Chat(ctx, messages, tools, config)
}

func (m *spawnCaptureModel) exposedTools() []types.JSONSchema {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]types.JSONSchema(nil), m.tools...)
}

func TestBuildToolSchemas_SpawnAvailability(t *testing.T) {
	loop := &TurnLoop{}

	declared := loop.buildToolSchemas(types.AgentConfig{Tools: types.ToolConfig{Builtin: []string{"Spawn", "Read"}}})
	var names []string
	for _, schema := range declared {
		names = append(names, schema.Name)
	}
	assert.Contains(t, names, "Spawn")
	assert.Contains(t, names, "Read")

	undeclared := loop.buildToolSchemas(types.AgentConfig{Tools: types.ToolConfig{Builtin: []string{"Read"}}})
	for _, schema := range undeclared {
		assert.NotEqual(t, "Spawn", schema.Name)
	}
}

func TestTurnLoop_SpawnDeclaredExecutesInline(t *testing.T) {
	fixture := newSpawnFixture(t)
	registry := tool.NewToolRegistry()
	registry.Register(fixture.spawn)
	executor := tool.NewToolExecutor(registry)

	model := &spawnCaptureModel{mockModelProvider: mockModelProvider{
		responses: []types.ChatResponse{
			{ToolCalls: []types.ToolCall{{
				ID:        "tc-1",
				Name:      "Spawn",
				Arguments: map[string]any{"item": map[string]any{"file": "a.go"}, "key": "role-a"},
			}}},
			{Text: "spawn finished"},
		},
	}}
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}})

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name:  "parent-agent",
		Tools: types.ToolConfig{Builtin: []string{"Spawn"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, fixture.parent)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, types.TurnCompleted, result.Status)
	assert.Equal(t, "spawn finished", result.Reply)

	// The model saw the Spawn schema...
	var sawSpawn bool
	for _, schema := range model.exposedTools() {
		if schema.Name == "Spawn" {
			sawSpawn = true
		}
	}
	assert.True(t, sawSpawn, "Spawn schema was not exposed to the model")

	// ...and the child ran inline with the parent's identity propagated
	// through the TurnLoop's context injection.
	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "fs-1", calls[0].req.FlowSessionID)
	assert.Equal(t, "team-1", calls[0].req.TeamID)
	assert.Contains(t, calls[0].req.CallID, "call-1/role-a")
	assert.NotNil(t, contextBlock(calls[0].req.ContextBlocks, "fanout_item"))

	entities, listErr := fixture.registry.List(context.Background(), "parent-agent")
	require.NoError(t, listErr)
	require.Len(t, entities, 1)
	assert.Equal(t, "role-a", entities[0].Key)

	snapshot, loadErr := fixture.states.LoadEntity(context.Background(), "parent-agent", "role-a")
	require.NoError(t, loadErr)
	assert.NotEmpty(t, snapshot.Confirmed)
}

func TestTurnLoop_SpawnUndeclaredIsFiltered(t *testing.T) {
	fixture := newSpawnFixture(t)
	registry := tool.NewToolRegistry()
	registry.Register(fixture.spawn)
	executor := tool.NewToolExecutor(registry)

	model := &spawnCaptureModel{mockModelProvider: mockModelProvider{
		responses: []types.ChatResponse{
			{ToolCalls: []types.ToolCall{{
				ID:        "tc-1",
				Name:      "Spawn",
				Arguments: map[string]any{"item": "a"},
			}}},
			{Text: "done"},
		},
	}}
	loop := NewTurnLoop(model, executor, nil, NewRouteParser(), nil, nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}})

	result, err := loop.Run(context.Background(), types.AgentConfig{
		Name: "parent-agent",
		// No Spawn in builtin: the call must be rejected exactly like any
		// other undeclared tool, with zero dynamic-agent side effects.
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
		Loop:  types.LoopConfig{MaxRounds: 3},
	}, fixture.parent)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, types.TurnCompleted, result.Status)

	assert.Empty(t, fixture.runner.recorded())
	for _, schema := range model.exposedTools() {
		assert.NotEqual(t, "Spawn", schema.Name)
	}
	entities, listErr := fixture.registry.List(context.Background(), "parent-agent")
	require.NoError(t, listErr)
	assert.Empty(t, entities)
}
