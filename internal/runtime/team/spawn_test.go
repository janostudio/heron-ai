package team

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ---------------------------------------------------------------------------
// runState unit tests
// ---------------------------------------------------------------------------

func testRunState(t *testing.T) (*runState, map[string]types.Call) {
	t.Helper()
	remaining := map[string]types.Call{
		"fixer": {ID: "fixer", Type: types.CallAgent, AgentID: "fix-agent", Output: types.OutputSpec{Record: "FixReport"}},
		"verifier": {
			ID: "verifier", Type: types.CallAgent, AgentID: "verify-agent", DependsOn: []string{"fixer"},
		},
	}
	state := newRunState(remaining, map[string]bool{}, nil)
	return state, remaining
}

func TestRunState_InsertRegistersSyntheticCall(t *testing.T) {
	state, _ := testRunState(t)
	err := state.InsertSpawnedCall(context.Background(), "fixer", agentstore.SpawnedCallSpec{
		AgentID: "child-agent", Key: "k1", Item: "fix a.go", Depth: 1,
	})
	require.NoError(t, err)

	child, ok := state.call("fixer/k1")
	require.True(t, ok)
	assert.Equal(t, types.CallAgent, child.Type)
	assert.Equal(t, "child-agent", child.AgentID)
	assert.Equal(t, "FixReport", child.Output.Record, "synthetic call inherits the parent's output.record name")

	spec, ok := state.specOf("fixer/k1")
	require.True(t, ok)
	assert.Equal(t, "fixer", spec.ParentCallID)
	assert.Equal(t, "child-agent", spec.AgentID)
	assert.Equal(t, "k1", spec.Key)
	assert.Equal(t, "fix a.go", spec.Item)
}

