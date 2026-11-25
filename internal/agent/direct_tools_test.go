package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestBuildToolSchemasInjectsOnlyDirectTools pins the split: a builtin tool that
// tools.direct leaves out is not rendered into the request at all. Offering it
// alongside REPL is what gives the model two ways to do the same job, and it picks
// the direct one.
func TestBuildToolSchemasInjectsOnlyDirectTools(t *testing.T) {
	loop := &TurnLoop{}
	loop.SetScriptDescription(func([]string) string { return "CATALOG" })
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Grep", "REPL"},
			Direct:  []string{"REPL"},
		},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["REPL"])
	require.False(t, names["Bash"], "a sandbox-only tool must not be injected as a direct call")
	require.False(t, names["Read"])
	require.False(t, names["Grep"])
}

// TestScriptCatalogListsSandboxToolsOnly keeps the two surfaces in step: what the
// REPL description advertises must be exactly what left the direct list. A catalog
// built from Tools.Builtin would advertise tools that are also injected, which is
// the double offer this field exists to remove.
func TestScriptCatalogListsSandboxToolsOnly(t *testing.T) {
	var catalogued []string
	loop := &TurnLoop{}
	loop.SetScriptDescription(func(names []string) string {
		catalogued = names
		return "CATALOG"
	})
	loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "REPL", "WebSearch"},
			Direct:  []string{"REPL", "WebSearch"},
		},
	})
	require.Equal(t, []string{"Bash", "Read"}, catalogued)
}

// TestScriptBindableToolsExcludesInjectedTools keeps execution aligned with the
// catalog: a tool the model calls directly is not also bound inside a program.
func TestScriptBindableToolsExcludesInjectedTools(t *testing.T) {
	loop := &TurnLoop{}
	bindable := loop.scriptBindableTools(types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Grep", "REPL", "Team", "TeamSession"},
			Direct:  []string{"REPL", "Team", "TeamSession"},
		},
	})
	require.Equal(t, []string{"Bash", "Grep", "Read"}, bindable)
}

// TestDirectWithoutNarrowingKeepsEveryTool is the compatibility case: definitions
// written before tools.direct existed declare nothing and must be injected whole.
func TestDirectWithoutNarrowingKeepsEveryTool(t *testing.T) {
	loop := &TurnLoop{}
	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
	})
	names := collectSchemaNames(schemas)
	require.True(t, names["Bash"])
	require.True(t, names["Read"])
}
