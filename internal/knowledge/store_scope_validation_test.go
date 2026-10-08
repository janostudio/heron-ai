package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// These tests cover load-time scope validation: the check that a knowledge
// file's frontmatter `scope` agrees with the directory it lives in.
//
// The rules exist because visibility is being converged onto path semantics
// (docs/skill-progressive-disclosure.md §3.4). The engine is moving to agentic
// search, where the model greps the tree itself; the decision "may this caller
// see this file?" then happens in a path filter on the way in, and a path
// filter cannot read a YAML field out of the file it is deciding about. A
// frontmatter scope is therefore unenforceable by construction, so the only
// complete guarantee left is that where a file lives is what it is.
//
// Every rejection below is a case where the two statements disagree, and each
// would be silent without the check:
//
//   - a private scope in the global directory has no path expression, so the
//     path filter would grant it to everyone (a leak);
//   - a private scope naming a different owner than the directory is worse —
//     the filter grants the directory's owner while the file claims someone
//     else, so one of the two is overruled without notice.
//
// The tests assert on the *message*, not just that an error occurred, because
// the message is the whole product: a user hitting this is looking at a tree
// they did not necessarily write (examples/ in this repository is full of
// them), and a bare "invalid scope" gives them nothing to act on. See the
// helper at the bottom for the shared assertion.

func writeKnowledgeFile(t *testing.T, files storage.FileStore, path, body string) {
	t.Helper()
	require.NoError(t, files.Write(path, []byte(body)))
}

// privateStoreFor builds the store internal/app actually constructs for an
// agent's private knowledge: one rooted at the private directory itself.
//
// A global store cannot reach .agents/agents/<id>/knowledge/ — listMarkdown
// walks down from the store root, and those directories are siblings of
// .agents/knowledge, not children — so tests of private locations must read
// through the private store, exactly as the engine does. Using a single global
// store here would "pass" by finding nothing, which proves nothing about the
// rule under test.
func privateStoreFor(t *testing.T, files storage.FileStore, agentID string) *MarkdownStore {
	t.Helper()
	return NewMarkdownStore(files, ".agents/agents/"+agentID+"/knowledge")
}

// TestLoadAcceptsGlobalLocationWithFlowScope is the baseline: the common case
// must keep working, since almost every existing entry looks like this.
func TestLoadAcceptsGlobalLocationWithFlowScope(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/gitignore.md",
		"---\nid: gitignore\ntitle: Gitignore\nscope:\n  type: flow\nstatus: active\n---\n\nIgnore build output.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "gitignore", entries[0].ID)
	require.Equal(t, "flow", entries[0].Scope.Type)
}

// TestLoadAcceptsGlobalLocationWithAllScope is the other spelling every example
// tree uses. "all" and "flow" mean the same thing here, and both mean "the
// location rather than a path rule decides who sees this".
func TestLoadAcceptsGlobalLocationWithAllScope(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/writing-style.md",
		"---\nid: writing-style\nscope:\n  type: all\nstatus: active\n---\n\nA global style guide.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

// TestLoadRejectsAgentScopeInGlobalLocation is the silent-leak case, and the
// reason this batch exists.
//
// Before the check, this file was private by frontmatter and the engine
// enforced that with a scope predicate at retrieval time. Under agentic search
// the enforcement point is a path filter, and .agents/knowledge/x.md carries
// nothing that says "agent a" — so every agent would be handed it. Failing the
// load is the only outcome that neither leaks the file nor silently pretends
// the scope still means something.
func TestLoadRejectsAgentScopeInGlobalLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/secret.md",
		"---\nid: secret\nscope:\n  type: agent\n  agents: [agent-a]\nstatus: active\n---\n\nOnly agent a should see this.\n")

	_, err := store.Load(context.Background())
	requireScopeConflictError(t, err,
		"secret.md",
		"agent-private to \"agent-a\"",
		"global (.agents/knowledge/)",
		".agents/agents/<agent-id>/knowledge/",
	)
}