func TestRunState_InsertValidations(t *testing.T) {
	state, _ := testRunState(t)
	ctx := context.Background()

	err := state.InsertSpawnedCall(ctx, "", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1"})
	require.ErrorContains(t, err, "parent call id is required")

	err = state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{Key: "k1"})
	require.ErrorContains(t, err, "agent id is required")

	err = state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent"})
	require.ErrorContains(t, err, "entity key is required")

	err = state.InsertSpawnedCall(ctx, "ghost", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1"})
	require.ErrorContains(t, err, "not a scheduled call")

	// A parent without output.record has no record channel for its children.
	remaining := map[string]types.Call{"plain": {ID: "plain", Type: types.CallAgent, AgentID: "a"}}
	bareState := newRunState(remaining, map[string]bool{}, nil)
	err = bareState.InsertSpawnedCall(ctx, "plain", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1"})
	require.ErrorContains(t, err, "output.record")

	// Duplicate children of one parent are rejected while pending.
	require.NoError(t, state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1"}))
	err = state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1"})
	require.ErrorContains(t, err, "already pending")

	// A closed run rejects late joiners.
	state.close()
	err = state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k2"})
	require.ErrorContains(t, err, "no longer running")
}

func TestRunState_ReadyCallsWaitForWholeGroup(t *testing.T) {
	state, _ := testRunState(t)
	ctx := context.Background()
	require.NoError(t, state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1", Depth: 1}))

	state.complete("fixer")
	_, ready := state.readyCalls()["verifier"]
	assert.False(t, ready, "downstream must wait for the parent's pending group member")

	state.complete("fixer/k1")
	_, ready = state.readyCalls()["verifier"]
	assert.True(t, ready, "downstream is ready once the whole group completed")
}

func TestRunState_ReadyCallsWaitForNestedGroupMembers(t *testing.T) {
	// A spawned child itself spawning downstream children extends the group
	// recursively: the grandchild blocks the parent's group completion.
	state, _ := testRunState(t)
	ctx := context.Background()
	require.NoError(t, state.InsertSpawnedCall(ctx, "fixer", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k1", Depth: 1}))
	require.NoError(t, state.InsertSpawnedCall(ctx, "fixer/k1", agentstore.SpawnedCallSpec{AgentID: "child-agent", Key: "k2", Depth: 2}))

	state.complete("fixer")
	state.complete("fixer/k1")
	_, ready := state.readyCalls()["verifier"]
	assert.False(t, ready, "grandchild still pending keeps the group incomplete")

	state.complete("fixer/k1/k2")
	_, ready = state.readyCalls()["verifier"]
	assert.True(t, ready)
}

func TestRunState_ExplicitGroupRegistryIgnoresSlashStaticNames(t *testing.T) {
	// Static call names may contain "/"; membership must come only from the
	// explicit registry, never from name parsing.
	remaining := map[string]types.Call{
		"a": {ID: "a", Type: types.CallAgent, AgentID: "agent-x", Output: types.OutputSpec{Record: "Out"}},
		"a/b": {
			ID: "a/b", Type: types.CallAgent, AgentID: "agent-x", DependsOn: []string{"a"},
		},
		"d": {ID: "d", Type: types.CallAgent, AgentID: "agent-x", DependsOn: []string{"a"}},
	}
	state := newRunState(remaining, map[string]bool{}, nil)
	ctx := context.Background()

	// Before any spawn: completing "a" satisfies both dependents.
	state.complete("a")
	ready := state.readyCalls()
	assert.Contains(t, ready, "a/b")
	assert.Contains(t, ready, "d")

	// Rebuild: now "a" spawns one real member. Both dependents wait for the
	// group; "a/b" is not itself a member of the group.
	remaining = map[string]types.Call{
		"a": {ID: "a", Type: types.CallAgent, AgentID: "agent-x", Output: types.OutputSpec{Record: "Out"}},
		"a/b": {
			ID: "a/b", Type: types.CallAgent, AgentID: "agent-x", DependsOn: []string{"a"},
		},
		"d": {ID: "d", Type: types.CallAgent, AgentID: "agent-x", DependsOn: []string{"a"}},
	}
	state = newRunState(remaining, map[string]bool{}, nil)
	require.NoError(t, state.InsertSpawnedCall(ctx, "a", agentstore.SpawnedCallSpec{AgentID: "agent-y", Key: "k1", Depth: 1}))
	state.complete("a")

	ready = state.readyCalls()
	assert.NotContains(t, ready, "d", "d waits for the pending group member a/k1")
	assert.NotContains(t, ready, "a/b", "a/b depends on a and also waits for the group")

	// The unrelated static "a/b" never blocks the group: completing only the
	// registered member makes d ready even while "a/b" is still pending.
	state.complete("a/k1")
	ready = state.readyCalls()
	assert.Contains(t, ready, "d")

	producers := state.producersFrom("a")
	assert.Equal(t, []string{"a", "a/k1"}, producers, "slash-named static calls are never group members")
}

func TestSelectRecordsWithEmptyGroupRegistryKeepsLegacySemantics(t *testing.T) {
	// Regression: with no registered group members, record selection behaves
	// exactly like the pre-batch-C single-producer lookup.
	state := newRunState(map[string]types.Call{}, map[string]bool{}, nil)
	previous := map[string]types.CallResult{
		"research": {Records: []types.SharedRecord{{Name: "Report", RecordID: "r1", Summary: "from call"}}},
	}
	flowRecords := []types.SharedRecord{{Name: "Report", RecordID: "r-flow", Summary: "from flow"}}

	fromCall := selectCallRecords(flowRecords, previous, types.Call{
		Inputs: types.InputSpec{Records: []types.InputBinding{{From: "research", Record: "Report"}}},
	}, state)
	require.Len(t, fromCall, 1)
	assert.Equal(t, "r1", fromCall[0].RecordID)

	fromUnknown := selectCallRecords(flowRecords, previous, types.Call{
		Inputs: types.InputSpec{Records: []types.InputBinding{{From: "ghost", Record: "Report"}}},
	}, state)
	require.Len(t, fromUnknown, 1)
	assert.Equal(t, "r-flow", fromUnknown[0].RecordID)

	// Team output keeps the legacy first-match semantics for one producer.
	team := types.Team{Output: types.OutputSpec{From: "synthesize", Record: "ReviewReport"}}
	results := map[string]types.CallResult{
		"synthesize": {Records: []types.SharedRecord{
			{Name: "ReviewReport", RecordID: "first"},
			{Name: "ReviewReport", RecordID: "second"},
		}},
	}
	selected := selectTeamRecords(team, results, nil, state)
	require.Len(t, selected, 1)
	assert.Equal(t, "first", selected[0].RecordID)
}

// ---------------------------------------------------------------------------
// Runtime integration tests with a fake Agent runner
// ---------------------------------------------------------------------------

// downstreamSpawnRunner plays a parent that inserts spawned children through
// the ctx-injected ChildInserter, the children themselves, and the downstream
// consumer.
type downstreamSpawnItem struct {
	Key  string
	Item string
}

type downstreamSpawnRunner struct {
	mu             sync.Mutex
	items          []downstreamSpawnItem
	failKeys       map[string]bool
	childBlocks    []types.ContextBlock
	verifierRan    bool
	verifierBlocks string
}

func (r *downstreamSpawnRunner) Run(ctx context.Context, _ types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error) {
	switch {
	case req.CallID == "fixer":
		inserter := agentstore.ChildInserterFromContext(ctx)
		if inserter == nil {
			return &types.AgentResult{Status: types.TurnFailed, Error: "child inserter missing from context"}, nil
		}
		for _, item := range r.items {
			spec := agentstore.SpawnedCallSpec{
				AgentID: "child-agent",
				Key:     item.Key,
				Item:    map[string]any{"file": item.Item},
				Depth:   1,
			}
			if err := inserter.InsertSpawnedCall(ctx, "fixer", spec); err != nil {
				return &types.AgentResult{Status: types.TurnFailed, Error: err.Error()}, nil
			}
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "fixes dispatched"}, nil

	case strings.HasPrefix(req.CallID, "fixer/"):
		key := strings.TrimPrefix(req.CallID, "fixer/")
		r.mu.Lock()
		r.childBlocks = append(r.childBlocks, req.ContextBlocks...)
		r.mu.Unlock()
		if r.failKeys[key] {
			return &types.AgentResult{Status: types.TurnFailed, Error: "child " + key + " boom"}, nil
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "child outcome " + key}, nil

	case req.CallID == "verifier":
		r.mu.Lock()
		r.verifierRan = true
		for _, block := range req.ContextBlocks {
			if block.Kind == "records" {
				r.verifierBlocks = block.Text
			}
		}
		r.mu.Unlock()
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "verified"}, nil
	}
	return &types.AgentResult{Status: types.TurnFailed, Error: "unexpected call " + req.CallID}, nil
}

func (r *downstreamSpawnRunner) capturedChildBlocks() []types.ContextBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]types.ContextBlock(nil), r.childBlocks...)
}

func (r *downstreamSpawnRunner) capturedVerifier() (bool, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verifierRan, r.verifierBlocks
}

func downstreamSpawnTeam() types.Team {
	return types.Team{
		ID: "fix-team",
		Calls: map[string]types.Call{
			"fixer": {
				ID: "fixer", Type: types.CallAgent, AgentID: "fix-agent",
				Responsibility: "dispatch fixes",
				Output:         types.OutputSpec{Record: "FixReport"},
			},
			"verifier": {
				ID: "verifier", Type: types.CallAgent, AgentID: "verify-agent",
				DependsOn: []string{"fixer"},
				Inputs: types.InputSpec{
					Records: []types.InputBinding{{From: "fixer", Record: "FixReport"}},
				},
			},
		},
	}
}

func newDownstreamRuntime(t *testing.T, runner agent.AgentRunner, files storage.FileStore) *Runtime {
	t.Helper()
	if files == nil {
		files = storage.NewFileStore(t.TempDir())
	}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(call.NewAgentExecutor(runner)))
	runtime := NewRuntime(registry, map[string]types.AgentConfig{
		"fix-agent":    {Name: "fix-agent"},
		"child-agent":  {Name: "child-agent"},
		"verify-agent": {Name: "verify-agent"},
	})
	runtime.SetStateStore(state.NewStore(files, state.Limits{}))
	return runtime
}

func TestRuntime_AsyncDownstreamInsertsChildAndDownstreamWaitsForGroup(t *testing.T) {
	runner := &downstreamSpawnRunner{items: []downstreamSpawnItem{
		{Key: "k1", Item: "fix a.go"},
		{Key: "k2", Item: "fix b.go"},
	}}
	runtime := newDownstreamRuntime(t, runner, nil)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team:        downstreamSpawnTeam(),
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Turn.Status)

	// Both synthetic children executed as scheduled calls and published
	// records under the parent's output.record name.
	for _, key := range []string{"k1", "k2"} {
		callID := "fixer/" + key
		childResult, ok := result.CallResults[callID]
		require.True(t, ok, "synthetic call %s must appear in CallResults", callID)
		require.Equal(t, types.TurnCompleted, childResult.Status)
		require.Len(t, childResult.Records, 1)
		assert.Equal(t, "FixReport", childResult.Records[0].Name)
		assert.Contains(t, childResult.Records[0].RecordID, callID, "RecordID keeps the child call id for uniqueness")
	}

	// The downstream call received the parent's record plus both children's
	// records through its from-binding.
	ran, verifierBlocks := runner.capturedVerifier()
	require.True(t, ran)
	assert.Contains(t, verifierBlocks, "fixes dispatched")
	assert.Contains(t, verifierBlocks, "child outcome k1")
	assert.Contains(t, verifierBlocks, "child outcome k2")

	// ## Your Item parity: each child saw its item as the fanout_item block.
	blocks := runner.capturedChildBlocks()
	items := 0
	for _, block := range blocks {
		if block.Kind == "fanout_item" {
			items++
			assert.Contains(t, block.Text, "fix ")
		}
	}
	assert.Equal(t, 2, items)
}

