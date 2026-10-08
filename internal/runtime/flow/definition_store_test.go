package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// swappingTeamRuntime swaps the store's tree during its first team Run and then
// routes to a second team. That ordering is the whole point: the swap lands
// after the turn has taken its snapshot but *before* the second activation is
// resolved, so a turn that re-read the store would resolve the second
// activation against the new generation and run a different team.
//
// The two generations differ in both team ids:
//
//   - generation 1: entry -> "gen1-team",    activated team -> "gen1-second"
//   - generation 2: entry -> "gen2-team",    activated team -> "gen2-second"
//
// The entry activation always comes from the snapshot the turn started with,
// so seenTeam[0] is fixed by construction; seenTeam[1] is the discriminating
// observation.
type swappingTeamRuntime struct {
	store *types.DefinitionStore
	// swap suppresses or enables the mid-turn Swap itself. The activation
	// routing is independent of it so a test can exercise the start boundary
	// without a swap landing inside the turn.
	swap    bool
	swapped bool
	seen    []string
}

func (r *swappingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	r.seen = append(r.seen, req.Team.ID)
	if !r.swapped {
		r.swapped = true
		if r.swap {
			r.store.Swap(twoTeamDefinitions(2, "gen2-team", "gen2-second"))
		}
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextActivate, Teams: []string{"second"}},
		}, nil
	}
	return types.TeamTurnResult{
		Turn: req.TeamTurn,
		Next: &types.Route{Action: types.NextProceed},
	}, nil
}

// twoTeamDefinitions builds a flow with a coordinator entry team that may
// activate a dependent second team. Both team ids are parameters so the same
// shape can describe two generations of the same flow.
func twoTeamDefinitions(generation int, entryTeam, secondTeam string) *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "snapshot-flow",
			EntryTeamID: "entry",
			Teams: map[string]types.FlowTeamBinding{
				"entry": {
					ID:          "entry",
					TeamID:      entryTeam,
					Coordinator: true,
					CanActivate: []string{"second"},
					Inputs:      types.InputSpec{UserMessage: true},
				},
				"second": {
					ID:        "second",
					TeamID:    secondTeam,
					DependsOn: []string{"entry"},
				},
			},
		},
		Teams: map[string]types.Team{
			entryTeam:  {ID: entryTeam},
			secondTeam: {ID: secondTeam},
		},
		Agents: map[string]types.AgentConfig{
			"agent-gen" + string(rune('0'+generation)): {Name: "agent-gen" + string(rune('0'+generation))},
		},
	}
}

// TestFlowTurnUsesSingleSnapshot is the "next turn, not this turn" contract.
//
// A definition published while a turn is running must not reach that turn.
// Re-snapshotting mid-turn would let a turn that started on generation 1
// suddenly route into generation 2's graph, so the flow it planned and the
// teams it resolves would come from different generations — the second
// activation would resolve to a team id that generation 1 never declared.
// The turn must finish entirely on the tree it started with, and the change
// must land on the next turn.
func TestFlowTurnUsesSingleSnapshot(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(fileStore)

	store := types.NewDefinitionStore(twoTeamDefinitions(1, "gen1-team", "gen1-second"), "", "")
	teamRuntime := &swappingTeamRuntime{store: store, swap: true}
	runtime := newTestRuntimeWithStore(store, teamRuntime, sessions, nil)

	first, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "snapshot-flow",
		Input:  "start the turn that swaps",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, first.Session.Status)
	assert.Empty(t, first.Error)

	// The whole turn ran on generation 1, including the activation resolved
	// after the swap had already been published.
	require.Equal(t, []string{"gen1-team", "gen1-second", "gen1-team"}, teamRuntime.seen,
		"a swap during a turn must not change the teams that turn resolves")

	// The next turn resolves against the published tree.
	second, err := runtime.HandleInput(context.Background(), first.Session.ID, "next turn")
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, second.Session.Status)
	assert.Equal(t, []string{"gen1-team", "gen1-second", "gen1-team", "gen2-team"},
		teamRuntime.seen,
		"the change must be visible on the next turn")
}

// TestFlowTurnSnapshotSurvivesSwapBeforeEntry is the control case: a swap that
// lands before a turn starts must be picked up by that turn. Together with the
// test above it pins the boundary at the turn start rather than somewhere
// inside the turn.
func TestFlowTurnSnapshotSurvivesSwapBeforeEntry(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(fileStore)

	store := types.NewDefinitionStore(twoTeamDefinitions(1, "gen1-team", "gen1-second"), "", "")
	teamRuntime := &swappingTeamRuntime{store: store, swap: true}
	runtime := newTestRuntimeWithStore(store, teamRuntime, sessions, nil)

	// Publish generation 2 before the turn begins, then suppress the runtime's
	// own swap so the observation is purely about the turn's start boundary.
	store.Swap(twoTeamDefinitions(2, "gen2-team", "gen2-second"))
	teamRuntime.swap = false

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "snapshot-flow",
		Input:  "start after the swap",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	// Without the mid-turn swap the activation chain is entry -> second ->
	// coordinator, all drawn from generation 2.
	assert.Equal(t, []string{"gen2-team", "gen2-second", "gen2-team"}, teamRuntime.seen,
		"a turn that starts after a swap must use the new tree")
}

// TestFlowRuntimeNilStoreFailsValidationRatherThanPanicking pins the nil-store
// behaviour the flow runtime inherits from Snapshot's nil-receiver safety.
func TestFlowRuntimeNilStoreFailsValidationRatherThanPanicking(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessions := storage.NewJSONLSessionWriter(fileStore)
	runtime := NewRuntime(nil, &fakeTeamRuntime{}, sessions, nil)

	_, err := runtime.Start(context.Background(), types.StartFlowRequest{FlowID: "any"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitions are nil")
}