// TestLoadRejectsTeamScopeInGlobalLocation is the same leak one level up: a
// team-scoped entry in the shared directory is visible to every team once the
// path is the only filter.
func TestLoadRejectsTeamScopeInGlobalLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/team-secret.md",
		"---\nid: team-secret\nscope:\n  type: team\n  teams: [team-a]\nstatus: active\n---\n\nOnly team a should see this.\n")

	_, err := store.Load(context.Background())
	requireScopeConflictError(t, err,
		"team-secret.md",
		"team-private to \"team-a\"",
		"global (.agents/knowledge/)",
		".agents/teams/<team-id>/knowledge/",
	)
}

// TestLoadRejectsOwnerlessAgentScope guards the write-path fallback. Save
// cannot derive a directory for a private scope that names nobody, so it puts
// the file in the shared one — and this is where that lands.
//
// The entry is not a leak (the shared directory is readable by all, so nothing
// hides it) but it is an unenforceable claim: the frontmatter says "one agent
// may read this" while the layout says "everyone", and no path filter can
// reconcile the two. Rejecting it is what stops the file from asserting a
// privacy the engine does not implement.
func TestLoadRejectsOwnerlessAgentScope(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/orphan.md",
		"---\nid: orphan\nscope:\n  type: agent\nstatus: active\n---\n\nNobody owns this.\n")

	_, err := store.Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "orphan.md")
	require.Contains(t, err.Error(), "names no agent in scope.agents")
	require.Contains(t, err.Error(), "Fix:")
}

func TestLoadAcceptsAgentPrivateLocationWithMatchingScope(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	// The private store, because that is the one that can see this file.
	store := privateStoreFor(t, files, "agent-a")

	writeKnowledgeFile(t, files, ".agents/agents/agent-a/knowledge/checklist.md",
		"---\nid: checklist\nscope:\n  type: agent\n  agents: [agent-a]\nstatus: active\n---\n\nAgent a's checklist.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "agent", entries[0].Scope.Type)
	require.Equal(t, []string{"agent-a"}, entries[0].Scope.Agents)
}

// TestLoadRejectsAgentPrivateLocationWithMismatchedOwner is the misattribution
// case: the directory and the frontmatter name two different agents.
//
// A path filter will grant this file to agent-a, because that is whose
// directory it is — while the file itself says agent-b. Whichever the author
// meant, one of the two is being overruled silently, and the failure mode is
// asymmetric: if they meant agent-b, agent-a is reading another agent's
// private notes.
func TestLoadRejectsAgentPrivateLocationWithMismatchedOwner(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := privateStoreFor(t, files, "agent-a")

	writeKnowledgeFile(t, files, ".agents/agents/agent-a/knowledge/x.md",
		"---\nid: x\nscope:\n  type: agent\n  agents: [agent-b]\nstatus: active\n---\n\nClaims to belong to b, lives with a.\n")

	_, err := store.Load(context.Background())
	requireScopeConflictError(t, err,
		"agents/agent-a/knowledge/x.md",
		"agent-private to \"agent-b\"",
		"agent-private to \"agent-a\"",
		".agents/agents/agent-b/knowledge/",
	)
}

// TestLoadRejectsTeamScopeInAgentPrivateLocation covers the cross-kind
// mismatch, which the same-owner check alone would not catch: the owner name is
// never compared because the kinds differ first.
func TestLoadRejectsTeamScopeInAgentPrivateLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := privateStoreFor(t, files, "agent-a")

	writeKnowledgeFile(t, files, ".agents/agents/agent-a/knowledge/x.md",
		"---\nid: x\nscope:\n  type: team\n  teams: [team-a]\nstatus: active\n---\n\nTeam scoped in an agent's directory.\n")

	_, err := store.Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "x.md")
	require.Contains(t, err.Error(), "sits in an agent-private directory")
}

