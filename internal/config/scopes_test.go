package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// scopeFixture builds a minimal loadable tree: one local Team bound into the
// Flow, one local Agent. Tests layer _teams/<scope>/ on top of it.
type scopeFixture struct {
	Root     string
	FlowPath string
}

const scopeFixtureFlow = `
id: flow
entry: main
teams:
  main:
    team: local_team
    inputs:
      user_message: true
    can_activate: [pkg]
`

const scopeFixtureTeam = `
id: local_team
calls:
  work:
    type: agent
    agent: helper
    output:
      record: Reply
`

const scopeFixtureAgent = `---
name: helper
persona:
  role: local helper
model:
  provider: openai
  model: local-model
---
You help locally.
`

func newScopeFixture(t *testing.T) scopeFixture {
	t.Helper()
	root := t.TempDir()
	agentsDir := filepath.Join(root, ".agents", "agents")
	teamsDir := filepath.Join(root, ".agents", "teams")
	flowsDir := filepath.Join(root, ".agents", "flows")
	for _, dir := range []string{agentsDir, teamsDir, flowsDir} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	writeScopeFile(t, filepath.Join(flowsDir, "default.yml"), scopeFixtureFlow)
	writeScopeFile(t, filepath.Join(teamsDir, "local.yml"), scopeFixtureTeam)
	writeScopeFile(t, filepath.Join(agentsDir, "helper.md"), scopeFixtureAgent)
	return scopeFixture{Root: root, FlowPath: filepath.Join(flowsDir, "default.yml")}
}

func writeScopeFile(t *testing.T, path, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
}

// addScope writes a package under .agents/_teams/<scope>/. Keys of files are
// package-relative paths.
func (f scopeFixture) addScope(t *testing.T, scope, manifest string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(f.Root, ".agents", scopesDirName, scope)
	if manifest != "" {
		writeScopeFile(t, filepath.Join(dir, "heron-team.yml"), manifest)
	}
	for rel, body := range files {
		writeScopeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), body)
	}
}

// bindPkgTeam points the fixture Flow's "pkg" binding at a Team id. The default
// fixture Flow has no package binding, so the no-package test stays a pure
// local-tree test.
func (f scopeFixture) bindPkgTeam(t *testing.T, teamIDs ...string) {
	t.Helper()
	flow := strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1)
	for i, teamID := range teamIDs {
		name := "pkg"
		if i > 0 {
			name = fmt.Sprintf("pkg%d", i)
		}
		flow += "  " + name + ":\n    team: " + teamID + "\n    depends_on: [main]\n    can_activate: [main]\n"
	}
	writeScopeFile(t, f.FlowPath, flow)
}

func (f scopeFixture) load(t *testing.T) error {
	t.Helper()
	_, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	return err
}

func TestLoadDefinitionsWithoutScopesUnchanged(t *testing.T) {
	f := newScopeFixture(t)
	// The fixture Flow has no "pkg" binding; this test is about the local tree
	// alone.
	writeScopeFile(t, f.FlowPath, strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1))
	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Contains(t, defs.Teams, "local_team")
	require.Len(t, defs.Teams, 1)
	require.Len(t, defs.Agents, 1)
}

func TestScopeTeamLoadsWithItsOwnAssets(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", `
apiVersion: heron/v1
kind: team
name: x
version: 1.0.0
provides:
  teams: [review_team]
`, map[string]string{
		"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: reviewer
    output:
      record: Report
`,
		"agents/reviewer/AGENT.md": `---
name: reviewer
skills: [owasp-check]
rules: [review-standards]
persona:
  role: reviewer
---
Review.
`,
		"skills/owasp-check/SKILL.md": `---
name: owasp-check
description: check
---
Look for OWASP issues.
`,
		"rules/review-standards.md": `---
id: review-standards
---
Be strict.
`,
	})
	f.bindPkgTeam(t, "x/review_team")

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Contains(t, defs.Teams, "x/review_team")
	require.Contains(t, defs.Agents, "x/reviewer")
	// Skills and rules are referenced by bare name from the Team/Agent that
	// uses them, so the package's copy answers to the bare name.
	require.Contains(t, defs.Skills, "owasp-check")
	require.Equal(t, "Look for OWASP issues.", defs.Skills["owasp-check"].Body)

	// The published Team points at the published Agent key so a runtime that
	// resolves ids from Definitions finds the package version.
	require.Equal(t, "x/reviewer", defs.Teams["x/review_team"].Calls["check"].AgentID)
}

