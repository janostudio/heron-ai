package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests cover the path restriction that keeps one agent's private
// knowledge out of another agent's file tools. They are written against the
// workspace layer directly, because that is where the guarantee is enforced —
// see restrict.go. internal/tool's tests cover the other half: that the
// restriction is computed from the caller's identity and passed in.

// writeFixtureFile creates a file (and its parents) under root and returns
// nothing, failing the test on any error.
func writeFixtureFile(t *testing.T, root, relative, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0755))
	require.NoError(t, os.WriteFile(full, []byte(content), 0644))
}

// knowledgeFixture is the tree the restriction tests run against: two agents
// with private knowledge, a team-private tree, and one shared global file.
// It mirrors the on-disk layout the engine uses (see the locationScope
// convention in internal/knowledge/store.go).
func knowledgeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, ".agents/agents/a/knowledge/private.md", "AGENT_A_SECRET\n")
	writeFixtureFile(t, root, ".agents/agents/b/knowledge/private.md", "AGENT_B_SECRET\n")
	writeFixtureFile(t, root, ".agents/agents/b/knowledge/deep/nested/leaf.md", "AGENT_B_DEEP_SECRET\n")
	writeFixtureFile(t, root, ".agents/teams/t1/knowledge/team.md", "TEAM_T1_SECRET\n")
	writeFixtureFile(t, root, ".agents/teams/t2/knowledge/team.md", "TEAM_T2_SECRET\n")
	writeFixtureFile(t, root, ".agents/knowledge/global.md", "GLOBAL_FACT\n")
	// Segment-aware neighbours: these exist so a naive strings.HasPrefix on
	// the deny list would wrongly hide them. They are NOT private knowledge
	// of anyone.
	writeFixtureFile(t, root, ".agents/agents/b-knowledge/decoy.md", "DECOY_B\n")
	writeFixtureFile(t, root, ".agents/agents/b/knowledgeable/decoy.md", "DECOY_KNOWLEDGEABLE\n")
	// An ordinary project file, so the write tests can prove the restriction
	// is a filter and not a blanket denial of the workspace.
	writeFixtureFile(t, root, "src/main.go", "package main\n")
	return root
}

// dataTreeFixture is the engine's runtime session layout under a temp root:
// the three event streams and the state files the engine writes through
// storage.FileStore. The restriction never sees the engine's own writers, so
// these files exist here only to be the target of a denied tool request.
func dataTreeFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, root, ".agents/data/sessions/fs-1/flow.jsonl", `{"type":"flow.session.created"}`+"\n")
	writeFixtureFile(t, root, ".agents/data/sessions/fs-1/team.jsonl", `{"type":"team.turn.completed"}`+"\n")
	writeFixtureFile(t, root, ".agents/data/sessions/fs-1/agent.jsonl", `{"payload":{"text":"MODEL_OUTPUT_TEXT"}}`+"\n")
	writeFixtureFile(t, root, ".agents/data/sessions/fs-1/agents/team/call/state.md", "GOAL_STATE_MARKER\n")
	writeFixtureFile(t, root, ".agents/data/sessions/fs-1/teams/team/state.md", "TEAM_STATE_MARKER\n")
	// An ordinary project file, so the read tests can prove the empty result
	// is the restriction and not a walk that found nothing at all.
	writeFixtureFile(t, root, "main.go", "package main\n")
	return root
}

