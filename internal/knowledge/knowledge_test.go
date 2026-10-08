package knowledge

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// This file used to test KnowledgeIndex (Add/Search/SearchWithScope/List/
// Count) and KnowledgeExtractor. All of it is gone: retrieval moved to
// agentic search (design doc skill-progressive-disclosure §3.4), so there is
// no in-memory index to search, and the extractor only ever fed that index.
//
// What remains is the one survivor: the write path's dedup still calls
// entryMatches, so the matching rule it encodes is still live behaviour.
// Everything else about knowledge is tested through the store (store_test.go),
// the scope validation (store_scope_validation_test.go) and the pointer block
// (pointer_test.go).
//
// The tests deleted from this file, and why:
//
//	TestKnowledgeIndex_SearchEmpty                      — index deleted
//	TestKnowledgeIndex_AddAndSearch                     — index deleted
//	TestKnowledgeIndex_SearchWithScope_AllScope         — index deleted
//	TestKnowledgeIndex_SearchWithScope_TeamScopeMatch   — index deleted
//	TestKnowledgeIndex_SearchWithScope_TeamScopeNoMatch — index deleted
//	TestKnowledgeIndex_SearchWithScope_AgentScope       — index deleted
//	TestKnowledgeIndex_KeywordMatchingInContent         — index deleted
//	TestKnowledgeIndex_KeywordMatchingInKeys            — index deleted
//	TestKnowledgeIndex_SearchMatchesTermsInsideLongRuntimeQuery — index deleted;
//	    the "long runtime query" shape was the injector's input, which no longer
//	    exists
//	TestKnowledgeIndex_List                             — index deleted
//	TestKnowledgeIndex_Count                            — index deleted
//	TestKnowledgeExtractor_ExtractHighImportance        — extractor deleted
//	TestKnowledgeExtractor_ExtractSkipsLowImportance    — extractor deleted
//	TestKnowledgeExtractor_ExtractAddsToIndex           — extractor deleted
//	TestKnowledgeInjector_InjectReturnsFormattedText    — injector deleted
//	TestKnowledgeInjector_InjectNoMatchesReturnsEmpty   — injector deleted
//	TestKnowledgeInjector_FormatEntriesIncludesUsageInstruction — injector deleted
//	TestKnowledgeInjector_InjectWithAllowlistNoMatchOmitsUsageInstruction — injector deleted
//	TestKnowledgeInjector_InjectAllWithScopeFiltering   — injector deleted

// TestEntryMatchesUsesMetadataOnly pins the rule FindDuplicate depends on.
//
// The distinction that matters is which fields a stored entry is matched
// against. The stored side is loaded body-less, so its Content is empty
// whether or not the file has a body; a query built from body text therefore
// matches nothing, which is the documented and accepted limitation of the
// write-path dedup (see FindDuplicate). Asserting it here rather than only
// through FindDuplicate pins the rule independently of the store.
func TestEntryMatchesUsesMetadataOnly(t *testing.T) {
	entry := types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Summary: "Retry requests must reuse the key.",
		Keys:    []string{"payment", "retry", "idempotency"},
	}

	t.Run("matches id, title, summary and keys", func(t *testing.T) {
		for _, query := range []string{"payment-idempotency", "Payment Idempotency", "reuse the key", "idempotency"} {
			require.True(t, entryMatches(entry, query), "query %q should match the entry's metadata", query)
		}
	})

	t.Run("phrase match is case-insensitive and trimmed", func(t *testing.T) {
		require.True(t, entryMatches(entry, "  PAYMENT IDEMPOTENCY  "))
	})

	t.Run("a term inside a longer query still matches", func(t *testing.T) {
		require.True(t, entryMatches(entry,
			"Review the payment path. Check that retry reuses the key and that tests pass."))
	})

	t.Run("no overlap is a non-match", func(t *testing.T) {
		require.False(t, entryMatches(entry, "kubernetes"))
	})

	t.Run("empty and blank queries never match", func(t *testing.T) {
		for _, query := range []string{"", "   ", "\t\n"} {
			require.False(t, entryMatches(entry, query), "blank query %q must not match", query)
		}
	})

	t.Run("the body does not participate", func(t *testing.T) {
		// The stored entry has no Content (that is what Load returns), so a
		// term that exists only in the file's body cannot match. This is the
		// dedup's known blind spot, not a bug to fix here.
		require.False(t, entryMatches(entry, "zarquon"))
	})
}

// TestKnowledgeTermsDropsNoise pins the tokenization the term fallback uses.
//
// Case is preserved here and applied by entryMatches, which lowercases the
// query once before splitting; this function only decides where the boundaries
// are and which fragments are worth keeping.
func TestKnowledgeTermsDropsNoise(t *testing.T) {
	got := knowledgeTerms("Check the payment-service, retry. x")
	require.Equal(t, []string{"Check", "the", "payment-service", "retry"}, got,
		"single-character terms are dropped and duplicates are collapsed")

	require.Empty(t, knowledgeTerms("a b , ."))
}