func TestRuntime_AsyncDownstreamChildFailureFailsTeamWithKey(t *testing.T) {
	runner := &downstreamSpawnRunner{
		items: []downstreamSpawnItem{
			{Key: "k1", Item: "fix a.go"},
			{Key: "k2", Item: "fix b.go"},
		},
		failKeys: map[string]bool{"k2": true},
	}
	runtime := newDownstreamRuntime(t, runner, nil)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team:        downstreamSpawnTeam(),
	})
	require.Error(t, err)
	require.Equal(t, types.TurnFailed, result.Turn.Status)

	// The failure aggregation names the failing member with its key; the
	// sibling member completed normally (existing team failure semantics).
	assert.Contains(t, result.Error, `call "fixer/k2" failed`)
	assert.Contains(t, result.Error, "child k2 boom")
	assert.Equal(t, types.TurnCompleted, result.CallResults["fixer/k1"].Status)
	assert.Equal(t, types.TurnFailed, result.CallResults["fixer/k2"].Status)

	ran, _ := runner.capturedVerifier()
	assert.False(t, ran, "downstream must not run after a group member failed")
}

func TestRuntime_EntityStateRoutingForSyntheticCalls(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	states := state.NewStore(files, state.Limits{})
	// Pre-existing entity state must reach the child as entity_state.
	require.NoError(t, states.SaveEntity(context.Background(), "child-agent", "k1", types.StateSnapshot{
		Goal: "keep fixing",
	}))
	runner := &downstreamSpawnRunner{items: []downstreamSpawnItem{{Key: "k1", Item: "fix a.go"}}}
	runtime := newDownstreamRuntime(t, runner, files)

	team := downstreamSpawnTeam()
	team.State = types.StateConfig{Enabled: true}
	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team:        team,
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Turn.Status)

	// The static parent kept its session-scoped per-call state.
	parentState, err := states.LoadAgent(context.Background(), "fs-1", "fix-team", "fixer")
	require.NoError(t, err)
	assert.Equal(t, "dispatch fixes", parentState.Goal)

	// The synthetic child loaded the entity state as a context block...
	blocks := runner.capturedChildBlocks()
	sawItem, sawEntity := false, false
	for _, block := range blocks {
		if block.Kind == "fanout_item" && strings.Contains(block.Text, "fix a.go") {
			sawItem = true
		}
		if block.Kind == "entity_state" && strings.Contains(block.Text, "keep fixing") {
			sawEntity = true
		}
	}
	assert.True(t, sawItem)
	assert.True(t, sawEntity)

	// ...and persisted its outcome to the entity scope (previous state text
	// exists, so the reply lands in NextSteps), never to the session scope.
	entityState, err := states.LoadEntity(context.Background(), "child-agent", "k1")
	require.NoError(t, err)
	assert.Equal(t, "keep fixing", entityState.Goal)
	require.Len(t, entityState.NextSteps, 1)
	assert.Equal(t, "child outcome k1", entityState.NextSteps[0])

	// No session-scoped state was created for the synthetic call id.
	childSessionState, err := states.LoadAgent(context.Background(), "fs-1", "fix-team", "fixer/k1")
	require.NoError(t, err)
	assert.Empty(t, childSessionState.Goal)
	assert.Empty(t, childSessionState.Confirmed)
	assert.Empty(t, childSessionState.NextSteps)
}