func TestScopeTeamRejectsLocalAgentReference(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", "", map[string]string{
		"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: helper
    output:
      record: Report
`,
	})
	f.bindPkgTeam(t, "x/review_team")

	err := f.load(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), "references missing agent")
	require.Contains(t, err.Error(), "helper")
}

func TestScopeTeamRejectsMissingSkill(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", "", map[string]string{
		"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: reviewer
    output:
      record: Report
`,
		"agents/reviewer/AGENT.md": `---
name: reviewer
skills: [owasp-check]
---
Review.
`,
	})
	f.bindPkgTeam(t, "x/review_team")

	err := f.load(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing skill")
}

func TestScopeTeamCoexistsWithLocalTeamOfSameName(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", `
provides:
  teams: [local_team]
`, map[string]string{
		"teams/local_team.yml": `
id: local_team
calls: {}
`,
	})
	f.bindPkgTeam(t, "local_team", "x/local_team")

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	// The local Team keeps its bare name; the package copy is namespaced, so
	// both bindings resolve and neither definition replaces the other.
	require.Contains(t, defs.Teams, "local_team")
	require.Contains(t, defs.Teams, "x/local_team")
	require.Equal(t, "helper", defs.Teams["local_team"].Calls["work"].AgentID)
	require.Equal(t, "local_team", defs.Flow.Teams["pkg"].TeamID)
	require.Equal(t, "x/local_team", defs.Flow.Teams["pkg1"].TeamID)
	require.Empty(t, defs.Teams["x/local_team"].Calls)
}

func TestScopeTeamResolvesOwnAgentWhenNameIsShadowed(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", "", map[string]string{
		"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: helper
    output:
      record: Report
`,
		"agents/helper/AGENT.md": `---
name: helper
persona:
  role: package helper
model:
  provider: openai
  model: package-model
---
Package help.
`,
	})
	f.bindPkgTeam(t, "x/review_team")

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Contains(t, defs.Agents, "x/helper")
	require.Equal(t, "local helper", defs.Agents["helper"].Persona.Role)
	require.Equal(t, "package helper", defs.Agents["x/helper"].Persona.Role)

	// The package Team's call must point at the package Agent, not the local
	// one it shadowed by name.
	require.Equal(t, "x/helper", defs.Teams["x/review_team"].Calls["check"].AgentID)
}

func TestFlowBindsScopeTeamByQualifiedName(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", "", map[string]string{
		"teams/review_team.yml": `
id: review_team
calls: {}
`,
	})

	// A Flow binding names the package Team directly, so the fixture Flow is
	// written out rather than going through bindPkgTeam.
	writeScopeFile(t, f.FlowPath, `
id: flow
entry: main
teams:
  main:
    team: local_team
    inputs:
      user_message: true
  pkg:
    team: x/review_team
    depends_on: [main]
`)

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Equal(t, "x/review_team", defs.Flow.Teams["pkg"].TeamID)
	require.Contains(t, defs.Teams, "x/review_team")
}

func TestFlowBindsUnknownScopeTeam(t *testing.T) {
	for _, unknown := range []string{"nope/review_team", "nope/local_team"} {
		t.Run(unknown, func(t *testing.T) {
			f := newScopeFixture(t)
			f.addScope(t, "x", "", map[string]string{
				"teams/review_team.yml": `
id: review_team
calls: {}
`,
			})
			f.bindPkgTeam(t, unknown)

			err := f.load(t)
			require.Error(t, err)
			require.Contains(t, err.Error(), unknown)
		})
	}
}

