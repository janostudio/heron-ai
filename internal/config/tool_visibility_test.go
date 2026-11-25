package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestCheckToolVisibilityAcceptsNarrowedSurface is the shape tools.direct exists
// for: the code tools move into the sandbox, REPL and the hand-off tools stay
// injected.
func TestCheckToolVisibilityAcceptsNarrowedSurface(t *testing.T) {
	require.NoError(t, checkToolVisibility(types.AgentConfig{
		Name: "narrowed",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "Grep", "REPL", "Team"},
			Direct:  []string{"REPL", "Team"},
		},
	}))
}

// TestCheckToolVisibilityAllowsFullInjection keeps every definition written before
// the field existed loadable.
func TestCheckToolVisibilityAllowsFullInjection(t *testing.T) {
	require.NoError(t, checkToolVisibility(types.AgentConfig{
		Name:  "legacy",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
	}))
}

// TestCheckToolVisibilityRejectsUndeclaredName: a name in direct that nothing
// declares is skipped by the schema loop, so the definition silently loses a tool
// it claims to have.
func TestCheckToolVisibilityRejectsUndeclaredName(t *testing.T) {
	err := checkToolVisibility(types.AgentConfig{
		Name: "typo",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "REPL"},
			Direct:  []string{"REPL", "Bsh"},
		},
	})
	require.ErrorContains(t, err, "tools.direct lists")
}

// TestCheckToolVisibilityRejectsUnreachableSandbox: hiding a tool without injecting
// REPL leaves it callable only from inside a program that the model never gets.
func TestCheckToolVisibilityRejectsUnreachableSandbox(t *testing.T) {
	err := checkToolVisibility(types.AgentConfig{
		Name: "unreachable",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read", "REPL"},
			Direct:  []string{"Read"},
		},
	})
	require.ErrorContains(t, err, "reachable only from inside REPL")
}

// TestCheckToolVisibilityRejectsMissingREPL covers the other half of the same
// mistake: narrowing the surface on a definition that never declared REPL.
func TestCheckToolVisibilityRejectsMissingREPL(t *testing.T) {
	err := checkToolVisibility(types.AgentConfig{
		Name: "no-repl",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "Read"},
			Direct:  []string{"Read"},
		},
	})
	require.ErrorContains(t, err, "no way to call them")
}

// TestAddAgentDefinitionRejectsBadVisibility proves the check runs at load time,
// which is where --check-config reads it, and not only in a helper nobody calls.
func TestAddAgentDefinitionRejectsBadVisibility(t *testing.T) {
	agents := map[string]types.AgentConfig{}
	err := addAgentDefinition(agents, types.AgentConfig{
		Name: "unreachable",
		Tools: types.ToolConfig{
			Builtin: []string{"Bash", "REPL"},
			Direct:  []string{"REPL", "Spawn"},
		},
	})
	require.ErrorContains(t, err, "tools.direct lists")
	require.Empty(t, agents, "a rejected definition must not be registered")
}

// TestLoadDefinitionsParsesToolDirect proves the field survives the YAML decode.
// A silently ignored key would leave the definition injecting everything while the
// author believes the surface is narrowed — the failure this test exists to catch.
func TestLoadDefinitionsParsesToolDirect(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))
	agentPath := filepath.Join(agentsDir, "narrowed", "AGENT.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentPath), 0755))
	require.NoError(t, os.WriteFile(agentPath, []byte(`---
name: narrowed
tools:
  builtin: [Bash, Read, REPL]
  direct: [REPL]
---
You work through REPL.
`), 0644))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.NoError(t, err)
	agent := definitions.Agents["narrowed"]
	require.Equal(t, []string{"REPL"}, agent.Tools.Direct)
	require.Equal(t, []string{"REPL"}, agent.Tools.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read"}, agent.Tools.SandboxBuiltin())
}

// TestLoadDefinitionsRejectsUnreachableSandbox pins the load-time gate: hiding a
// tool without injecting REPL is a definition that cannot run, so it must not load.
func TestLoadDefinitionsRejectsUnreachableSandbox(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))
	agentPath := filepath.Join(agentsDir, "unreachable", "AGENT.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentPath), 0755))
	require.NoError(t, os.WriteFile(agentPath, []byte(`---
name: unreachable
tools:
  builtin: [Bash, Read, REPL]
  direct: [Read]
---
Body.
`), 0644))

	_, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.ErrorContains(t, err, "reachable only from inside REPL")
}