// busyEntityRunner inserts two children with the same entity key from two
// different parents.
type busyEntityRunner struct{}

func (r *busyEntityRunner) Run(ctx context.Context, _ types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error) {
	if req.CallID == "fixer1" || req.CallID == "fixer2" {
		inserter := agentstore.ChildInserterFromContext(ctx)
		if inserter == nil {
			return &types.AgentResult{Status: types.TurnFailed, Error: "inserter missing"}, nil
		}
		if err := inserter.InsertSpawnedCall(ctx, req.CallID, agentstore.SpawnedCallSpec{
			AgentID: "child-agent", Key: "shared", Item: "work", Depth: 1,
		}); err != nil {
			return &types.AgentResult{Status: types.TurnFailed, Error: err.Error()}, nil
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: req.CallID + " dispatched"}, nil
	}
	if strings.HasPrefix(req.CallID, "fixer1/") || strings.HasPrefix(req.CallID, "fixer2/") {
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "child ran " + req.CallID}, nil
	}
	return &types.AgentResult{Status: types.TurnFailed, Error: "unexpected call " + req.CallID}, nil
}

func runBusyEntityTeam(t *testing.T, runtime *Runtime) (types.TeamTurnResult, error) {
	t.Helper()
	return runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team: types.Team{
			ID: "fix-team",
			Calls: map[string]types.Call{
				"fixer1": {
					ID: "fixer1", Type: types.CallAgent, AgentID: "fix-agent",
					Output: types.OutputSpec{Record: "Fix1"},
				},
				"fixer2": {
					ID: "fixer2", Type: types.CallAgent, AgentID: "fix-agent",
					Output: types.OutputSpec{Record: "Fix2"},
				},
			},
		},
	})
}

