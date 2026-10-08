package team

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/skill"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// agentCallTeam is a one-call Team whose only call targets agentID. It keeps
// the store tests focused on definition resolution rather than on scheduling.
func agentCallTeam(teamID, agentID string) types.Team {
	return types.Team{
		ID: teamID,
		Calls: map[string]types.Call{
			"work": {ID: "work", Type: types.CallAgent, AgentID: agentID},
		},
	}
}

// runAgentCall runs the Team and returns the result. A call that fails to
// resolve its agent is reported through both the result and the error return;
// these tests care about the result, so the error is only used to assert that
// nothing unexpected (a panic, a nil map read) happened.
func runAgentCall(t *testing.T, runtime *Runtime, team types.Team) types.TeamTurnResult {
	t.Helper()
	result, _ := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: team.ID},
		Team:        team,
	})
	return result
}

// TestTeamRuntimeSeesDefinitionsAddedAfterBuild is the core contract of the
// store: a definition published after the runtime was built must be resolvable
// on the next Run, without rebuilding the Team runtime. Before the store, the
// runtime captured a map at build time and the late definition could never be
// found.
func TestTeamRuntimeSeesDefinitionsAddedAfterBuild(t *testing.T) {
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(newFakeCallExecutor(types.CallAgent)))

	store := types.NewDefinitionStore(&types.Definitions{
		Agents: map[string]types.AgentConfig{"original-agent": {Name: "original-agent"}},
	}, "", "")
	runtime := NewRuntime(registry, store)

	team := agentCallTeam("review", "late-agent")

	// Before the swap the new agent simply is not there. The failure must be
	// the ordinary "not found" path, not a panic on a nil map.
	before := runAgentCall(t, runtime, team)
	assert.Equal(t, types.TurnFailed, before.Turn.Status)
	assert.Contains(t, before.Error, `agent definition "late-agent" not found`)

	// Publish a tree containing the new agent. The pointer swap is the whole
	// mechanism: no setter, no rebuild.
	store.Swap(&types.Definitions{
		Agents: map[string]types.AgentConfig{
			"original-agent": {Name: "original-agent"},
			"late-agent":     {Name: "late-agent", Persona: types.PersonaConfig{Role: "late"}},
		},
	})

	after := runAgentCall(t, runtime, team)
	require.Equal(t, types.TurnCompleted, after.Turn.Status)
	require.Contains(t, after.CallResults, "work")
	assert.Empty(t, after.CallResults["work"].Error)
	assert.Equal(t, "work completed", after.CallResults["work"].Reply)
}

// TestTeamRuntimeSnapshotIsPerRun not Run-to-Run: each Run takes its own
// snapshot, so a definition removed by a swap stops resolving on the next Run
// even though an earlier Run used it successfully. This is the other half of
// "next turn, not this turn": neither addition nor removal is sticky.
func TestTeamRuntimeSnapshotIsPerRun(t *testing.T) {
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(newFakeCallExecutor(types.CallAgent)))

	store := types.NewDefinitionStore(&types.Definitions{
		Agents: map[string]types.AgentConfig{"stable-agent": {Name: "stable-agent"}},
	}, "", "")
	runtime := NewRuntime(registry, store)

	team := agentCallTeam("review", "stable-agent")
	require.Equal(t, types.TurnCompleted, runAgentCall(t, runtime, team).Turn.Status)

	store.Swap(&types.Definitions{Agents: map[string]types.AgentConfig{}})

	after := runAgentCall(t, runtime, team)
	assert.Equal(t, types.TurnFailed, after.Turn.Status)
	assert.Contains(t, after.Error, `agent definition "stable-agent" not found`)
}

// TestTeamRuntimeNilStoreFailsResolutionRatherThanPanicking pins the nil-store
// path: a runtime built without definitions must report the missing agent, not
// crash on a nil map read. A5/A6 will build runtimes in new places, and a
// forgotten store should surface as a failed call.
func TestTeamRuntimeNilStoreFailsResolutionRatherThanPanicking(t *testing.T) {
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(newFakeCallExecutor(types.CallAgent)))
	runtime := NewRuntime(registry, nil)

	result := runAgentCall(t, runtime, agentCallTeam("review", "any-agent"))
	assert.Equal(t, types.TurnFailed, result.Turn.Status)
	assert.Contains(t, result.Error, `agent definition "any-agent" not found`)
}

// TestTeamRuntimeSkillToolsDoNotMutatePublishedDefinition guards the second
// hazard the store introduces: resolution hands out structs whose slices still
// alias the published tree. The skill injector appends its tools to the agent
// copy on every call, so if that append wrote through the shared backing array
// the published definition's array would gain skill tools behind a slice
// header that still claims a shorter length.
//
// The corruption is deliberately observed through the backing array rather
// than through the published slice's length. appendUnique returns a longer
// slice without shortening the caller's, so the write lands at an index the
// published header no longer exposes — invisible to a len-bounded comparison,
// but a real data race against every concurrent turn reading the same array.
func TestTeamRuntimeSkillToolsDoNotMutatePublishedDefinition(t *testing.T) {
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(newFakeCallExecutor(types.CallAgent)))

	// Capacity deliberately exceeds length so a naive append has spare room in
	// the shared array and can write in place without reallocating.
	builtin := make([]string, 1, 8)
	builtin[0] = "Read"
	store := types.NewDefinitionStore(&types.Definitions{
		Agents: map[string]types.AgentConfig{
			"skill-agent": {
				Name:   "skill-agent",
				Skills: []string{"reporter"},
				Tools:  types.ToolConfig{Builtin: builtin},
			},
		},
	}, "", "")

	runtime := NewRuntime(registry, store)
	skillRegistry := skill.NewSkillRegistry()
	require.NoError(t, skillRegistry.Register(types.Skill{Name: "reporter", Tools: []string{"Collect"}}))
	runtime.SetSkillInjector(skill.NewSkillInjector(skillRegistry))

	team := agentCallTeam("review", "skill-agent")
	require.Equal(t, types.TurnCompleted, runAgentCall(t, runtime, team).Turn.Status)

	published := store.Snapshot().Agents["skill-agent"].Tools.Builtin
	assert.Equal(t, []string{"Read"}, published,
		"the published definition must not gain the skill tools the call appended to its local copy")

	// Reach into the spare capacity of the published array. A correct
	// implementation leaves it zeroed; an in-place append left "Collect"
	// there, and that write raced with every other reader of this tree.
	spare := published[:cap(published)]
	assert.Equal(t, "", spare[len(published)],
		"the call's append must not write through the published definition's backing array")
}