func TestLoadAcceptsTeamPrivateLocationWithMatchingScope(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/teams/team-a/knowledge")

	writeKnowledgeFile(t, files, ".agents/teams/team-a/knowledge/shared.md",
		"---\nid: shared\nscope:\n  type: team\n  teams: [team-a]\nstatus: active\n---\n\nTeam a's shared notes.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "team", entries[0].Scope.Type)
	require.Equal(t, []string{"team-a"}, entries[0].Scope.Teams)
}

func TestLoadRejectsTeamPrivateLocationWithMismatchedOwner(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/teams/team-a/knowledge")

	writeKnowledgeFile(t, files, ".agents/teams/team-a/knowledge/x.md",
		"---\nid: x\nscope:\n  type: team\n  teams: [team-b]\nstatus: active\n---\n\nClaims to belong to b, lives with a.\n")

	_, err := store.Load(context.Background())
	requireScopeConflictError(t, err,
		"teams/team-a/knowledge/x.md",
		"team-private to \"team-b\"",
		"team-private to \"team-a\"",
		".agents/teams/team-b/knowledge/",
	)
}

// TestLoadAcceptsBareScopeInPrivateLocation pins the normalization decision: a
// file with no scope at all, sitting in agent a's private directory, becomes
// agent-private to a *in memory*, and the file on disk is not touched.
//
// Normalizing is what keeps the model honest. Leaving Scope.Type empty would
// not make the entry visible to everyone — the path still hides it from other
// agents — it would only make in-memory state disagree with the disk: a
// reader of the loaded entry would be told "no scope" about a file whose
// location says "agent a". The enforcement no longer reads this field (the
// path filter does), which is exactly why it must not be left saying something
// the layout contradicts: nothing would catch the lie.
//
// The file-level assertion is the other half: load must not write back. A load
// that mutated the tree would make reading it a side effect, and the
// frontmatter is still the author's to change.
func TestLoadAcceptsBareScopeInPrivateLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := privateStoreFor(t, files, "agent-a")

	const path = ".agents/agents/agent-a/knowledge/notes.md"
	writeKnowledgeFile(t, files, path,
		"---\nid: notes\ntitle: Notes\nstatus: active\n---\n\nNo scope declared.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.Equal(t, "agent", entries[0].Scope.Type,
		"a bare scope in a private directory is private to that directory's owner")
	require.Equal(t, []string{"agent-a"}, entries[0].Scope.Agents)

	// The visibility the entry now carries is the one the layout enforces.
	location, ok := locationScopeOf(store.root, entries[0].Path)
	require.True(t, ok)
	require.Equal(t, locationAgent, location.Kind)
	require.Equal(t, "agent-a", location.OwnerID,
		"the location the loader derived must agree with the scope it settled on")

	// Load did not rewrite the file: still no scope in the frontmatter.
	data, err := files.Read(path)
	require.NoError(t, err)
	require.NotContains(t, string(data), "scope:")
}

// TestLoadAcceptsBareScopeInTeamPrivateLocation is the team counterpart, and it
// also pins the "flow"/"all" reading: those widen to everyone in the global
// directory, but inside a private one the file is private to the owner.
//
// This is a judgement call, not a deduction, and it is recorded as one: the
// location and the frontmatter disagree, and the narrower reading wins because
// the wider one is exactly the leak this batch closes. An entry that truly
// means "everyone" belongs in .agents/knowledge/, and the validation error for
// a *contradiction* tells the author how to get there.
func TestLoadAcceptsBareScopeInTeamPrivateLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/teams/team-a/knowledge")

	writeKnowledgeFile(t, files, ".agents/teams/team-a/knowledge/notes.md",
		"---\nid: notes\nscope:\n  type: flow\nstatus: active\n---\n\nThe global spelling inside a private directory.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "team", entries[0].Scope.Type,
		"a location-private file is not made global by a flow scope; the location is the narrower statement")
	require.Equal(t, []string{"team-a"}, entries[0].Scope.Teams)
}

// TestLoadValidationErrorIsNotSilentlySwallowed covers the wiring, not the rule.
//
// A validation failure that the caller discards is worse than no validation at
// all: the engine comes up looking healthy with the knowledge feature quietly
// switched off, which is indistinguishable from "there is no knowledge in this
// workspace" and therefore undiagnosable. internal/app used to read knowledge
// as `if entries, loadErr := store.Load(ctx); loadErr == nil`, which collapses
// every failure — including this one — into the empty case.
//
// The assertion is at the store level because that is where the contract is:
// Load returns the error, and callers must propagate. internal/app's own
// propagation is covered by the build-time guard in that package.
func TestLoadValidationErrorIsNotSilentlySwallowed(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/leaky.md",
		"---\nid: leaky\nscope:\n  type: agent\n  agents: [agent-a]\nstatus: active\n---\n\nShould never load.\n")

	entries, err := store.Load(context.Background())
	require.Error(t, err, "the error must reach the caller")
	require.Nil(t, entries, "no partial result may be returned alongside the error")

	// LoadAll and the archive path go through the same reader, so the same
	// broken tree cannot slip in through a lifecycle command instead.
	_, err = store.LoadAll(context.Background())
	require.Error(t, err, "LoadAll must reject the same tree")
	require.Error(t, store.Archive(context.Background(), "leaky"),
		"the archive path reads through load and must reject it too")
}

