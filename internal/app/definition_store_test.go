package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// fixtureConfigRoot writes a minimal but complete .agents/ tree under root and
// returns the config root, the absolute flow path, and the definitions loaded
// the way the startup path loads them.
//
// settings.json is included on purpose: the reload-equivalence test below
// exists precisely because the reload loader is rooted at an absolute
// directory while the startup loader is rooted at the process cwd, and the
// loader's settings reads (LoadRuntimeLimits / LoadKnowledgeSettings) resolve
// ".agents/settings.json" against the loader's own base directory rather than
// against the config root it derives from the flow path.
func fixtureConfigRoot(t *testing.T, root string) (configRoot, flowPath string, definitions *types.Definitions) {
	t.Helper()
	configRoot = filepath.Join(root, ".agents")
	for _, dir := range []string{"agents", "teams", "flows"} {
		require.NoError(t, os.MkdirAll(filepath.Join(configRoot, dir), 0o755))
	}
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "settings.json"), []byte(`{
  "runtime": {"max_team_turns": 7, "max_agent_rounds": 3},
  "knowledge": {"summary_model": ""}
}`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "flows", "default.yml"), []byte(`
id: fixture-flow
entry: main
teams:
  main:
    team: main-team
    coordinator: true
    inputs:
      user_message: true
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "teams", "main.yml"), []byte(`
id: main-team
calls:
  assistant:
    type: agent
    agent: helper
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "agents", "helper.md"), []byte(`---
name: helper
persona:
  role: helper
---
You help.
`), 0o644))

	flowPath = filepath.Join(configRoot, "flows", "default.yml")
	// Load the startup way: cwd-relative base, cwd-relative flow path. The
	// test chdirs into root so the process cwd is the directory the relative
	// paths were written against, matching how the CLI starts.
	cwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { require.NoError(t, os.Chdir(cwd)) })

	definitions, err = config.NewConfigLoader(".").LoadDefinitions(
		context.Background(),
		config.DefinitionsLoadRequest{FlowPath: filepath.Join(".agents", "flows", "default.yml")},
	)
	require.NoError(t, err)
	return configRoot, flowPath, definitions
}

// TestReloadWithNoChangesProducesEquivalentTree is the safety net for the
// second loader batch A4 introduces.
//
// The reload path cannot use the startup loader's cwd-relative base (a reload
// from a process started elsewhere would read the wrong files), so it is
// rooted at the absolute config root instead. That is only safe if rooting the
// loader at the config root yields the same tree as the startup load: if it
// did not, the first reload would silently swap in definitions describing
// different settings than the running engine started with — a reload that
// "changed" nothing would still change behaviour.
//
// The specific hazard is the loader's family of ".agents/..." reads
// (LoadRuntimeLimits, LoadKnowledgeSettings, LoadLoggingSettings), which
// resolve against the loader's base directory. This test pins that a
// no-op reload through the absolute loader reproduces the startup tree,
// including those settings.
func TestReloadWithNoChangesProducesEquivalentTree(t *testing.T) {
	configRoot, flowPath, startup := fixtureConfigRoot(t, t.TempDir())

	// Sanity: the fixture actually exercises the settings read, otherwise the
	// equivalence assertion below would pass vacuously.
	require.Equal(t, 7, startup.Limits.MaxTeamTurns)
	require.Equal(t, 3, startup.Limits.MaxAgentRounds)

	store := types.NewDefinitionStore(startup, configRoot, flowPath)
	reload := reloadDefinitions(store)
	require.NotNil(t, reload, "a store built with a root and flow must expose a reload func")

	reloaded, err := reload(context.Background())
	require.NoError(t, err)
	require.NotNil(t, reloaded)

	// Compare the parts of the tree a turn actually resolves against. Raw
	// reflect.DeepEqual on the whole struct would also compare anything the
	// loader stamps per-load, which is not what "equivalent" needs to mean.
	assert.Equal(t, startup.Flow, reloaded.Flow)
	assert.Equal(t, startup.Teams, reloaded.Teams)
	assert.Equal(t, startup.Agents, reloaded.Agents)
	assert.Equal(t, startup.Skills, reloaded.Skills)
	assert.Equal(t, startup.Rules, reloaded.Rules)
	assert.True(t, reflect.DeepEqual(startup.Workspace, reloaded.Workspace))
	assert.Equal(t, startup.Limits, reloaded.Limits,
		"a reload must read the same .agents/settings.json the startup load did")
	assert.Equal(t, startup.Knowledge, reloaded.Knowledge)

	// And publishing it through the store must be a true no-op for consumers.
	require.NoError(t, store.Reload(context.Background(), reload))
	assert.Equal(t, startup.Agents, store.Snapshot().Agents)
}

// TestReloadDefinitionsNeedsRootAndFlow pins that a store built without a
// source (tests, embedded flows) reports reload as unwired rather than
// returning a loader that would read the wrong directory.
func TestReloadDefinitionsNeedsRootAndFlow(t *testing.T) {
	assert.Nil(t, reloadDefinitions(types.NewDefinitionStore(&types.Definitions{}, "", "")))
	assert.Nil(t, reloadDefinitions(nil))
}

// TestBuildRuntimeRequiresDefinitions pins the startup contract: BuildRuntime
// takes a store and refuses to build a runtime with nothing to resolve
// against, rather than producing a runtime that fails on its first turn.
func TestBuildRuntimeRequiresDefinitions(t *testing.T) {
	_, err := BuildRuntime(context.Background(), nil, nil, t.TempDir(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definition store is required")

	_, err = BuildRuntime(context.Background(), types.NewDefinitionStore(nil, "", ""), nil, t.TempDir(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no definitions")
}