// TestToolSurfaceWarningsReportsImpliedCodeTools keeps the redundancy note narrow:
// it fires for a code tool listed next to REPL, and for nothing else.
func TestToolSurfaceWarningsReportsImpliedCodeTools(t *testing.T) {
	warnings := ToolSurfaceWarnings(types.AgentConfig{
		Name:  "repl-only",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "REPL"}},
	})
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "Bash")
	require.Contains(t, warnings[0], "REPL already implies")

	require.Empty(t, ToolSurfaceWarnings(types.AgentConfig{
		Name:  "native",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "Read"}},
	}), "no REPL means no derivation to report")

	require.Empty(t, ToolSurfaceWarnings(types.AgentConfig{
		Name:  "override",
		Tools: types.ToolConfig{Builtin: []string{"Bash", "REPL"}, Direct: []string{"Bash", "REPL"}},
	}), "an explicit surface is the author's decision, not a warning")
}

// TestLoadDefinitionsDerivesSurfaceFromREPL is the one-line definition end to end:
// the YAML carries REPL and Team, and the loader hands the runtime a surface where
// the code tools are sandbox-only.
func TestLoadDefinitionsDerivesSurfaceFromREPL(t *testing.T) {
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	require.NoError(t, os.MkdirAll(agentsDir, 0755))
	require.NoError(t, os.MkdirAll(teamsDir, 0755))
	require.NoError(t, os.MkdirAll(flowsDir, 0755))

	require.NoError(t, os.WriteFile(filepath.Join(flowsDir, "default.yml"), []byte(`
id: flow
entry: default
teams:
  default:
    team: default-team
`), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(teamsDir, "default.yml"), []byte(`
id: default-team
calls: {}
`), 0644))
	agentPath := filepath.Join(agentsDir, "repl-only", "AGENT.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(agentPath), 0755))
	require.NoError(t, os.WriteFile(agentPath, []byte(`---
name: repl-only
tools:
  builtin: [REPL, Team]
---
You work through REPL.
`), 0644))

	definitions, err := NewConfigLoader(root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: filepath.Join(flowsDir, "default.yml"),
	})
	require.NoError(t, err)
	agent := definitions.Agents["repl-only"]
	require.Equal(t, []string{"REPL", "Team"}, agent.Tools.InjectedBuiltin())
	require.Equal(t, []string{"Bash", "Read", "Write", "Grep", "Glob"}, agent.Tools.SandboxBuiltin())
}

// TestREPLDisabledWarningsCoversTheEmptySurface: declaring REPL while the tool is
// switched off leaves the Agent without its code tools and without a replacement.
func TestREPLDisabledWarningsCoversTheEmptySurface(t *testing.T) {
	defs := &types.Definitions{
		Script: types.ScriptConfig{Enabled: false},
		Agents: map[string]types.AgentConfig{
			"repl-only": {Name: "repl-only", Tools: types.ToolConfig{Builtin: []string{"REPL"}}},
			"native":    {Name: "native", Tools: types.ToolConfig{Builtin: []string{"Bash"}}},
		},
	}
	warnings := REPLDisabledWarnings(defs)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "repl-only")
	require.NotContains(t, warnings[0], "native")

	defs.Script.Enabled = true
	require.Empty(t, REPLDisabledWarnings(defs), "nothing to report once the tool is on")
}
