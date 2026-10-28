package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func collectSchemaNames(schemas []types.JSONSchema) map[string]bool {
	names := make(map[string]bool, len(schemas))
	for _, schema := range schemas {
		names[schema.Name] = true
	}
	return names
}

// TestBuildToolSchemasAdvertisesCollectWithTeam pins the pairing: an Agent that
// can hand work to a Team can also collect the result. The engine injects a
// Collect call when the background result comes back, and the model copies it;
// without the grant the copy is rejected and the turn never produces an answer
// (minimal two-Team fixture, 2026-10-27).
func TestBuildToolSchemasAdvertisesCollectWithTeam(t *testing.T) {
	loop := &TurnLoop{}
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Team"}},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["Team"])
	require.True(t, names["Collect"], "Team implies Collect; the engine injects it")
}

func TestBuildToolSchemasAdvertisesCollectWithSpawn(t *testing.T) {
	loop := &TurnLoop{}
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Spawn"}},
	})
	require.True(t, collectSchemaNames(schemas)["Collect"], "Spawn implies Collect")
}

// TestBuildToolSchemasHidesCollectWithoutCollaboration keeps the grant narrow:
// an agent that neither teams nor spawns is not advertised a tool it has no use
// for.
func TestBuildToolSchemasHidesCollectWithoutCollaboration(t *testing.T) {
	loop := &TurnLoop{}
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
	})
	require.False(t, collectSchemaNames(schemas)["Collect"])
}