// agentARestriction mirrors what internal/tool derives for agent "a" on team
// "t1". It is hand-written because the dependency only runs tool → workspace,
// so this package cannot call the real derivation, and the tests below are
// about *enforcement* — given a restriction, does the workspace apply it.
//
// The mirror is not left unguarded: internal/tool asserts this exact value
// against the value it derives (TestDenyListMatchesWorkspaceFixture in
// internal/tool/path_scope_test.go), so the rule is pinned in one direction and
// enforced in the other, and a prefix dropped from the derivation turns that
// test red rather than leaving a green suite on a broken rule.
//
// The denied prefixes are ".agents/agents" and ".agents/teams" — the
// directories whose children are owners — and NOT ".agents/agents/knowledge".
// The latter is the single easiest mistake to make here and it is completely
// silent: no owner's private file lives under ".agents/agents/knowledge", so
// that deny list hides nothing while looking correctly configured.
// TestOwnerDirectoryIsNotTheKnowledgeSubdirectory pins the difference.
//
// ".agents/data" is denied with no allow for anyone, which is what makes it
// different in kind from the two knowledge trees: those are denied-with-an-
// allow-back, this one is closed outright.
func agentARestriction(t *testing.T) Restriction {
	t.Helper()
	return Restriction{
		Denied: []string{
			".agents/agents",
			".agents/teams",
			".agents/data",
		},
		Allowed: []string{
			".agents/agents/a/knowledge",
			".agents/teams/t1/knowledge",
		},
	}
}

// useDerivedRestriction is the equivalent mirror for a caller that names no
// agent: the same denials, no allow entry anywhere.
func useDerivedRestriction(t *testing.T) Restriction {
	t.Helper()
	return Restriction{Denied: []string{".agents/agents", ".agents/teams", ".agents/data"}}
}

func TestSearchCannotReachOtherAgentsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "SECRET",
		Path:     ".",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)

	paths := matchPaths(result.Matches)
	// The guarantee: agent B's file is not reachable.
	require.NotContains(t, paths, ".agents/agents/b/knowledge/private.md")
	require.NotContains(t, paths, ".agents/agents/b/knowledge/deep/nested/leaf.md")
	// The positive control, without which the test passes vacuously if the
	// restriction denied everything: A's OWN private file must come back.
	require.Contains(t, paths, ".agents/agents/a/knowledge/private.md")
	// And the other owner's must really exist to be excluded — otherwise
	// this proves nothing about denial.
	require.FileExists(t, filepath.Join(root, ".agents/agents/b/knowledge/private.md"))
}

func TestSearchCannotReachOtherTeamsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "SECRET",
		Path:     ".",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)

	paths := matchPaths(result.Matches)
	require.NotContains(t, paths, ".agents/teams/t2/knowledge/team.md")
	require.Contains(t, paths, ".agents/teams/t1/knowledge/team.md")
}

func TestSearchStillReachesGlobalKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "GLOBAL_FACT",
		Path:     ".",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)

	// The shared tree is the whole point of the layout: restriction must not
	// quietly turn knowledge reading off.
	require.Contains(t, matchPaths(result.Matches), ".agents/knowledge/global.md")
}

func TestReadCannotReachOtherAgentsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	_, err = service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/private.md",
		Restrict: agentARestriction(t),
	})
	require.Error(t, err)
	// Reported as absent rather than as "denied": a distinct error would
	// confirm the file exists, which is the fact being withheld.
	require.ErrorIs(t, err, ErrFileNotFound)

	// Positive control: A's own private knowledge is readable.
	read, err := service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/a/knowledge/private.md",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	require.Contains(t, read.Content, "AGENT_A_SECRET")
}

func TestReadStillReachesGlobalKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	read, err := service.Read(context.Background(), ReadRequest{
		Path:     ".agents/knowledge/global.md",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	require.Contains(t, read.Content, "GLOBAL_FACT")
}

func TestGlobCannotReachOtherAgentsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	matches, err := service.GlobWithOptions(context.Background(), GlobRequest{
		Pattern:  "**/*.md",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)

	require.NotContains(t, matches, ".agents/agents/b/knowledge/private.md")
	// A's own file is listed, so the assertion above is not vacuous.
	require.Contains(t, matches, ".agents/agents/a/knowledge/private.md")
	// The shared tree stays visible to a glob too.
	require.Contains(t, matches, ".agents/knowledge/global.md")
}

func TestDenyPrefixIsSegmentAware(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// Deny exactly agent b's knowledge directory. A naive HasPrefix would
	// also swallow "b-knowledge" and "b/knowledgeable", which are unrelated
	// directories.
	restriction := Restriction{Denied: []string{".agents/agents/b/knowledge"}}

	matches, err := service.GlobWithOptions(context.Background(), GlobRequest{
		Pattern:  "**/*.md",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.NotContains(t, matches, ".agents/agents/b/knowledge/private.md")
	require.Contains(t, matches, ".agents/agents/b-knowledge/decoy.md",
		"denying .../b/knowledge must not deny a sibling named .../b-knowledge")
	require.Contains(t, matches, ".agents/agents/b/knowledgeable/decoy.md",
		"denying .../b/knowledge must not deny a child of .../b/knowledgeable")

	// The same distinction on the read path.
	_, err = service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b-knowledge/decoy.md",
		Restrict: restriction,
	})
	require.NoError(t, err, "a segment-aware prefix must not match b-knowledge")

	// And on the search path.
	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "DECOY",
		Path:     ".",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.Len(t, matchPaths(result.Matches), 2,
		"both decoys must remain searchable while the real directory is denied")
}

func TestDenyListAppliesToNestedFiles(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	restriction := agentARestriction(t)

	// A file several levels below the denied directory is denied too: the
	// restriction is a prefix, not a directory listing.
	_, err = service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/deep/nested/leaf.md",
		Restrict: restriction,
	})
	require.ErrorIs(t, err, ErrFileNotFound)

	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "AGENT_B_DEEP_SECRET",
		Path:     ".",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, result.Matches)

	matches, err := service.GlobWithOptions(context.Background(), GlobRequest{
		Pattern:  "**/leaf.md",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestSearchFromInsideDeniedPathReturnsNothing(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// Naming a denied directory as the search root must not become a way
	// around the restriction: the walk starts there and the first entry it
	// sees is denied.
	result, err := service.Search(context.Background(), SearchRequest{
		Pattern:  "AGENT_B_SECRET",
		Path:     ".agents/agents/b/knowledge",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	require.Empty(t, result.Matches)
}

func TestUnrestrictedRequestIsUnchanged(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// A zero Restriction is the pre-existing behaviour, which is what makes
	// adding the field additive rather than a behaviour change for callers
	// that have no opinion.
	result, err := service.Search(context.Background(), SearchRequest{
		Pattern: "SECRET",
		Path:    ".",
	})
	require.NoError(t, err)
	require.Contains(t, matchPaths(result.Matches), ".agents/agents/b/knowledge/private.md")
}

func TestAllowsOverrideDeniedAncestor(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// The shape the tools actually use: deny the whole owner directory, then
	// re-admit one owner. Allow must win, or the entry would be unreachable
	// whenever it sits under the denied parent — which is always.
	restriction := Restriction{
		Denied:  []string{".agents/agents"},
		Allowed: []string{".agents/agents/a/knowledge"},
	}

	read, err := service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/a/knowledge/private.md",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.Contains(t, read.Content, "AGENT_A_SECRET")

	_, err = service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/private.md",
		Restrict: restriction,
	})
	require.ErrorIs(t, err, ErrFileNotFound)
}

func TestOwnerDirectoryIsNotTheKnowledgeSubdirectory(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// The trap this test exists for. Denying ".agents/agents/knowledge"
	// looks like it hides the agents' private trees, but that path names a
	// directory inside the owner directory, not the owner directory itself —
	// no owner's file lives under it, so the restriction denies nothing.
	// Asserting the negative here means a future edit that "simplifies" the
	// deny list into this shape fails loudly instead of silently leaking.
	looksRight := Restriction{Denied: []string{".agents/agents/knowledge"}}
	read, err := service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/private.md",
		Restrict: looksRight,
	})
	require.NoError(t, err, "this prefix does NOT cover .agents/agents/b/knowledge — which is why it must not be used")
	require.Contains(t, read.Content, "AGENT_B_SECRET")

	// The prefix that does work: the directory whose children are owners.
	correct := Restriction{Denied: []string{".agents/agents"}}
	_, err = service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/private.md",
		Restrict: correct,
	})
	require.ErrorIs(t, err, ErrFileNotFound)
}