// TestLoadRejectsArchivedEntryScopeConflictToo pins that validation is not
// skipped for entries the index then drops. An archived file with a
// contradicting scope is still evidence of a broken tree, and reporting it only
// once the entry is un-archived would make the error appear long after the
// change that caused it.
func TestLoadRejectsArchivedEntryScopeConflictToo(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	writeKnowledgeFile(t, files, ".agents/knowledge/archived-leak.md",
		"---\nid: archived-leak\nscope:\n  type: agent\n  agents: [agent-a]\nstatus: archived\n---\n\nArchived but still misplaced.\n")

	_, err := store.Load(context.Background())
	require.Error(t, err)
	require.Contains(t, err.Error(), "archived-leak.md")
}

// requireScopeConflictError asserts the four things an actionable message must
// say, given a validation failure: which file, what the frontmatter claimed,
// what the location implies, and how to fix it. A message that omits the fix is
// only marginally better than no error at all — the reader knows something is
// wrong and still has to reverse-engineer the path convention to act.
func requireScopeConflictError(t *testing.T, err error, file, claimed, implied, fix string) {
	t.Helper()
	require.Error(t, err, "expected a scope/location conflict")
	msg := err.Error()
	require.Contains(t, msg, file, "the error must name the file")
	require.Contains(t, msg, claimed, "the error must quote what the frontmatter says")
	require.Contains(t, msg, implied, "the error must state what the location implies")
	require.Contains(t, msg, "Fix:", "the error must label its remediation")
	require.Contains(t, msg, fix, "the error must say how to fix it")
}

// TestLocationScopeOfEncodesTheConvention covers the derivation directly, since
// it is the single place the path convention is encoded and both the validator
// and the next batch's path filter depend on it.
func TestLocationScopeOfEncodesTheConvention(t *testing.T) {
	const root = ".agents/knowledge"

	cases := []struct {
		name    string
		path    string
		want    locationScope
		wantOK  bool
		comment string
	}{
		{
			name: "global file",
			path: ".agents/knowledge/gitignore.md",
			want: locationScope{Kind: locationGlobal},
			// The store's own root, which is what a global store lists.
			wantOK: true,
		},
		{
			name:   "global file in a subdirectory the old layout used",
			path:   ".agents/knowledge/proposed/kn-1.md",
			want:   locationScope{Kind: locationGlobal},
			wantOK: true,
		},
		{
			name:   "agent private",
			path:   ".agents/agents/audit-agent/knowledge/session-layout.md",
			want:   locationScope{Kind: locationAgent, OwnerID: "audit-agent"},
			wantOK: true,
		},
		{
			name:   "team private",
			path:   ".agents/teams/fix-team/knowledge/notes.md",
			want:   locationScope{Kind: locationTeam, OwnerID: "fix-team"},
			wantOK: true,
		},
		{
			name:   "leading dot slash is tolerated",
			path:   "./.agents/agents/a/knowledge/x.md",
			want:   locationScope{Kind: locationAgent, OwnerID: "a"},
			wantOK: true,
		},
		{
			name:   "unclean separators are tolerated",
			path:   ".agents//agents/a/knowledge/x.md",
			want:   locationScope{Kind: locationAgent, OwnerID: "a"},
			wantOK: true,
		},
		{
			name:   "innermost private directory wins",
			path:   ".agents/agents/a/knowledge/agents/b/knowledge/x.md",
			want:   locationScope{Kind: locationAgent, OwnerID: "b"},
			wantOK: true,
		},
		{
			name:   "not a knowledge location",
			path:   ".agents/skills/x/SKILL.md",
			wantOK: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := locationScopeOf(root, tc.path)
			require.Equal(t, tc.wantOK, ok, tc.comment)
			if tc.wantOK {
				require.Equal(t, tc.want, got)
			}
		})
	}
}

