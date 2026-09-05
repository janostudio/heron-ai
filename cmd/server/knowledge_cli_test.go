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

func TestShouldArchive(t *testing.T) {
	now := time.Now().UTC()
	window := 15 * 24 * time.Hour

	old := now.Add(-16 * 24 * time.Hour).Format(time.RFC3339)
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	future := now.Add(1 * time.Hour).Format(time.RFC3339)

	// Expired via CreatedAt+window, no explicit ExpiresAt.
	if !shouldArchive(types.KnowledgeEntry{CreatedAt: old}, 0, now, window) {
		t.Fatal("expected old entry with 0 hits to be archived")
	}
	// Explicit ExpiresAt in the past.
	if !shouldArchive(types.KnowledgeEntry{CreatedAt: recent, ExpiresAt: old}, 5, now, window) {
		t.Fatal("expected entry with past ExpiresAt to be archived")
	}
	// Explicit future ExpiresAt: not archived.
	if shouldArchive(types.KnowledgeEntry{CreatedAt: old, ExpiresAt: future}, 0, now, window) {
		t.Fatal("expected entry with future ExpiresAt to be kept")
	}
	// Recent entry: not archived.
	if shouldArchive(types.KnowledgeEntry{CreatedAt: recent}, 0, now, window) {
		t.Fatal("expected recent entry to be kept")
	}
	// Old entry but with hits: not archived (hit_count > 0).
	if shouldArchive(types.KnowledgeEntry{CreatedAt: old}, 3, now, window) {
		t.Fatal("expected old entry with hits to be kept")
	}
}