func TestEmptyDenyPrefixDeniesNothing(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// An empty prefix is what a path-join with a missing id produces. Read
	// as a wildcard it would deny the entire workspace, so it must be
	// ignored instead.
	restriction := Restriction{Denied: []string{""}}

	read, err := service.Read(context.Background(), ReadRequest{
		Path:     ".agents/agents/b/knowledge/private.md",
		Restrict: restriction,
	})
	require.NoError(t, err)
	require.Contains(t, read.Content, "AGENT_B_SECRET")
}

func TestDenyPrefixTolerantOfPathSpelling(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// The deny list is built by joining path segments, and the walk produces
	// its own spelling. "./x", "x/" and "x" must all mean the same prefix.
	for _, denied := range []string{".agents/agents/b/knowledge", "./.agents/agents/b/knowledge", ".agents/agents/b/knowledge/"} {
		_, err := service.Read(context.Background(), ReadRequest{
			Path:     ".agents/agents/b/knowledge/private.md",
			Restrict: Restriction{Denied: []string{denied}},
		})
		require.ErrorIs(t, err, ErrFileNotFound, "spelling %q must deny", denied)
	}
}

// ---------------------------------------------------------------------------
// Write-side restriction (batch R3, hole 1)
//
// The tests above cover reads. Before this batch WriteRequest carried no
// restriction at all, so the deny list was bypassable in one step: write into
// a restricted tree, then read the file back. Every test below is written so
// that it fails if the enforcement is removed — the positive controls exist
// specifically to catch a test that passes by denying everything.
// ---------------------------------------------------------------------------

// TestWriteCannotReachOtherAgentsPrivateKnowledge is the direct counterpart of
// TestReadCannotReachOtherAgentsPrivateKnowledge: agent a must not be able to
// plant, overwrite or edit a file inside agent b's private knowledge tree.
func TestWriteCannotReachOtherAgentsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	_, err = service.Write(context.Background(), WriteRequest{
		Path:     ".agents/agents/b/knowledge/planted.md",
		Content:  "IMPLANTED_BY_A\n",
		Mode:     "create",
		Restrict: agentARestriction(t),
	})
	require.Error(t, err, "a foreign agent's private tree must not be writable")
	require.ErrorIs(t, err, ErrFileNotFound)
	require.NoFileExists(t, filepath.Join(root, ".agents/agents/b/knowledge/planted.md"))

	// Positive control: a's OWN private tree is writable. Without this, the
	// assertions above would also pass if writes were denied everywhere.
	written, err := service.Write(context.Background(), WriteRequest{
		Path:     ".agents/agents/a/knowledge/notes.md",
		Content:  "A_OWN_NOTE\n",
		Mode:     "create",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err, "an agent must be able to write its own private knowledge")
	require.NotEmpty(t, written.Revision)
	require.FileExists(t, filepath.Join(root, ".agents/agents/a/knowledge/notes.md"))
}

func TestWriteCannotReachOtherTeamsPrivateKnowledge(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	_, err = service.Write(context.Background(), WriteRequest{
		Path:     ".agents/teams/t2/knowledge/planted.md",
		Content:  "IMPLANTED_BY_T1\n",
		Mode:     "create",
		Restrict: agentARestriction(t),
	})
	require.ErrorIs(t, err, ErrFileNotFound)
	require.NoFileExists(t, filepath.Join(root, ".agents/teams/t2/knowledge/planted.md"))

	// Positive control: the caller's own team tree is writable.
	_, err = service.Write(context.Background(), WriteRequest{
		Path:     ".agents/teams/t1/knowledge/notes.md",
		Content:  "T1_OWN_NOTE\n",
		Mode:     "create",
		Restrict: agentARestriction(t),
	})
	require.NoError(t, err, "an agent must be able to write its own team's private knowledge")
	require.FileExists(t, filepath.Join(root, ".agents/teams/t1/knowledge/notes.md"))
}