func TestRuntime_EntityLockSharedAcrossParents(t *testing.T) {
	// Two parents spawn children with distinct entity keys: both synthetic
	// calls run normally — the shared lock set does not over-block unrelated
	// entities. (Same-key contention is covered deterministically by the
	// pre-locked test below and by the agentstore.EntityLocks unit tests.)
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(call.NewAgentExecutor(&distinctKeysRunner{})))
	runtime := NewRuntime(registry, map[string]types.AgentConfig{
		"fix-agent":   {Name: "fix-agent"},
		"child-agent": {Name: "child-agent"},
	})
	locks := agentstore.NewEntityLocks()
	runtime.SetEntityLocks(locks)

	result, err := runTwoChildTeam(t, runtime)
	require.NoError(t, err)
	assert.Equal(t, types.TurnCompleted, result.CallResults["fixer1/c1"].Status)
	assert.Equal(t, types.TurnCompleted, result.CallResults["fixer2/c2"].Status)
}

// distinctKeysRunner spawns two children with different entity keys.
type distinctKeysRunner struct{}

func (r *distinctKeysRunner) Run(ctx context.Context, _ types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error) {
	if req.CallID == "fixer1" || req.CallID == "fixer2" {
		inserter := agentstore.ChildInserterFromContext(ctx)
		if inserter == nil {
			return &types.AgentResult{Status: types.TurnFailed, Error: "inserter missing"}, nil
		}
		key := "c1"
		if req.CallID == "fixer2" {
			key = "c2"
		}
		if err := inserter.InsertSpawnedCall(ctx, req.CallID, agentstore.SpawnedCallSpec{
			AgentID: "child-agent", Key: key, Item: "work", Depth: 1,
		}); err != nil {
			return &types.AgentResult{Status: types.TurnFailed, Error: err.Error()}, nil
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: req.CallID + " dispatched"}, nil
	}
	return &types.AgentResult{Status: types.TurnCompleted, Reply: "child ran " + req.CallID}, nil
}

