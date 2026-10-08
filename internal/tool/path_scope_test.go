package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// These tests cover the half of the knowledge restriction that lives in
// internal/tool: that the deny list is computed from the caller's identity in
// the context, and that each of the three file tools actually passes it into
// its workspace request. The enforcement itself is covered by the workspace
// tests (internal/workspace/restrict_test.go); if the list is computed
// correctly but never handed to the workspace, nothing is enforced, and that
// failure is invisible without these tests.

// knowledgeTree writes the layout the restriction is about: private knowledge
// for agents a and b, one shared file, and the engine's runtime session data.
func knowledgeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".agents/agents/a/knowledge/private.md":           "AGENT_A_SECRET\n",
		".agents/agents/b/knowledge/private.md":           "AGENT_B_SECRET\n",
		".agents/teams/t2/knowledge/team.md":              "TEAM_T2_SECRET\n",
		".agents/knowledge/global.md":                     "GLOBAL_FACT\n",
		".agents/data/sessions/fs-1/agent.jsonl":          `{"payload":{"text":"MODEL_OUTPUT_TEXT"}}` + "\n",
		".agents/data/sessions/fs-1/teams/t1/state.md":    "TOOL_STATE_MARKER goal\n",
		".agents/data/sessions/fs-1/agents/t1/x/state.md": "AGENT_STATE_MARKER goal\n",
	}
	for relative, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0644))
	}
	return dir
}

// agentAContext is a context as the TurnLoop would leave it for agent a.
func agentAContext() context.Context {
	return types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"})
}

// ---------------------------------------------------------------------------
// Write (batch R3, hole 1): the tool must hand its restriction to the
// workspace. Enforcement lives in internal/workspace; a WriteTool that built a
// WriteRequest without it would leave that enforcement unreachable, and no
// workspace test could notice.
// ---------------------------------------------------------------------------