// TestLocationScopeOfIsRootAgnostic pins that the derivation does not depend on
// the root being spelled relatively. internal/app builds stores over
// process-relative roots while a reload may hand over an absolute config root;
// both must reach the same verdict or the same file would validate in one
// code path and not the other.
func TestLocationScopeOfIsRootAgnostic(t *testing.T) {
	const absolute = "/tmp/ws/.agents/knowledge"
	path := "/tmp/ws/.agents/agents/a/knowledge/x.md"

	location, ok := locationScopeOf(absolute, path)
	require.True(t, ok)
	require.Equal(t, locationScope{Kind: locationAgent, OwnerID: "a"}, location)
}

// TestMarkdownStorePrivateRootReadsOwnFiles covers the store shape internal/app
// actually builds for each agent: a store whose root *is* the private directory.
// Its files are listed as ".agents/agents/<id>/knowledge/<id>.md", so the owner
// is derivable from the full path even though the store root adds nothing.
//
// This is the case that had to work for the app wiring to keep loading agent
// knowledge at all — before the derivation handled the owner segment, every
// private store's entries were read as global and an agent-scoped file was
// then rejected as a leak, i.e. the engine refused to start on a tree that was
// already correct.
func TestMarkdownStorePrivateRootReadsOwnFiles(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/agents/agent-a/knowledge")

	writeKnowledgeFile(t, files, ".agents/agents/agent-a/knowledge/checklist.md",
		"---\nid: checklist\nscope:\n  type: agent\n  agents: [agent-a]\nstatus: active\n---\n\nAgent a's checklist.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "agent", entries[0].Scope.Type)
	require.Equal(t, []string{"agent-a"}, entries[0].Scope.Agents)
}

// TestNormalizationSurvivesASaveRoundTrip documents the one place the in-memory
// normalization becomes durable, and why that is acceptable: Save encodes
// whatever Scope it is handed, so an already-normalized entry that is saved
// again picks the owner up in its frontmatter. Nothing on the load path writes,
// so this only happens when a caller genuinely saves.
func TestNormalizationSurvivesASaveRoundTrip(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/agents/agent-a/knowledge")

	const path = ".agents/agents/agent-a/knowledge/notes.md"
	writeKnowledgeFile(t, files, path,
		"---\nid: notes\nstatus: active\n---\n\nNo scope declared.\n")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	// Re-saving the loaded entry is how `heron knowledge` would persist a
	// touched entry; the normalized scope is now written down.
	require.NoError(t, store.Save(context.Background(), entries[0]))

	data, err := files.Read(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "type: agent")
	require.Contains(t, string(data), "agent-a")

	// And it still loads, which is the round trip that matters.
	_, err = store.Load(context.Background())
	require.NoError(t, err)
}

// TestConfigRootRecognizesBothStoreShapes pins the derivation placementPath and
// ensureWithinRoot both depend on.
//
// The two shapes are the global store (<configRoot>/knowledge) and the per-agent
// private store (<configRoot>/agents/<id>/knowledge). Getting the private one
// wrong is not a cosmetic bug: placementPath would derive the wrong prefix for
// where to WRITE a private entry, and ensureWithinRoot would reject it, so
// `heron knowledge` would fail to persist every agent-scoped rule.
func TestConfigRootRecognizesBothStoreShapes(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())

	cases := []struct {
		root  string
		want  string
		wantK bool
	}{
		{root: ".agents/knowledge", want: ".agents", wantK: true},
		{root: ".agents/agents/agent-a/knowledge", want: ".agents", wantK: true},
		{root: ".agents/teams/team-a/knowledge", want: ".agents", wantK: true},
		{root: "/ws/.agents/knowledge", want: "/ws/.agents", wantK: true},
		{root: "/ws/.agents/agents/a/knowledge", want: "/ws/.agents", wantK: true},
		// A store rooted somewhere unrelated has no derivable config root, and
		// callers fall back to the store root itself.
		{root: "some/random/dir", wantK: false},
	}

	for _, tc := range cases {
		store := NewMarkdownStore(files, tc.root)
		got, ok := store.configRoot()
		require.Equal(t, tc.wantK, ok, "configRoot(%q)", tc.root)
		if tc.wantK {
			require.Equal(t, tc.want, got, "configRoot(%q)", tc.root)
		}
	}
}