func runTwoChildTeam(t *testing.T, runtime *Runtime) (types.TeamTurnResult, error) {
	t.Helper()
	return runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team: types.Team{
			ID: "fix-team",
			Calls: map[string]types.Call{
				"fixer1": {
					ID: "fixer1", Type: types.CallAgent, AgentID: "fix-agent",
					Output: types.OutputSpec{Record: "Fix1"},
				},
				"fixer2": {
					ID: "fixer2", Type: types.CallAgent, AgentID: "fix-agent",
					Output: types.OutputSpec{Record: "Fix2"},
				},
			},
		},
	})
}

func TestRuntime_EntityLockPreLockedEntityFailsSyntheticCall(t *testing.T) {
	// The entity is already executing (for example an inline spawned child
	// holds the shared lock): every synthetic call of that entity fails fast
	// through the existing team failure aggregation.
	locks := agentstore.NewEntityLocks()
	unlock, ok := locks.TryLock("child-agent", "shared")
	require.True(t, ok)
	defer unlock()

	registry := call.NewRegistry()
	require.NoError(t, registry.Register(call.NewAgentExecutor(&busyEntityRunner{})))
	runtime := NewRuntime(registry, map[string]types.AgentConfig{
		"fix-agent":   {Name: "fix-agent"},
		"child-agent": {Name: "child-agent"},
	})
	runtime.SetEntityLocks(locks)

	result, err := runBusyEntityTeam(t, runtime)
	require.Error(t, err)
	assert.Contains(t, result.Error, "already executing")
	assert.Equal(t, types.TurnFailed, result.CallResults["fixer1/shared"].Status)
	assert.Equal(t, types.TurnFailed, result.CallResults["fixer2/shared"].Status)
}

// ---------------------------------------------------------------------------
// End-to-end: real TurnLoop + Spawn tool inside the Team runtime
// ---------------------------------------------------------------------------

// callMarkerRenderer embeds the call id (and all context block texts) into
// the first user message so the scripted model can distinguish calls.
type callMarkerRenderer struct{}

func (callMarkerRenderer) Render(_ types.AgentConfig, req types.AgentRequest, rctx agent.RenderContext) ([]types.Message, error) {
	var user strings.Builder
	user.WriteString("call:" + req.CallID)
	for _, block := range rctx.ContextBlocks {
		user.WriteString("\n" + block.Text)
	}
	return []types.Message{
		{Role: "system", Content: "You are a test agent."},
		{Role: "user", Content: user.String()},
	}, nil
}

type scriptedTurn struct {
	text     string
	toolCall *types.ToolCall
}

// scriptedModel answers per call id: the first user message carries the
// "call:<id>" marker. It records the order of turns and the prompts it saw.
type scriptedModel struct {
	mu      sync.Mutex
	script  map[string][]scriptedTurn
	seq     int
	order   []string
	prompts map[string]string
}

func (m *scriptedModel) Chat(_ context.Context, messages []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (*types.ChatResponse, error) {
	callID := ""
	prompt := ""
	for _, message := range messages {
		if message.Role == "user" && strings.HasPrefix(message.Content, "call:") {
			line := message.Content
			if idx := strings.IndexByte(line, '\n'); idx >= 0 {
				callID = strings.TrimPrefix(line[:idx], "call:")
				prompt = line[idx+1:]
			} else {
				callID = strings.TrimPrefix(line, "call:")
			}
			break
		}
	}
	m.mu.Lock()
	m.seq++
	m.order = append(m.order, callID)
	if prompt != "" {
		if m.prompts == nil {
			m.prompts = map[string]string{}
		}
		m.prompts[callID] = prompt
	}
	turns := m.script[callID]
	var turn scriptedTurn
	if len(turns) > 0 {
		turn = turns[0]
		m.script[callID] = turns[1:]
	}
	m.mu.Unlock()

	response := &types.ChatResponse{Text: turn.text}
	if turn.toolCall != nil {
		response.ToolCalls = []types.ToolCall{*turn.toolCall}
	}
	return response, nil
}

func (m *scriptedModel) ChatStream(_ context.Context, _ []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (<-chan types.ChatChunk, error) {
	return nil, fmt.Errorf("streaming is not scripted")
}

func (m *scriptedModel) turnOrder() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.order...)
}

