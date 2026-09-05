package knowledge

import (
	"context"
	"strings"
	"testing"
)

func TestSummarizeLayeredSplitsDocuments(t *testing.T) {
	model := &capturingSummarizerModel{text: "---\nid: a\nscope: flow\n---\n\nbody a\n\n---KNOWLEDGE---\n\n---\nid: b\nscope: agent\n---\n\nbody b"}
	s := NewKnowledgeSummarizer(model, "")

	docs, err := s.SummarizeLayered(context.Background(), []LayeredSource{
		{Layer: "flow", Text: "source flow"},
		{Layer: "agent", Text: "source agent"},
	})
	if err != nil {
		t.Fatalf("SummarizeLayered: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("expected 2 documents, got %d: %#v", len(docs), docs)
	}
	if !strings.Contains(docs[0], "id: a") || !strings.Contains(docs[1], "id: b") {
		t.Fatalf("unexpected split result: %#v", docs)
	}
}

func TestSummarizeLayeredTagsSourcesWithLayer(t *testing.T) {
	model := &capturingSummarizerModel{text: "---\nid: x\nscope: flow\n---\n\nbody"}
	s := NewKnowledgeSummarizer(model, "")

	_, err := s.SummarizeLayered(context.Background(), []LayeredSource{
		{Layer: "team", Text: "team note"},
	})
	if err != nil {
		t.Fatalf("SummarizeLayered: %v", err)
	}

	joined := strings.Join(func() []string {
		var out []string
		for _, m := range model.lastMessages {
			out = append(out, m.Content)
		}
		return out
	}(), "\n")

	if !strings.Contains(joined, "[layer: team]") {
		t.Fatalf("expected layer tag, got %q", joined)
	}
	if !strings.Contains(joined, "team note") {
		t.Fatalf("expected source text, got %q", joined)
	}
}

func TestSummarizeLayeredEmptySourcesErrors(t *testing.T) {
	model := &capturingSummarizerModel{text: "entry"}
	s := NewKnowledgeSummarizer(model, "")

	_, err := s.SummarizeLayered(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on empty sources")
	}
	if model.calls != 0 {
		t.Fatalf("expected model not called, got %d calls", model.calls)
	}
}

func TestSummarizeLayeredEmptyOutputErrors(t *testing.T) {
	model := &capturingSummarizerModel{text: "   "}
	s := NewKnowledgeSummarizer(model, "")

	_, err := s.SummarizeLayered(context.Background(), []LayeredSource{{Layer: "flow", Text: "x"}})
	if err == nil {
		t.Fatal("expected error on empty output")
	}
}
