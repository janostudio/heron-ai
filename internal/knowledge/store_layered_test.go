package knowledge

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestMarkdownStore_LayeredDirectories(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	for _, e := range []types.KnowledgeEntry{
		{ID: "flow-id", Content: "flow content", Scope: types.Scope{Type: "flow"}},
		{ID: "team-id", Content: "team content", Scope: types.Scope{Type: "team"}},
		{ID: "agent-id", Content: "agent content", Scope: types.Scope{Type: "agent"}},
	} {
		if _, err := store.UpsertActive(context.Background(), e); err != nil {
			t.Fatalf("UpsertActive %s: %v", e.ID, err)
		}
	}

	if !files.Exists(".agents/knowledge/flow/flow-id.md") {
		t.Fatal("expected flow/flow-id.md")
	}
	if !files.Exists(".agents/knowledge/team/team-id.md") {
		t.Fatal("expected team/team-id.md")
	}
	if !files.Exists(".agents/knowledge/agent/agent-id.md") {
		t.Fatal("expected agent/agent-id.md")
	}

	entries, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries across subdirs, got %d", len(entries))
	}
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

func TestScopeAllows_FlowVisibleToAll(t *testing.T) {
	flowScope := types.Scope{Type: "flow"}
	if !scopeAllows(flowScope, "any-agent", "any-team") {
		t.Fatal("flow scope should be visible to all agents")
	}
}