// TestWriteRestrictionCoversAllModes exists because the obvious place to put
// the check — inside the "create" branch, where the file first appears — would
// leave replace and edit open while every create-mode test still passed. Each
// mode is exercised against the same foreign path, and each is paired with a
// positive control on an ordinary file so that a mode that was never actually
// implemented cannot pass as "denied".
func TestWriteRestrictionCoversAllModes(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	foreign := ".agents/agents/b/knowledge/private.md"
	victim := filepath.Join(root, ".agents/agents/b/knowledge/private.md")
	before, err := os.ReadFile(victim)
	require.NoError(t, err)

	t.Run("create", func(t *testing.T) {
		_, err := service.Write(context.Background(), WriteRequest{
			Path:     ".agents/agents/b/knowledge/new.md",
			Content:  "X\n",
			Mode:     "create",
			Restrict: agentARestriction(t),
		})
		require.ErrorIs(t, err, ErrFileNotFound)
		require.NoFileExists(t, filepath.Join(root, ".agents/agents/b/knowledge/new.md"))
	})

	t.Run("replace", func(t *testing.T) {
		_, err := service.Write(context.Background(), WriteRequest{
			Path:     foreign,
			Content:  "OVERWRITTEN\n",
			Mode:     "replace",
			Restrict: agentARestriction(t),
		})
		require.ErrorIs(t, err, ErrFileNotFound)
		// The existing file must be byte-identical: a denial that let the
		// write through and reported an error afterwards would still be a
		// leak, and only the content check catches that.
		after, readErr := os.ReadFile(victim)
		require.NoError(t, readErr)
		require.Equal(t, string(before), string(after), "a denied write must not modify the file")
	})

	t.Run("edit", func(t *testing.T) {
		_, err := service.Write(context.Background(), WriteRequest{
			Path:         foreign,
			Mode:         "edit",
			OldText:      "AGENT_B_SECRET",
			NewText:      "REWRITTEN",
			BaseRevision: revisionOf(before),
			Restrict:     agentARestriction(t),
		})
		require.ErrorIs(t, err, ErrFileNotFound)
		// Notably NOT ErrRevisionConflict: the denial must come before the
		// revision comparison, or a correct base_revision would let an edit
		// through a path that replace-mode denies.
		require.NotErrorIs(t, err, ErrRevisionConflict)
		after, readErr := os.ReadFile(victim)
		require.NoError(t, readErr)
		require.Equal(t, string(before), string(after))
	})

	// Positive control for the two modes that need an existing file: on an
	// ordinary path both modes really do run, so the denials above are not
	// the side effect of an unimplemented mode.
	_, err = service.Write(context.Background(), WriteRequest{
		Path: "src/control.txt", Content: "one\n", Mode: "create", Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	replaced, err := service.Write(context.Background(), WriteRequest{
		Path: "src/control.txt", Content: "two\n", Mode: "replace", Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	edited, err := service.Write(context.Background(), WriteRequest{
		Path: "src/control.txt", Mode: "edit", OldText: "two", NewText: "three",
		BaseRevision: replaced.Revision, Restrict: agentARestriction(t),
	})
	require.NoError(t, err)
	require.Equal(t, "three\n", readFileString(t, filepath.Join(root, "src/control.txt")))
	require.Equal(t, 1, edited.MatchedCount)
}

// TestWriteStillReachesWorkspaceFiles is the regression guard against
// over-denying. The restriction exists to keep one agent out of another's
// private tree; an implementation that also stopped the agent from writing
// code would satisfy every denial test above and make the engine useless.
func TestWriteStillReachesWorkspaceFiles(t *testing.T) {
	root := knowledgeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	restriction := agentARestriction(t)

	for _, target := range []string{"src/new.go", ".agents/knowledge/shared-note.md", "docs/deep/nested/readme.md"} {
		_, err := service.Write(context.Background(), WriteRequest{
			Path: target, Content: "ok\n", Mode: "create", Restrict: restriction,
		})
		require.NoError(t, err, "%s must remain writable", target)
		require.FileExists(t, filepath.Join(root, filepath.FromSlash(target)))
	}

	// Editing an existing ordinary file works too, and is not silently
	// converted into a create by the restriction check.
	initial, err := service.Read(context.Background(), ReadRequest{Path: "src/main.go", Restrict: restriction})
	require.NoError(t, err)
	_, err = service.Write(context.Background(), WriteRequest{
		Path: "src/main.go", Mode: "edit", OldText: "package main",
		NewText: "package changed", BaseRevision: initial.Revision, Restrict: restriction,
	})
	require.NoError(t, err)
	require.Equal(t, "package changed\n", readFileString(t, filepath.Join(root, "src/main.go")))

	// A zero Restriction stays unrestricted, which is what keeps the new
	// field additive for callers that have no opinion — the engine's own
	// fixtures and every pre-existing caller included.
	_, err = service.Write(context.Background(), WriteRequest{
		Path: ".agents/agents/b/knowledge/by-engine.md", Content: "engine\n", Mode: "create",
	})
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// The engine's runtime data directory (batch R3, hole 2)
//
// .agents/data is the engine's own session record. It is denied for every
// caller through the same restriction mechanism as the knowledge trees, and
// these tests pin both halves of that: the tools cannot reach it, and the
// engine's own storage layer still can (TestSessionPersistenceUnaffected).
// ---------------------------------------------------------------------------

func TestDataDirectoryIsNotReadable(t *testing.T) {
	root := dataTreeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// derivedFromAgentA is the restriction an agent-a turn actually runs
	// under, computed from the identity rather than hand-written, so this
	// test also fails if the data directory is dropped from the derivation
	// in internal/tool. A hardcoded Denied list here would keep passing
	// while the running engine leaked.
	restriction := useDerivedRestriction(t)

	// Read: every file in the tree, including the agent's own session.
	for _, target := range []string{
		".agents/data/sessions/fs-1/flow.jsonl",
		".agents/data/sessions/fs-1/team.jsonl",
		".agents/data/sessions/fs-1/agent.jsonl",
		".agents/data/sessions/fs-1/agents/team/call/state.md",
	} {
		_, err := service.Read(context.Background(), ReadRequest{Path: target, Restrict: restriction})
		require.ErrorIs(t, err, ErrFileNotFound, "%s must not be readable", target)
	}

	// Search: the raw model output and the state marker must not surface,
	// even from a search rooted at the workspace root.
	result, err := service.Search(context.Background(), SearchRequest{
		Pattern: "MODEL_OUTPUT_TEXT", Path: ".", Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, result.Matches)

	result, err = service.Search(context.Background(), SearchRequest{
		Pattern: "GOAL_STATE_MARKER", Path: ".", Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, result.Matches)

	// Glob: the filenames are withheld too. Listing "fs-1/agent.jsonl" is
	// itself information about sessions the caller is not part of.
	matches, err := service.GlobWithOptions(context.Background(), GlobRequest{
		Pattern: "**/*.jsonl", Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, matches)

	matches, err = service.GlobWithOptions(context.Background(), GlobRequest{
		Pattern: "**/state.md", Restrict: restriction,
	})
	require.NoError(t, err)
	require.Empty(t, matches)

	// Positive control: a search rooted at the data directory returns
	// nothing while an identical search of the workspace still works, so
	// the emptiness above is the restriction and not a broken walk.
	result, err = service.Search(context.Background(), SearchRequest{
		Pattern: "package main", Path: ".", Restrict: restriction,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.Matches, "ordinary workspace content must still be searchable")
}

func TestDataDirectoryIsNotWritable(t *testing.T) {
	root := dataTreeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	restriction := useDerivedRestriction(t)

	// Including the caller's own session directory: the denial is a property
	// of the directory, not of who owns the session inside it.
	for _, target := range []string{
		".agents/data/sessions/fs-1/agent.jsonl",
		".agents/data/sessions/fs-1/agents/team/call/state.md",
		".agents/data/sessions/fs-2/agent.jsonl",
	} {
		_, err := service.Write(context.Background(), WriteRequest{
			Path: target, Content: "FORGED\n", Mode: "replace", Restrict: restriction,
		})
		require.ErrorIs(t, err, ErrFileNotFound, "%s must not be writable", target)
	}

	// Nothing was created or modified.
	require.Equal(t, `{"payload":{"text":"MODEL_OUTPUT_TEXT"}}`+"\n",
		readFileString(t, filepath.Join(root, ".agents/data/sessions/fs-1/agent.jsonl")))
	require.NoFileExists(t, filepath.Join(root, ".agents/data/sessions/fs-2/agent.jsonl"))

	// create mode cannot smuggle a file in either, including into the
	// uploads subtree the media store owns.
	_, err = service.Write(context.Background(), WriteRequest{
		Path: ".agents/data/uploads/forged", Content: "FORGED\n", Mode: "create", Restrict: restriction,
	})
	require.ErrorIs(t, err, ErrFileNotFound)
	require.NoFileExists(t, filepath.Join(root, ".agents/data/uploads/forged"))
}

// TestDataDenialAppliesWithoutIdentity pins that the data directory is denied
// for everyone, which is the one place this differs from the knowledge trees:
// those are denied-with-an-allow-back, this is denied outright. A caller with
// no scope at all must not be able to reach it either — an implementation that
// hung the data denial off the caller's identity would open it for exactly the
// callers whose identity is unknown.
func TestDataDenialAppliesWithoutIdentity(t *testing.T) {
	root := dataTreeFixture(t)
	service, err := NewLocal(root)
	require.NoError(t, err)

	// The restriction an unscoped caller's tool call actually gets. Derived,
	// not spelled out: the claim under test is that the derivation denies
	// the data directory when there is no identity to attribute, and a
	// hand-written list would assert nothing about the derivation.
	anonymous := useDerivedRestriction(t)
	require.Empty(t, anonymous.Allowed, "an identity-less caller can be allowed nothing")

	_, err = service.Read(context.Background(), ReadRequest{
		Path: ".agents/data/sessions/fs-1/agent.jsonl", Restrict: anonymous,
	})
	require.ErrorIs(t, err, ErrFileNotFound)

	_, err = service.Write(context.Background(), WriteRequest{
		Path: ".agents/data/sessions/fs-1/agent.jsonl", Content: "FORGED\n",
		Mode: "replace", Restrict: anonymous,
	})
	require.ErrorIs(t, err, ErrFileNotFound)

	// And a zero restriction — no restriction at all — still reaches it,
	// which is the honest statement of what this batch changes: the deny
	// lives in the restriction the caller passes, not in the workspace. A
	// caller that passes nothing gets the old behaviour, which is why the
	// tool layer must always pass the computed restriction.
	read, err := service.Read(context.Background(), ReadRequest{
		Path: ".agents/data/sessions/fs-1/agent.jsonl",
	})
	require.NoError(t, err)
	require.Contains(t, read.Content, "MODEL_OUTPUT_TEXT")
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

// matchPaths extracts the Path field of every match, which is what the
// assertions above compare against.
func matchPaths(matches []SearchMatch) []string {
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		paths = append(paths, match.Path)
	}
	return paths
}
