package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestKnowledgeIndex_SearchEmpty(t *testing.T) {
	idx := NewKnowledgeIndex()
	results, err := idx.Search(context.Background(), "anything")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results != nil {
		t.Fatalf("expected nil, got %v", results)
	}
}

func TestKnowledgeIndex_AddAndSearch(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Go is a programming language",
		Keys:    []string{"go", "programming", "language"},
		Scope:   types.Scope{Type: "all"},
	})

	results, err := idx.Search(context.Background(), "go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ID != "1" {
		t.Fatalf("expected ID '1', got '%s'", results[0].ID)
	}
}

func TestKnowledgeIndex_SearchWithScope_AllScope(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "test content",
		Keys:    []string{"test"},
		Scope:   types.Scope{Type: "all"},
	})

	results, err := idx.SearchWithScope(context.Background(), "test", "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestKnowledgeIndex_SearchWithScope_TeamScopeMatch(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "team specific content",
		Keys:    []string{"team"},
		Scope: types.Scope{
			Type:  "team",
			Teams: []string{"team1", "team2"},
		},
	})

	results, err := idx.SearchWithScope(context.Background(), "team", "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestKnowledgeIndex_SearchWithScope_TeamScopeNoMatch(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "team specific content",
		Keys:    []string{"team"},
		Scope: types.Scope{
			Type:  "team",
			Teams: []string{"team1", "team2"},
		},
	})

	results, err := idx.SearchWithScope(context.Background(), "team", "agent1", "team3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}
}

func TestKnowledgeIndex_SearchWithScope_AgentScope(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "agent specific content",
		Keys:    []string{"agent"},
		Scope: types.Scope{
			Type:   "agents",
			Agents: []string{"agent1"},
		},
	})

	results, err := idx.SearchWithScope(context.Background(), "agent", "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestKnowledgeIndex_KeywordMatchingInContent(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Heron AI is a multi-agent framework",
		Keys:    []string{},
		Scope:   types.Scope{Type: "all"},
	})

	results, err := idx.Search(context.Background(), "heron")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestKnowledgeIndex_KeywordMatchingInKeys(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "some content",
		Keys:    []string{"heron", "ai", "framework"},
		Scope:   types.Scope{Type: "all"},
	})

	results, err := idx.Search(context.Background(), "heron")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestKnowledgeIndex_List(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{ID: "1", Content: "first", Scope: types.Scope{Type: "all"}})
	idx.Add(types.KnowledgeEntry{ID: "2", Content: "second", Scope: types.Scope{Type: "all"}})

	results := idx.List()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestKnowledgeIndex_Count(t *testing.T) {
	idx := NewKnowledgeIndex()
	if idx.Count() != 0 {
		t.Fatalf("expected 0, got %d", idx.Count())
	}

	idx.Add(types.KnowledgeEntry{ID: "1", Content: "test", Scope: types.Scope{Type: "all"}})
	if idx.Count() != 1 {
		t.Fatalf("expected 1, got %d", idx.Count())
	}
}

func TestKnowledgeExtractor_ExtractHighImportance(t *testing.T) {
	idx := NewKnowledgeIndex()
	extractor := NewKnowledgeExtractor(idx)

	memories := []types.MemoryEntry{
		{
			Content:    "The API rate limit is 100 requests per minute",
			Importance: "high",
			Source:     "agent1",
			Round:      1,
		},
	}

	entries, err := extractor.Extract(context.Background(), memories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Source != "agent1" {
		t.Fatalf("expected source 'agent1', got '%s'", entries[0].Source)
	}
	if !strings.Contains(entries[0].ID, "agent1") {
		t.Fatalf("expected ID to contain 'agent1', got '%s'", entries[0].ID)
	}
}

func TestKnowledgeExtractor_ExtractSkipsLowImportance(t *testing.T) {
	idx := NewKnowledgeIndex()
	extractor := NewKnowledgeExtractor(idx)

	memories := []types.MemoryEntry{
		{
			Content:    "low importance memory",
			Importance: "low",
			Source:     "agent1",
			Round:      1,
		},
		{
			Content:    "medium importance memory",
			Importance: "medium",
			Source:     "agent1",
			Round:      2,
		},
	}

	entries, err := extractor.Extract(context.Background(), memories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestKnowledgeExtractor_ExtractAddsToIndex(t *testing.T) {
	idx := NewKnowledgeIndex()
	extractor := NewKnowledgeExtractor(idx)

	memories := []types.MemoryEntry{
		{
			Content:    "Critical security vulnerability found",
			Importance: "critical",
			Source:     "agent1",
			Round:      1,
		},
	}

	_, err := extractor.Extract(context.Background(), memories)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if idx.Count() != 1 {
		t.Fatalf("expected 1 entry in index, got %d", idx.Count())
	}
}

func TestKnowledgeInjector_InjectReturnsFormattedText(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Important knowledge",
		Keys:    []string{"important", "knowledge"},
		Scope:   types.Scope{Type: "all"},
	})

	injector := NewKnowledgeInjector(idx)
	result, err := injector.Inject(context.Background(), "important", "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(result, "## Knowledge Context") {
		t.Fatal("expected result to contain '## Knowledge Context'")
	}
	if !strings.Contains(result, "Important knowledge") {
		t.Fatal("expected result to contain 'Important knowledge'")
	}
}

func TestKnowledgeInjector_InjectNoMatchesReturnsEmpty(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Important knowledge",
		Keys:    []string{"important"},
		Scope:   types.Scope{Type: "all"},
	})

	injector := NewKnowledgeInjector(idx)
	result, err := injector.Inject(context.Background(), "nonexistent", "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "" {
		t.Fatalf("expected empty result, got '%s'", result)
	}
}

func TestKnowledgeInjector_InjectAllWithScopeFiltering(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Global knowledge",
		Scope:   types.Scope{Type: "all"},
	})
	idx.Add(types.KnowledgeEntry{
		ID:      "2",
		Content: "Team specific knowledge",
		Scope: types.Scope{
			Type:  "team",
			Teams: []string{"team1"},
		},
	})
	idx.Add(types.KnowledgeEntry{
		ID:      "3",
		Content: "Agent specific knowledge",
		Scope: types.Scope{
			Type:   "agents",
			Agents: []string{"agent2"},
		},
	})

	injector := NewKnowledgeInjector(idx)
	result, err := injector.InjectAll(context.Background(), "agent1", "team1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result, "Global knowledge") {
		t.Fatal("expected result to contain 'Global knowledge'")
	}
	if !strings.Contains(result, "Team specific knowledge") {
		t.Fatal("expected result to contain 'Team specific knowledge'")
	}
	if strings.Contains(result, "Agent specific knowledge") {
		t.Fatal("expected result NOT to contain 'Agent specific knowledge'")
	}
}
