package knowledge

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestKnowledgeInjector_RecordsHits(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Important knowledge",
		Keys:    []string{"important"},
		Scope:   types.Scope{Type: "all"},
	})

	files := storage.NewFileStore(t.TempDir())
	rec := NewStatsRecorder(files, "knowledge")

	injector := NewKnowledgeInjector(idx)
	injector.SetStatsRecorder(rec)

	_, err := injector.Inject(context.Background(), "important", "agent1", "team1")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	counts, err := rec.HitCounts()
	if err != nil {
		t.Fatalf("hit counts: %v", err)
	}
	if counts["1"] != 1 {
		t.Fatalf("expected id '1' hit once, got %d", counts["1"])
	}
}

func TestKnowledgeInjector_NoHitsWhenNoMatch(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Important knowledge",
		Keys:    []string{"important"},
		Scope:   types.Scope{Type: "all"},
	})

	files := storage.NewFileStore(t.TempDir())
	rec := NewStatsRecorder(files, "knowledge")

	injector := NewKnowledgeInjector(idx)
	injector.SetStatsRecorder(rec)

	_, err := injector.Inject(context.Background(), "nonexistent", "agent1", "team1")
	if err != nil {
		t.Fatalf("inject: %v", err)
	}

	counts, err := rec.HitCounts()
	if err != nil {
		t.Fatalf("hit counts: %v", err)
	}
	if len(counts) != 0 {
		t.Fatalf("expected no hits, got %v", counts)
	}
}

func TestKnowledgeInjector_NoRecorderIsSafe(t *testing.T) {
	idx := NewKnowledgeIndex()
	idx.Add(types.KnowledgeEntry{
		ID:      "1",
		Content: "Important knowledge",
		Keys:    []string{"important"},
		Scope:   types.Scope{Type: "all"},
	})

	injector := NewKnowledgeInjector(idx)
	if _, err := injector.Inject(context.Background(), "important", "agent1", "team1"); err != nil {
		t.Fatalf("inject without recorder should not error: %v", err)
	}
}
