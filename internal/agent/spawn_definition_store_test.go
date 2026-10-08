package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newStoreBackedFixture builds a Spawn tool whose store the test can swap.
// It mirrors newSpawnFixture but keeps the store reachable so a definition can
// be published after construction.
func newStoreBackedFixture(t *testing.T) (*spawnFixture, *types.DefinitionStore) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	store := types.NewDefinitionStore(&types.Definitions{
		Agents: map[string]types.AgentConfig{
			"parent-agent": {Name: "parent-agent"},
		},
	}, "", "")
	fixture := &spawnFixture{
		runner:   &fakeSpawnRunner{},
		registry: agentstore.NewRegistry(files),
		states:   state.NewStore(files, state.Limits{}),
	}
	fixture.spawn = NewSpawnTool(fixture.runner, store, fixture.registry, fixture.states)
	fixture.parent = types.AgentRequest{
		FlowSessionID: "fs-1",
		TeamID:        "team-1",
		TeamTurnID:    "tt-1",
		CallID:        "call-1",
		CallTurnID:    "ct-1",
		AgentID:       "parent-agent",
		AgentTurnID:   "turn-1",
	}
	return fixture, store
}

// TestSpawnToolUsesLatestDefinitions is the Spawn counterpart of the Team
// contract: Spawn is a separate primitive whose tool-call path resolves its
// target per call, so a definition published between calls must be usable by
// the next Execute without rebuilding the tool.
func TestSpawnToolUsesLatestDefinitions(t *testing.T) {
	fixture, store := newStoreBackedFixture(t)

	// Before the swap the new agent is simply not defined.
	before, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"agent": "late-child",
		"item":  "work",
	})
	require.NoError(t, err)
	require.False(t, before.Success)
	assert.Contains(t, before.Error, `Spawn agent "late-child" is not defined`)

	store.Swap(&types.Definitions{
		Agents: map[string]types.AgentConfig{
			"parent-agent": {Name: "parent-agent"},
			"late-child":   {Name: "late-child"},
		},
	})

	after, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"agent": "late-child",
		"item":  "work",
	})
	require.NoError(t, err)
	require.True(t, after.Success, "error: %s", after.Error)

	calls := fixture.runner.recorded()
	require.Len(t, calls, 1)
	assert.Equal(t, "late-child", calls[0].agent.Name,
		"the child turn must run as the newly published agent")
}

// TestSpawnToolResolvesAllLookupsFromOneSnapshot pins the hoist: every lookup
// inside one Execute must see the same tree. The test swaps while the first
// child turn is executing, then resolves the second child's target afterwards,
// so if the tool re-snapshotted per lookup (or per child) a batch of two
// children would straddle two generations.
func TestSpawnToolResolvesAllLookupsFromOneSnapshot(t *testing.T) {
	fixture, store := newStoreBackedFixture(t)

	// The swap happens inside the first child turn. Children run in parallel,
	// so the counter and the one-shot swap share a mutex; both children are
	// released from the same barrier so the ordering is deterministic.
	release := make(chan struct{})
	var mu sync.Mutex
	started := 0
	fixture.runner.result = func(_ spawnRunnerCall) (*types.AgentResult, error) {
		mu.Lock()
		started++
		first := started == 1
		if first {
			store.Swap(&types.Definitions{
				Agents: map[string]types.AgentConfig{
					"parent-agent": {Name: "parent-agent"},
					"child-agent":  {Name: "child-agent"},
				},
			})
		}
		mu.Unlock()
		if first {
			// Do not let the sibling finish before the swap is visible, so the
			// test cannot pass by accident on a lucky interleaving.
			<-release
		}
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "child done"}, nil
	}

	// The tree Execute starts with. Both children must resolve to this one.
	store.Swap(&types.Definitions{
		Agents: map[string]types.AgentConfig{
			"parent-agent": {Name: "parent-agent"},
			"child-agent":  {Name: "child-agent", Persona: types.PersonaConfig{Role: "generation-1"}},
		},
	})

	go func() {
		// Give the second child time to reach the runner, then unblock.
		for {
			mu.Lock()
			ready := started >= 2
			mu.Unlock()
			if ready {
				break
			}
			time.Sleep(time.Millisecond)
		}
		close(release)
	}()

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"agent": "child-agent",
		"items": []any{"one", "two"},
	})
	require.NoError(t, err)
	require.True(t, result.Success, "error: %s", result.Error)

	recorded := fixture.runner.recorded()
	require.Len(t, recorded, 2)
	for i, call := range recorded {
		assert.Equal(t, "generation-1", call.agent.Persona.Role,
			"child %d must run the generation the Execute call started with", i)
	}
}

// TestSpawnToolNilStoreReportsUndefinedAgent pins the nil-store path: a tool
// built without definitions must report the missing agent rather than panic.
func TestSpawnToolNilStoreReportsUndefinedAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	spawn := NewSpawnTool(
		&fakeSpawnRunner{},
		nil,
		agentstore.NewRegistry(files),
		state.NewStore(files, state.Limits{}),
	)
	ctx := withSpawnIdentity(context.Background(), types.AgentConfig{Name: "parent-agent"}, types.AgentRequest{
		CallID: "call-1", AgentID: "parent-agent",
	})

	result, err := spawn.Execute(ctx, map[string]any{"item": "work"})
	require.NoError(t, err)
	require.False(t, result.Success)
	assert.Contains(t, result.Error, `Spawn agent "parent-agent" is not defined`)
}