func (m *scriptedModel) promptFor(callID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prompts[callID]
}

func TestRuntime_TurnLoopSpawnDownstreamEndToEnd(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	model := &scriptedModel{script: map[string][]scriptedTurn{
		"fixer": {
			{toolCall: &types.ToolCall{
				ID:   "tc-spawn",
				Name: "Spawn",
				Arguments: map[string]any{
					"agent":   "child-agent",
					"item":    "fix a.go",
					"key":     "k1",
					"wait":    false,
					"deliver": "downstream",
				},
			}},
			{text: "fixes dispatched"},
		},
		"fixer/k1": {{text: "child outcome"}},
		"verifier": {{text: "verified"}},
	}}

	agents := map[string]types.AgentConfig{
		"fix-agent":    {Name: "fix-agent", Tools: types.ToolConfig{Builtin: []string{"Spawn"}}},
		"child-agent":  {Name: "child-agent"},
		"verify-agent": {Name: "verify-agent"},
	}
	toolRegistry := tool.NewToolRegistry()
	turnLoop := agent.NewTurnLoop(
		model,
		tool.NewToolExecutor(toolRegistry),
		nil,
		agent.NewRouteParser(),
		agent.NewHITLGate(0),
		agent.NewHookExecutor(),
		callMarkerRenderer{},
	)
	entityRegistry := agentstore.NewRegistry(files)
	states := state.NewStore(files, state.Limits{})
	entityLocks := agentstore.NewEntityLocks()
	spawnTool := agent.NewSpawnTool(turnLoop, agents, entityRegistry, states)
	spawnTool.SetEntityLocks(entityLocks)
	toolRegistry.Register(spawnTool)

	executors := call.NewRegistry()
	require.NoError(t, executors.Register(call.NewAgentExecutor(turnLoop)))
	runtime := NewRuntime(executors, agents)
	runtime.SetStateStore(states)
	runtime.SetEntityLocks(entityLocks)

	result, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: "fix-team"},
		Team:        downstreamSpawnTeam(),
	})
	require.NoError(t, err)
	require.Equal(t, types.TurnCompleted, result.Turn.Status)

	// The spawned child entered the DAG and published its record.
	childResult, ok := result.CallResults["fixer/k1"]
	require.True(t, ok)
	require.Equal(t, types.TurnCompleted, childResult.Status)
	require.Len(t, childResult.Records, 1)
	assert.Equal(t, "FixReport", childResult.Records[0].Name)
	assert.Contains(t, childResult.Records[0].RecordID, "fixer/k1")

	// The downstream call ran after the child and received the child's
	// record through its inputs binding.
	order := model.turnOrder()
	childIndex, verifierIndex := -1, -1
	for i, callID := range order {
		if callID == "fixer/k1" && childIndex < 0 {
			childIndex = i
		}
		if callID == "verifier" && verifierIndex < 0 {
			verifierIndex = i
		}
	}
	require.GreaterOrEqual(t, childIndex, 0)
	require.GreaterOrEqual(t, verifierIndex, 0)
	assert.Greater(t, verifierIndex, childIndex, "verifier must run after the spawned child")

	verifierPrompt := model.promptFor("verifier")
	assert.Contains(t, verifierPrompt, "child outcome", "downstream inputs must carry the child record")
	assert.Contains(t, verifierPrompt, "fixes dispatched", "downstream inputs must carry the parent record")

	// ## Your Item reached the child, and the entity was persisted with its
	// own state scope.
	childPrompt := model.promptFor("fixer/k1")
	assert.Contains(t, childPrompt, "fix a.go")

	entity, err := entityRegistry.Get(context.Background(), "child-agent", "k1")
	require.NoError(t, err)
	assert.Equal(t, "child-agent", entity.Agent)

	entityState, err := states.LoadEntity(context.Background(), "child-agent", "k1")
	require.NoError(t, err)
	assert.Equal(t, `"fix a.go"`, entityState.Goal)
	require.Len(t, entityState.Confirmed, 1)
	assert.Equal(t, "child outcome", entityState.Confirmed[0])
}
