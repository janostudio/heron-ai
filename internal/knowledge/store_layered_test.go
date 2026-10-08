package knowledge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestMarkdownStore_LayeredDirectories pins the layout Save now produces.
//
// It replaced a type-scoped layout (<root>/flow/<id>.md, <root>/team/<id>.md,
// <root>/agent/<id>.md). The type-scoped directories are unfixable as a home
// for private knowledge: nothing in "knowledge/agent/" says *which* agent owns
// the file, so the loader — which now enforces visibility by path, because a
// path filter is the only thing agentic search can enforce — rejects a
// candidate there for claiming a privacy the path cannot express.
//
// Owner-scoped directories are the layout that can satisfy the loader, and this
// is the round-trip that proves the writer and the reader agree: every entry
// written here is read back with the scope it was written with, and would fail
// to load if Save and locationScopeOf ever drifted apart.
func TestMarkdownStore_LayeredDirectories(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	written := []types.KnowledgeEntry{
		{ID: "flow-id", Content: "flow content", Scope: types.Scope{Type: "flow"}},
		{ID: "team-id", Content: "team content", Scope: types.Scope{Type: "team", Teams: []string{"team-a"}}},
		{ID: "agent-id", Content: "agent content", Scope: types.Scope{Type: "agent", Agents: []string{"agent-a"}}},
	}
	for _, e := range written {
		if _, err := store.UpsertActive(context.Background(), e); err != nil {
			t.Fatalf("UpsertActive %s: %v", e.ID, err)
		}
	}

	// Global entries stay in the store's own tree; private entries move to the
	// owner-scoped location the loader recognizes.
	for _, path := range []string{
		".agents/knowledge/flow-id.md",
		".agents/agents/agent-a/knowledge/agent-id.md",
		".agents/teams/team-a/knowledge/team-id.md",
	} {
		if !files.Exists(path) {
			t.Fatalf("expected %s", path)
		}
	}

	// The full round trip: all three load, and each came back with the scope
	// its writer put on it. A location the loader rejects would surface here as
	// an error, not as a missing entry.
	entries, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	byID := make(map[string]types.KnowledgeEntry, len(entries))
	for _, e := range entries {
		byID[e.ID] = e
	}
	if len(entries) != 1 {
		t.Fatalf("the global store sees only its own tree; got %d entries (%v)", len(entries), byID)
	}

	// The private trees are separate stores, exactly as internal/app builds
	// them — read through one to prove the file it wrote is accepted.
	agentStore := NewMarkdownStore(files, ".agents/agents/agent-a/knowledge")
	agentEntries, err := agentStore.Load(context.Background())
	if err != nil {
		t.Fatalf("Load agent-private: %v", err)
	}
	if len(agentEntries) != 1 || agentEntries[0].ID != "agent-id" {
		t.Fatalf("expected agent-id from the agent-private store, got %#v", agentEntries)
	}
	if agentEntries[0].Scope.Type != "agent" || !containsTrimmed(agentEntries[0].Scope.Agents, "agent-a") {
		t.Fatalf("agent-private entry lost its scope: %#v", agentEntries[0].Scope)
	}

	teamStore := NewMarkdownStore(files, ".agents/teams/team-a/knowledge")
	teamEntries, err := teamStore.Load(context.Background())
	if err != nil {
		t.Fatalf("Load team-private: %v", err)
	}
	if len(teamEntries) != 1 || teamEntries[0].ID != "team-id" {
		t.Fatalf("expected team-id from the team-private store, got %#v", teamEntries)
	}
	if teamEntries[0].Scope.Type != "team" || !containsTrimmed(teamEntries[0].Scope.Teams, "team-a") {
		t.Fatalf("team-private entry lost its scope: %#v", teamEntries[0].Scope)
	}
}

// TestPlacementPathFallsBackToGlobalWhenOwnerIsMissing pins the one case where
// placement cannot be derived from the scope: scope.type says private but names
// nobody. Guessing an owner would be worse than declining — the entry lands in
// the shared directory and fails validation there, which is loud, whereas an
// invented owner would grant a real agent access to it.
func TestPlacementPathFallsBackToGlobalWhenOwnerIsMissing(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	entry := types.KnowledgeEntry{ID: "ownerless-agent", Content: "x", Scope: types.Scope{Type: "agent"}}
	if _, err := store.UpsertActive(context.Background(), entry); err != nil {
		t.Fatalf("UpsertActive: %v", err)
	}

	if !files.Exists(".agents/knowledge/ownerless-agent.md") {
		t.Fatal("expected ownerless-agent.md in the global directory")
	}

	// The write is accepted, and the read then rejects it — asserting both
	// halves matters, because only the pair shows that the placement fallback
	// lands somewhere the loader will complain about rather than somewhere it
	// silently accepts. The complaint is what makes an unscoped owner
	// discoverable instead of dead.
	_, err := store.Load(context.Background())
	require.Error(t, err, "an ownerless private entry in the shared directory must fail to load")
	require.Contains(t, err.Error(), "ownerless-agent.md", "the error must name the file")
	require.Contains(t, err.Error(), "scope.agents", "the error must name the field to fix")
}

func TestScopeDir(t *testing.T) {
	cases := map[string]string{
		"flow":    "flow",
		"team":    "team",
		"agent":   "agent",
		"":        "flow",
		"all":     "flow",
		"unknown": "flow",
	}
	for in, want := range cases {
		if got := scopeDir(in); got != want {
			t.Fatalf("scopeDir(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLocationScopeOfGlobalDirIsGlobal is the replacement for the old
// scopeAllows test. Under agentic search the question "may this caller see
// this file?" is answered by the path, so the thing worth pinning is that the
// global directory resolves to the global location — the predicate that used
// to answer it is gone.
func TestLocationScopeOfGlobalDirIsGlobal(t *testing.T) {
	location, ok := locationScopeOf(".agents/knowledge", ".agents/knowledge/style.md")
	require.True(t, ok)
	require.Equal(t, locationGlobal, location.Kind)
	require.Empty(t, location.OwnerID)
	require.False(t, location.isPrivate())
}
