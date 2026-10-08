package main

import (
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestParseKnowledgeMarkdown(t *testing.T) {
	md := `---
id: payment-idempotency
kind: rule
scope: flow
status: active
confidence: high
keywords: [payment, retry, idempotency]
---

# Payment Idempotency

Retry requests must use the same idempotency key.
`

	entry, err := parseKnowledgeMarkdown(md, "s1", "flow")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if entry.ID != "payment-idempotency" {
		t.Fatalf("expected id payment-idempotency, got %q", entry.ID)
	}
	if entry.Title != "Payment Idempotency" {
		t.Fatalf("expected title, got %q", entry.Title)
	}
	if entry.Confidence != "high" {
		t.Fatalf("expected confidence high, got %q", entry.Confidence)
	}
	if entry.Scope.Type != "flow" {
		t.Fatalf("expected scope 'flow' for flow, got %q", entry.Scope.Type)
	}
	if entry.Status != "active" {
		t.Fatalf("expected status active, got %q", entry.Status)
	}
	if entry.Source != "s1" {
		t.Fatalf("expected source s1, got %q", entry.Source)
	}
	if len(entry.Keys) != 3 {
		t.Fatalf("expected 3 keys, got %v", entry.Keys)
	}
	if entry.CreatedAt == "" {
		t.Fatal("expected CreatedAt to be set")
	}
	if len(entry.Basis) != 1 || entry.Basis[0].Path != "s1" {
		t.Fatalf("expected basis session ref, got %v", entry.Basis)
	}
}

func TestParseKnowledgeMarkdownFallsBackToSessionID(t *testing.T) {
	md := `# No id in frontmatter

Some content.`
	entry, err := parseKnowledgeMarkdown(md, "s1", "team")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if entry.ID != "s1" {
		t.Fatalf("expected id s1, got %q", entry.ID)
	}
	if entry.Scope.Type != "team" {
		t.Fatalf("expected scope team, got %q", entry.Scope.Type)
	}
}

// TestShouldArchive pins the GC rule that survived the move to agentic search.
//
// GC used to have a second rule — "older than the window AND never hit" — and
// the hit half of it is gone: hits were recorded by the knowledge injector per
// query, and there is no injector any more. Rather than consult a stats file
// that would be empty forever (which would archive every sufficiently old
// entry while reporting "zero hits" as if it were a measurement), GC now
// decides on ExpiresAt alone. The tests that asserted the age/hit rules are
// deleted; the ones below assert the remaining rule and, importantly, the
// non-archiving cases that used to be covered by "has hits".
func TestShouldArchive(t *testing.T) {
	now := time.Now().UTC()

	old := now.Add(-16 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	future := now.Add(1 * time.Hour).Format(time.RFC3339)

	// Explicit ExpiresAt in the past.
	if !shouldArchive(types.KnowledgeEntry{CreatedAt: recent, ExpiresAt: old}, now) {
		t.Fatal("expected entry with past ExpiresAt to be archived")
	}
	// Explicit future ExpiresAt: not archived.
	if shouldArchive(types.KnowledgeEntry{CreatedAt: old, ExpiresAt: future}, now) {
		t.Fatal("expected entry with future ExpiresAt to be kept")
	}
	// Age alone no longer archives: without an ExpiresAt there is no signal
	// that says the entry is undesirable, and deleting on age would drop
	// knowledge the model may still be reading.
	if shouldArchive(types.KnowledgeEntry{CreatedAt: old}, now) {
		t.Fatal("age alone must not archive an entry any more")
	}
	if shouldArchive(types.KnowledgeEntry{CreatedAt: recent}, now) {
		t.Fatal("expected recent entry to be kept")
	}
	// No timestamps at all: kept.
	if shouldArchive(types.KnowledgeEntry{}, now) {
		t.Fatal("expected an entry with no dates to be kept")
	}
	// An unparsable ExpiresAt is "no expiry", not "expire now": a typo in a
	// timestamp must not delete the entry.
	if shouldArchive(types.KnowledgeEntry{ExpiresAt: "not-a-date"}, now) {
		t.Fatal("an unparsable ExpiresAt must not archive the entry")
	}
}