func TestWriteToolPassesPathRestrictionThrough(t *testing.T) {
	dir := knowledgeTree(t)
	write := NewWriteTool(newTestWorkspace(t, dir))

	// Foreign private knowledge: denied, and denied by the tool rather than
	// by a workspace that was never told anything.
	result, err := write.Execute(agentAContext(), map[string]any{
		"file": ".agents/agents/b/knowledge/planted.md", "content": "IMPLANTED\n", "mode": "create",
	})
	require.NoError(t, err)
	require.False(t, result.Success, "Write must not reach another agent's private tree")
	require.NoFileExists(t, filepath.Join(dir, ".agents/agents/b/knowledge/planted.md"))

	// The engine's runtime data: denied for everyone.
	result, err = write.Execute(agentAContext(), map[string]any{
		"file": ".agents/data/sessions/fs-1/agent.jsonl", "content": "FORGED\n", "mode": "replace",
	})
	require.NoError(t, err)
	require.False(t, result.Success, "Write must not reach the engine's session data")

	// Positive controls. Without these the test would pass just as well on a
	// WriteTool that returned failure unconditionally.
	result, err = write.Execute(agentAContext(), map[string]any{
		"file": ".agents/agents/a/knowledge/own.md", "content": "OWN\n", "mode": "create",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.FileExists(t, filepath.Join(dir, ".agents/agents/a/knowledge/own.md"))

	result, err = write.Execute(agentAContext(), map[string]any{
		"file": "src/main.go", "content": "package main\n", "mode": "create",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.FileExists(t, filepath.Join(dir, "src/main.go"))
}

// TestWriteToolFailsClosedWithoutScope mirrors
// TestFileToolsFailClosedWithoutScope for the write path: a tool call outside
// an AgentTurn carries no identity, so every private tree — the caller's own
// included — is closed, as is the data directory.
func TestWriteToolFailsClosedWithoutScope(t *testing.T) {
	dir := knowledgeTree(t)
	write := NewWriteTool(newTestWorkspace(t, dir))

	for _, target := range []string{
		".agents/agents/a/knowledge/planted.md",
		".agents/agents/b/knowledge/planted.md",
		".agents/data/sessions/fs-1/agent.jsonl",
	} {
		result, err := write.Execute(context.Background(), map[string]any{
			"file": target, "content": "X\n", "mode": "create",
		})
		require.NoError(t, err)
		require.False(t, result.Success, "an unknown caller must not write %s", target)
	}

	// Ordinary files stay writable: failing closed must not disable the tool.
	result, err := write.Execute(context.Background(), map[string]any{
		"file": "src/ok.go", "content": "package ok\n", "mode": "create",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
}

// TestDataDirectoryToolsAreDenied covers the read side of hole 2 at the tool
// layer: whichever of the three file tools is used, none of them reaches a
// session file.
func TestDataDirectoryToolsAreDenied(t *testing.T) {
	dir := knowledgeTree(t)
	ctx := agentAContext()

	read := NewReadTool(newTestWorkspace(t, dir))
	for _, target := range []string{
		".agents/data/sessions/fs-1/agent.jsonl",
		".agents/data/sessions/fs-1/teams/t1/state.md",
	} {
		result, err := read.Execute(ctx, map[string]any{"file": target})
		require.NoError(t, err)
		require.False(t, result.Success, "Read must not reach %s", target)
	}

	grep := NewGrepTool(newTestWorkspace(t, dir))
	for _, pattern := range []string{"MODEL_OUTPUT_TEXT", "STATE_MARKER"} {
		result, err := grep.Execute(ctx, map[string]any{"pattern": pattern, "path": "."})
		require.NoError(t, err)
		require.True(t, result.Success, result.Error)
		require.NotContains(t, result.Content, pattern, "Grep must not surface %s", pattern)
	}
	// The positive control: the same Grep call does find ordinary content.
	found, err := grep.Execute(ctx, map[string]any{"pattern": "AGENT_A_SECRET", "path": "."})
	require.NoError(t, err)
	require.Contains(t, found.Content, "AGENT_A_SECRET")

	glob := NewGlobTool(newTestWorkspace(t, dir))
	result, err := glob.Execute(ctx, map[string]any{"pattern": "**/*.jsonl"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.NotContains(t, result.Content, ".agents/data/", "Glob must not list session filenames")
}

func TestGrepToolAppliesToolPathRestrictionFromContext(t *testing.T) {
	dir := knowledgeTree(t)
	grep := NewGrepTool(newTestWorkspace(t, dir))

	result, err := grep.Execute(agentAContext(), map[string]any{
		"pattern": "SECRET",
		"path":    ".",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.NotContains(t, result.Content, "AGENT_B_SECRET")
	require.NotContains(t, result.Content, ".agents/agents/b/")
	// The positive control: A's own private file is found through the same
	// call, so this is a filter and not an empty result.
	require.Contains(t, result.Content, "AGENT_A_SECRET")
}

func TestReadToolAppliesToolPathRestrictionFromContext(t *testing.T) {
	dir := knowledgeTree(t)
	read := NewReadTool(newTestWorkspace(t, dir))

	result, err := read.Execute(agentAContext(), map[string]any{"file": ".agents/agents/b/knowledge/private.md"})
	require.NoError(t, err)
	require.False(t, result.Success)

	result, err = read.Execute(agentAContext(), map[string]any{"file": ".agents/agents/a/knowledge/private.md"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Content, "AGENT_A_SECRET")
}

func TestGlobToolAppliesToolPathRestrictionFromContext(t *testing.T) {
	dir := knowledgeTree(t)
	glob := NewGlobTool(newTestWorkspace(t, dir))

	result, err := glob.Execute(agentAContext(), map[string]any{"pattern": "**/*.md"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	require.NotContains(t, result.Content, ".agents/agents/b/knowledge/private.md")
	require.Contains(t, result.Content, ".agents/agents/a/knowledge/private.md")
	require.Contains(t, result.Content, ".agents/knowledge/global.md")
}

func TestFileToolsFailClosedWithoutScope(t *testing.T) {
	dir := knowledgeTree(t)
	// A context with no scope is what a tool sees outside an AgentTurn.
	// Nothing private is attributable to the caller, so nothing private may
	// be read — including agent a's own tree, whose owner is unknown here.
	ctx := context.Background()

	read := NewReadTool(newTestWorkspace(t, dir))
	for _, file := range []string{".agents/agents/a/knowledge/private.md", ".agents/agents/b/knowledge/private.md"} {
		result, err := read.Execute(ctx, map[string]any{"file": file})
		require.NoError(t, err)
		require.False(t, result.Success, "an unknown caller must not read %s", file)
	}

	// The shared tree stays readable: failing closed must not disable
	// knowledge reading, only the private part.
	result, err := read.Execute(ctx, map[string]any{"file": ".agents/knowledge/global.md"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
	require.Contains(t, result.Content, "GLOBAL_FACT")

	grep := NewGrepTool(newTestWorkspace(t, dir))
	search, err := grep.Execute(ctx, map[string]any{"pattern": "GLOBAL_FACT", "path": "."})
	require.NoError(t, err)
	require.Contains(t, search.Content, "GLOBAL_FACT")
}

func TestOrdinaryFilesUnaffectedByRestriction(t *testing.T) {
	dir := knowledgeTree(t)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0644))

	ctx := agentAContext()
	read := NewReadTool(newTestWorkspace(t, dir))
	result, err := read.Execute(ctx, map[string]any{"file": "main.go"})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	grep := NewGrepTool(newTestWorkspace(t, dir))
	search, err := grep.Execute(ctx, map[string]any{"pattern": "package main", "path": "."})
	require.NoError(t, err)
	require.Contains(t, search.Content, "main.go")

	glob := NewGlobTool(newTestWorkspace(t, dir))
	files, err := glob.Execute(ctx, map[string]any{"pattern": "**/*.go"})
	require.NoError(t, err)
	require.Contains(t, files.Content, "main.go")
}

// deniedPrefixes is the deny list every caller gets, in order, with the data
// directory last. Written out here so a future addition to the list has to be
// acknowledged by the tests that pin the shape rather than slipping in.
func deniedPrefixes() []string {
	return []string{".agents/agents", ".agents/teams", ".agents/data"}
}

func TestToolPathRestrictionShape(t *testing.T) {
	ctx := types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"})
	restriction := ToolPathRestriction(ctx)

	// The denied prefixes are the owner *directories*, not
	// ".agents/agents/knowledge" — the latter names a directory inside the
	// owner directory and would deny nothing. See ownerDirectory. The data
	// directory is denied for everyone and appears here whether or not the
	// caller has an identity.
	require.Equal(t, deniedPrefixes(), restriction.Denied)
	require.Equal(t, []string{".agents/agents/a/knowledge", ".agents/teams/t1/knowledge"}, restriction.Allowed)

	// An agent outside any team cannot be attributed a team tree, so the
	// team half gets no allow entry and stays fully denied.
	noTeam := ToolPathRestriction(types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a"}))
	require.Equal(t, deniedPrefixes(), noTeam.Denied)
	require.Equal(t, []string{".agents/agents/a/knowledge"}, noTeam.Allowed)

	// No scope at all: both knowledge trees denied, nothing allowed. The
	// data directory is not a knowledge tree and is denied in every row.
	anonymous := ToolPathRestriction(context.Background())
	require.Equal(t, deniedPrefixes(), anonymous.Denied)
	require.Empty(t, anonymous.Allowed)
}

// TestDenyListMatchesWorkspaceFixture closes the seam between the rule and its
// enforcement. internal/workspace's tests hand-write the restriction they
// assert against, because the dependency between these packages only runs one
// way (tool → workspace) and a test cannot cross it without an import cycle.
// That hand-written value is the mirror of this derivation, and a mirror can
// drift.
//
// So it is pinned from this side: the workspace fixture for agent "a" on team
// "t1" is written out here, and the derivation is asserted to produce exactly
// it. Combined with the workspace tests, which enforce whatever they are
// given, this means a prefix dropped from ToolPathRestriction turns a test red
// instead of leaving a green suite guarding a rule the engine no longer
// applies.
//
// The expected value is a copy of agentARestriction in
// internal/workspace/restrict_test.go. Changing one without the other is
// exactly the drift being caught; if the change is intentional, change both.
func TestDenyListMatchesWorkspaceFixture(t *testing.T) {
	derived := ToolPathRestriction(
		types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"}))

	// internal/workspace/restrict_test.go, agentARestriction()
	require.Equal(t, workspace.Restriction{
		Denied:  []string{".agents/agents", ".agents/teams", ".agents/data"},
		Allowed: []string{".agents/agents/a/knowledge", ".agents/teams/t1/knowledge"},
	}, derived, "the workspace test fixture no longer mirrors the derivation")

	// And the identity-less row, which the workspace's
	// useDerivedRestriction mirror must agree with too.
	require.Equal(t,
		workspace.Restriction{Denied: []string{".agents/agents", ".agents/teams", ".agents/data"}},
		ToolPathRestriction(context.Background()),
		"the workspace test fixture for an unscoped caller no longer mirrors the derivation")
}

func TestCodeNavToolAppliesToolPathRestriction(t *testing.T) {
	dir := knowledgeTree(t)
	// `echo` stands in for the language server: if the helper runs at all,
	// its arguments appear in the result, so a success here means the
	// restricted path was accepted. The point of the test is which paths are
	// rejected before the helper is invoked.
	codenav := NewCodeNavTool(newTestWorkspace(t, dir), "echo")

	result, err := codenav.Execute(agentAContext(), map[string]any{
		"operation": "definition",
		"file":      ".agents/agents/b/knowledge/private.md",
	})
	require.NoError(t, err)
	require.False(t, result.Success)
	// Absence, not refusal, so this cannot be used to probe for existence.
	require.Contains(t, result.Error, "not found")

	// A's own private file passes the resolve step (and then reaches the
	// helper, which is what "success" means with echo).
	result, err = codenav.Execute(agentAContext(), map[string]any{
		"operation": "definition",
		"file":      ".agents/agents/a/knowledge/private.md",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)

	// An ordinary file is unaffected.
	result, err = codenav.Execute(agentAContext(), map[string]any{
		"operation": "definition",
		"file":      ".agents/knowledge/global.md",
	})
	require.NoError(t, err)
	require.True(t, result.Success, result.Error)
}

func TestToolScopeRoundTripsThroughContext(t *testing.T) {
	// The scope is the link between the TurnLoop (which publishes it) and
	// internal/tool (which reads it). A break here would silently disable
	// every restriction, so the round trip is asserted directly.
	ctx := types.WithToolScope(context.Background(), types.ToolScope{AgentID: "agent-1", TeamID: "team-1"})
	scope, ok := types.ToolScopeFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, "agent-1", scope.AgentID)
	require.Equal(t, "team-1", scope.TeamID)

	_, ok = types.ToolScopeFromContext(context.Background())
	require.False(t, ok, "a context that never came from a turn must report absence")
}

// TestRealTreeIsolationEndToEnd is the end-to-end proof the security guarantee
// rests on: a real local workspace, plus the restriction computed from a real
// context identity — the exact combination a running agent produces.
//
// The unit tests above split this in two: the workspace tests hand-build a
// Denied list, and the tool tests check the list is passed through. Neither
// would catch a bug in how the list is DERIVED (e.g. denying the wrong prefix),
// because a hand-built list bypasses the derivation entirely. This test joins
// them, and it asserts the positive cases too, so it cannot pass by denying
// everything.
func TestRealTreeIsolationEndToEnd(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		path := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	write(".agents/knowledge/global.md", "GLOBAL_TOKEN\n")
	write(".agents/agents/a/knowledge/a.md", "AGENT_A_TOKEN\n")
	write(".agents/agents/b/knowledge/b.md", "AGENT_B_TOKEN\n")
	write(".agents/teams/t1/knowledge/t1.md", "TEAM_T1_TOKEN\n")
	write(".agents/teams/t2/knowledge/t2.md", "TEAM_T2_TOKEN\n")

	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)

	search := func(ctx context.Context) string {
		restriction := ToolPathRestriction(ctx)
		result, err := ws.Search(ctx, workspace.SearchRequest{Pattern: "TOKEN", Restrict: restriction})
		require.NoError(t, err)
		var seen []string
		for _, match := range result.Matches {
			seen = append(seen, match.Path)
		}
		return strings.Join(seen, " ")
	}

	// Agent a, on team t1: its own trees plus the shared one, nothing else.
	scoped := types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"})
	seen := search(scoped)
	require.Contains(t, seen, ".agents/knowledge/global.md", "shared knowledge stays visible to everyone")
	require.Contains(t, seen, ".agents/agents/a/knowledge/a.md", "an agent reads its own private tree")
	require.Contains(t, seen, ".agents/teams/t1/knowledge/t1.md", "an agent reads its own team's tree")
	require.NotContains(t, seen, "agents/b/knowledge", "another agent's private tree must be unreachable")
	require.NotContains(t, seen, "teams/t2/knowledge", "another team's tree must be unreachable")

	// No identity: every private tree is closed, the shared one is not.
	anonymous := search(context.Background())
	require.Contains(t, anonymous, ".agents/knowledge/global.md")
	require.NotContains(t, anonymous, "AGENT_", "an unscoped caller reaches no private tree")
	require.NotContains(t, anonymous, "TEAM_")
}

// TestRealTreeWriteIsolationEndToEnd is the write-side twin of the test above:
// a real workspace and the restriction derived from a real context, rather
// than a hand-built Denied list. It is the only test that would catch the
// derivation naming the wrong write prefix, and it asserts the positive cases
// so it cannot pass by denying every write.
func TestRealTreeWriteIsolationEndToEnd(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)

	ctx := types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"})
	write := func(ctx context.Context, target, content string) error {
		_, err := ws.Write(ctx, workspace.WriteRequest{
			Path: target, Content: content, Mode: "create", Restrict: ToolPathRestriction(ctx),
		})
		return err
	}

	for _, target := range []string{
		".agents/agents/b/knowledge/implant.md",
		".agents/teams/t2/knowledge/implant.md",
		".agents/data/sessions/fs-x/agent.jsonl",
	} {
		require.Error(t, write(ctx, target, "IMPLANT\n"), "%s must be denied", target)
		require.NoFileExists(t, filepath.Join(root, filepath.FromSlash(target)))
	}

	for _, target := range []string{
		".agents/agents/a/knowledge/own.md",
		".agents/teams/t1/knowledge/own.md",
		".agents/knowledge/shared.md",
		"src/main.go",
	} {
		require.NoError(t, write(ctx, target, "OK\n"), "%s must be writable", target)
		require.FileExists(t, filepath.Join(root, filepath.FromSlash(target)))
	}
}

// TestSessionPersistenceUnaffected is the proof that the data-directory denial
// did not break the engine's own runtime. Callers here look like the denial
// from the outside and nothing like it from the inside: the engine writes and
// reads session data through storage.FileStore, which takes a path and has no
// notion of a Restriction, so none of the above can reach it.
//
// The test is written that way round on purpose — it does not assert anything
// about the restriction. It asserts that the mechanism the engine actually
// uses still works on the exact paths the tools are now denied, which is the
// claim "we closed a hole without closing the engine".
func TestSessionPersistenceUnaffected(t *testing.T) {
	root := t.TempDir()
	files := storage.NewFileStore(root)

	// A session writer's own path: append an event stream the way
	// internal/state does, with no workspace and no restriction in sight.
	eventPath := filepath.ToSlash(filepath.Join(
		".agents", "data", "sessions", "fs-1", "agent.jsonl"))
	require.NoError(t, files.Append(eventPath, []byte(`{"type":"agent_turn.completed"}`+"\n")))
	require.NoError(t, files.Append(eventPath, []byte(`{"payload":{"text":"MODEL_OUTPUT_TEXT"}}`+"\n")))
	require.NoError(t, files.Write(
		filepath.ToSlash(filepath.Join(".agents", "data", "sessions", "fs-1", "agents", "t1", "x", "state.md")),
		[]byte("GOAL\n")))

	// And reads it back, so the write is not merely a file on disk.
	data, err := files.Read(eventPath)
	require.NoError(t, err)
	require.Contains(t, string(data), "agent_turn.completed")
	require.True(t, files.Exists(eventPath))

	// The same paths through the workspace with the restriction applied are
	// still denied — the two statements hold at once, which is the point.
	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)
	ctx := types.WithToolScope(context.Background(), types.ToolScope{AgentID: "a", TeamID: "t1"})
	_, err = ws.Read(ctx, workspace.ReadRequest{Path: eventPath, Restrict: ToolPathRestriction(ctx)})
	require.ErrorIs(t, err, workspace.ErrFileNotFound)
}