func TestUnboundBrokenPackageDoesNotBlockFlow(t *testing.T) {
	f := newScopeFixture(t)
	writeScopeFile(t, f.FlowPath, strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1))
	// review_team references an Agent the package does not ship, but the
	// fixture Flow binds only the local Team.
	f.addScope(t, "x", "", map[string]string{
		"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: absent
    output:
      record: Report
`,
	})

	require.NoError(t, f.load(t))
}

func TestBrokenManifestFailsLoad(t *testing.T) {
	f := newScopeFixture(t)
	f.addScope(t, "x", `
provides:
  teams: [missing_team]
`, nil)

	// A manifest naming a Team the package does not ship is a broken package,
	// not merely an unused one: the installed files contradict the manifest.
	err := f.load(t)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing_team")
}

// A package must not change how a Flow that does not bind it loads: the local
// tree loads to the same result whether the package is installed or not.
func TestScopePackageDoesNotChangeLocalFlow(t *testing.T) {
	without := newScopeFixture(t)
	writeScopeFile(t, without.FlowPath, strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1))
	base, err := NewConfigLoader(without.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: without.FlowPath,
	})
	require.NoError(t, err)

	with := newScopeFixture(t)
	writeScopeFile(t, with.FlowPath, strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1))
	with.addScope(t, "x", `
provides:
  teams: [local_team, review_team]
`, map[string]string{
		// Same Team id and same Agent name as the local tree: the worst case
		// for accidental shadowing.
		"teams/local_team.yml": `
id: local_team
calls:
  work:
    type: agent
    agent: helper
    output:
      record: Reply
`,
		"teams/review_team.yml": `
id: review_team
calls: {}
`,
		"agents/helper/AGENT.md": `---
name: helper
persona:
  role: package helper
---
Package help.
`,
		"skills/owasp-check/SKILL.md": `---
name: owasp-check
description: check
---
Check.
`,
	})

	got, err := NewConfigLoader(with.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: with.FlowPath,
	})
	require.NoError(t, err)

	require.Equal(t, base.Flow, got.Flow)
	require.Equal(t, base.Agents["helper"], got.Agents["helper"])
	require.Equal(t, base.Teams["local_team"], got.Teams["local_team"])
	// Package assets are published only under namespaced keys.
	require.Contains(t, got.Teams, "x/local_team")
	require.Contains(t, got.Teams, "x/review_team")
	require.Contains(t, got.Agents, "x/helper")
	require.Equal(t, "package helper", got.Agents["x/helper"].Persona.Role)
}

// Two packages may ship definitions with the same names; each keeps its own
// namespaced copy and each Team keeps calling its own Agent.
func TestTwoScopesWithSameNamesAreIsolated(t *testing.T) {
	f := newScopeFixture(t)
	for _, scope := range []string{"a", "b"} {
		f.addScope(t, scope, "", map[string]string{
			"teams/review_team.yml": `
id: review_team
calls:
  check:
    type: agent
    agent: reviewer
    output:
      record: Report
`,
			"agents/reviewer/AGENT.md": `---
name: reviewer
persona:
  role: ` + scope + ` reviewer
---
Review.
`,
		})
	}
	f.bindPkgTeam(t, "a/review_team", "b/review_team")

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Equal(t, "a/reviewer", defs.Teams["a/review_team"].Calls["check"].AgentID)
	require.Equal(t, "b/reviewer", defs.Teams["b/review_team"].Calls["check"].AgentID)
	require.Equal(t, "a reviewer", defs.Agents["a/reviewer"].Persona.Role)
	require.Equal(t, "b reviewer", defs.Agents["b/reviewer"].Persona.Role)
}

// `teams remove --keep-agents` leaves a scope directory behind with its asset
// directories but no manifest. That is a valid leftover, not a broken package:
// the Flow has to keep loading.
func TestScopeDirectoryWithoutManifestStillLoads(t *testing.T) {
	f := newScopeFixture(t)
	writeScopeFile(t, f.FlowPath, strings.Replace(scopeFixtureFlow, "    can_activate: [pkg]\n", "", 1))
	f.addScope(t, "x", "", map[string]string{
		"agents/helper/AGENT.md": `---
name: helper
persona:
  role: leftover
  goal: reuse
---
Left over from a removed package.
`,
	})

	defs, err := NewConfigLoader(f.Root).LoadDefinitions(context.Background(), DefinitionsLoadRequest{
		FlowPath: f.FlowPath,
	})
	require.NoError(t, err)
	require.Equal(t, "local helper", defs.Agents["helper"].Persona.Role)
	require.Contains(t, defs.Agents, "x/helper")
	require.Equal(t, "reuse", defs.Agents["x/helper"].Persona.Goal)
}
