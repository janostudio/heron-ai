package knowledge

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestMarkdownStoreSaveLoadAndRebuildIndex(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")
	entry := types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Summary: "Retry requests must use the same idempotency key.",
		Content: "Retry requests must use the same idempotency key.",
		Keys:    []string{"payment", "retry", "idempotency"},
		Scope:   types.Scope{Type: "flow"},
		Status:  "active",
	}

	require.NoError(t, store.Save(context.Background(), entry))
	require.NoError(t, store.RebuildIndex(context.Background()))

	data, err := files.Read(".agents/knowledge/index.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "payment-idempotency.md")

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "payment-idempotency", entries[0].ID)
	require.Equal(t, "Payment Idempotency", entries[0].Title)
	require.Equal(t, "active", entries[0].Status)
	require.True(t, strings.HasSuffix(entries[0].Path, "payment-idempotency.md"))
}

func TestMarkdownStoreIgnoresIndexAndRejectsPathEscape(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")
	require.NoError(t, files.Write(".agents/knowledge/index.md", []byte("# Index")))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Empty(t, entries)

	// A path that leaves the configuration tree entirely. The guard was widened
	// to "under the config root" so Save can write a private entry to
	// .agents/agents/<id>/knowledge/ — one level sideways from this store's
	// root — but "outside .agents" is still a rejection, and must be, or a
	// caller could write knowledge anywhere on disk.
	err = store.Save(context.Background(), types.KnowledgeEntry{
		ID:      "bad",
		Path:    ".agents/../outside.md",
		Content: "bad",
		Scope:   types.Scope{Type: "flow"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes root")

	err = store.Save(context.Background(), types.KnowledgeEntry{
		ID:      "bad-absolute",
		Path:    filepath.Join(t.TempDir(), "elsewhere.md"),
		Content: "bad",
		Scope:   types.Scope{Type: "flow"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes root")
}

// TestMarkdownStoreSaveAcceptsPrivateLocation is the counterpart to the escape
// test above, and the reason ensureWithinRoot could not stay "under the store
// root": a private entry's home is one level sideways from the global store
// (.agents/agents/<id>/knowledge/ vs .agents/knowledge/), so the guard has to
// allow everything under the config root those two share. Getting this wrong
// would not fail loudly — Save would reject the layout the loader demands, and
// `heron knowledge` would report a save error for every agent-scoped rule it
// distilled.
func TestMarkdownStoreSaveAcceptsPrivateLocation(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "agent-rule",
		Content: "private",
		Scope:   types.Scope{Type: "agent", Agents: []string{"agent-a"}},
	})
	require.NoError(t, err)
	require.True(t, files.Exists(".agents/agents/agent-a/knowledge/agent-rule.md"))

	// The write round-trips through the store that owns that location.
	privateStore := NewMarkdownStore(files, ".agents/agents/agent-a/knowledge")
	entries, err := privateStore.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "agent-rule", entries[0].ID)
}

func TestMarkdownStore_UpsertActiveVersionBump(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	first, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Content: "Retry requests must use the same idempotency key.",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)
	require.Equal(t, "active", first.Status)

	second, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Content: "Updated idempotency rule.",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)
	require.Equal(t, 2, second.Version)
	require.Equal(t, "active", second.Status)

	// LoadAll must reveal both versions: the deprecated v1 and active v2.
	all, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Load excludes archived only; deprecated is still returned here and is
	// filtered at search time. The active v2 must be present.
	loaded, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, loaded, 2)
	var activeVersion int
	for _, e := range loaded {
		if e.Status == "active" {
			activeVersion = e.Version
		}
	}
	require.Equal(t, 2, activeVersion)
}

// TestMarkdownStore_UpsertActiveKeepsDeprecatedBody guards the read-modify-write
// path against the body-less Load. UpsertActive finds the previous version and
// writes it out as <id>.v<n>.md; if that lookup ever loses the body, every
// superseded version silently becomes an empty file.
func TestMarkdownStore_UpsertActiveKeepsDeprecatedBody(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Content: "the original v1 body",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	_, err = store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Content: "the replacement v2 body",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	deprecated, err := files.Read(".agents/knowledge/payment-idempotency.v1.md")
	require.NoError(t, err)
	require.Equal(t, "the original v1 body", markdownBody(t, string(deprecated)),
		"the deprecated version must keep its body")

	active, err := files.Read(".agents/knowledge/payment-idempotency.md")
	require.NoError(t, err)
	require.Equal(t, "the replacement v2 body", markdownBody(t, string(active)),
		"the active version must keep its body")
}

func TestMarkdownStore_ArchiveAndLoadFiltering(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "keep-me",
		Title:   "Keep",
		Content: "keep",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)
	_, err = store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "archive-me",
		Title:   "Archive",
		Content: "archive",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	require.NoError(t, store.Archive(context.Background(), "archive-me"))

	active, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, active, 1)
	require.Equal(t, "keep-me", active[0].ID)

	all, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 2)

	// The archived file must still exist on disk, in the location its scope implies.
	require.True(t, files.Exists(".agents/knowledge/archive-me.md"))
}

// TestMarkdownStore_ArchiveKeepsBody is the Archive half of the guard above:
// Archive rewrites the file in place with a new Status, so it must read with
// the body or the archived entry is left with an empty document.
//
// The assertion is on the body section specifically, not on the whole file.
// A blanket Contains over the file passes even when the body is gone, because
// a missing Summary is derived from the body and lands in the frontmatter —
// so the same text survives in a different field and the test would not notice
// the regression it exists to catch.
func TestMarkdownStore_ArchiveKeepsBody(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	const body = "the body that must survive archiving"
	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "archive-me",
		Title:   "Archive",
		Content: body,
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	require.NoError(t, store.Archive(context.Background(), "archive-me"))

	data, err := files.Read(".agents/knowledge/archive-me.md")
	require.NoError(t, err)
	require.Equal(t, body, markdownBody(t, string(data)),
		"archiving must round-trip the document body")
	require.Contains(t, string(data), "status: archived")
}

// markdownBody returns everything after the closing frontmatter delimiter, i.e.
// the document body as `encodeMarkdown` writes it. Tests assert on this rather
// than the raw file so a value surviving in frontmatter cannot masquerade as a
// surviving body.
func markdownBody(t *testing.T, raw string) string {
	t.Helper()
	const delimiter = "---"
	rest := strings.TrimPrefix(strings.ReplaceAll(raw, "\r\n", "\n"), delimiter+"\n")
	end := strings.Index(rest, "\n"+delimiter)
	require.GreaterOrEqual(t, end, 0, "frontmatter delimiter not found in:\n%s", raw)
	return strings.TrimSpace(rest[end+len(delimiter)+1:])
}

func TestMarkdownStore_FindDuplicate(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Summary: "Retry requests must use the same idempotency key.",
		Content: "Retry requests must use the same idempotency key.",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	dup, err := store.FindDuplicate(context.Background(), types.KnowledgeEntry{
		ID:      "another-id",
		Content: "idempotency key for retries",
	})
	require.NoError(t, err)
	require.NotNil(t, dup)
	require.Equal(t, "payment-idempotency", dup.ID)

	// A genuinely unrelated candidate must not match.
	unrelated, err := store.FindDuplicate(context.Background(), types.KnowledgeEntry{
		ID:      "another-id",
		Content: "quantum banana sandwich",
	})
	require.NoError(t, err)
	require.Nil(t, unrelated)
}

// TestLoadDoesNotPopulateContent is the core invariant of layered disclosure:
// the index view of a store must hold metadata and a path, never the body. If
// Content ever comes back non-empty here, every entry's full text is resident
// in memory and can be injected wholesale again.
func TestLoadDoesNotPopulateContent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
		Content: "Retry requests must use the same idempotency key.",
		Scope:   types.Scope{Type: "flow"},
	})
	require.NoError(t, err)

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.Empty(t, entries[0].Content)
	require.NotEmpty(t, entries[0].Path, "Path is what a later body read resolves against")
	require.True(t, strings.HasSuffix(entries[0].Path, "payment-idempotency.md"))

	// LoadAll is the same view; it must not re-introduce bodies either.
	all, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Empty(t, all[0].Content)
}

func TestSummaryDerivedWhenFrontmatterMissing(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	// No frontmatter summary, and the body opens with a heading — the derived
	// summary must skip the heading and take the first real paragraph.
	require.NoError(t, files.Write(".agents/knowledge/flow/derived.md", []byte(
		"---\nid: derived\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# Payment Idempotency\n\n"+
			"Retry requests must reuse the\nsame idempotency key.\n\n"+
			"## Details\n\nMore text that must not be picked.\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	require.Equal(t, "derived", entries[0].ID)
	require.Equal(t, "Retry requests must reuse the same idempotency key.", entries[0].Summary,
		"summary should be the first non-heading paragraph with newlines collapsed")

	// The derived summary must not be written back to the file.
	data, err := files.Read(".agents/knowledge/flow/derived.md")
	require.NoError(t, err)
	require.NotContains(t, string(data), "summary:")
}

func TestExplicitSummaryWinsOverDerived(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/explicit.md", []byte(
		"---\nid: explicit\nsummary: Author-written summary.\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# Heading\n\nA body paragraph that must lose.\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "Author-written summary.", entries[0].Summary)

	// LoadAll derives through the same rule; assert it agrees.
	all, err := store.LoadAll(context.Background())
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "Author-written summary.", all[0].Summary)
}

func TestDerivedSummaryTruncated(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	long := strings.Repeat("a", maxDerivedSummaryRunes*2)
	require.NoError(t, files.Write(".agents/knowledge/flow/long.md", []byte(
		"---\nid: long\nscope:\n  type: flow\nstatus: active\n---\n\n"+long+"\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	got := entries[0].Summary
	require.Equal(t, maxDerivedSummaryRunes+len("..."), len([]rune(got)))
	require.True(t, strings.HasSuffix(got, "..."))
	require.Equal(t, strings.Repeat("a", maxDerivedSummaryRunes), strings.TrimSuffix(got, "..."))
}

// TestDeriveSummaryFromBody covers the derivation rule directly.
//
// The rule has three tiers, and the lower ones exist because an entry with no
// summary is effectively unsearchable: retrieval matches metadata only, so a
// blank summary means the entry can be found by its ID and nothing else. A
// heading or a list item is a weaker selection signal than a sentence, but it
// is strictly better than the nothing the model would otherwise get. Only a
// body with no text at all yields "".
func TestDeriveSummaryFromBody(t *testing.T) {
	cases := map[string]string{
		"":                       "",
		"# Only A Heading":       "Only A Heading",
		"## A\n\n### B\n":        "A",
		"plain paragraph":        "plain paragraph",
		"# Heading\n\nparagraph": "paragraph",
		"first\n\nsecond":        "first",
		"line one\nline two":     "line one line two",
		"# Title\n\n>\tindented": "> indented",
		// A heading with content on the next line and no blank between them is
		// one Markdown block; the content must win over the heading.
		"## Title\n- point one":  "point one",
		"# T\n\n## Section\n- a": "a",
		// List items are tier two: concrete about coverage when no prose exists.
		"- alpha\n- beta":         "alpha; beta",
		"\n\n# H\n\n\n\nreal one": "real one",
	}
	for body, want := range cases {
		if got := deriveSummaryFromBody(body); got != want {
			t.Errorf("deriveSummaryFromBody(%q) = %q, want %q", body, got, want)
		}
	}
}

// TestSummaryDerivedTruncatesOnRuneBoundary pins the unit of truncation: a CJK
// body must be cut at the same character count as a Latin one, not the same
// byte count (UTF-8 uses three bytes per character here).
func TestSummaryDerivedTruncatesOnRuneBoundary(t *testing.T) {
	body := strings.Repeat("知", maxDerivedSummaryRunes+50)
	got := deriveSummaryFromBody(body)

	require.Equal(t, maxDerivedSummaryRunes+len("..."), len([]rune(got)))
	require.Equal(t, strings.Repeat("知", maxDerivedSummaryRunes)+"...", got)
}

// TestLoadLeavesContentEmpty is the load-path counterpart of the removed
// TestSearchIgnoresContentOnlyTerms.
//
// That test proved the body did not participate in *retrieval*; with retrieval
// deleted the same property has to be asserted where it still means something:
// the body must not be resident at all. The fixture's body carries a term that
// appears nowhere else, and the assertion is that it is absent from the loaded
// entry — if Content were populated, every knowledge document in the tree
// would be in memory for no reason.
func TestLoadLeavesContentEmpty(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/body-only.md", []byte(
		"---\nid: body-only\ntitle: A Title\nsummary: Nothing to see here.\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# Heading\n\nThe body mentions zarquon but nothing else does.\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].Content, "the metadata view must not hold the body")
	// Guard the fixture itself: derivation must not have pulled the body-only
	// term into the summary, or the assertion above would prove nothing.
	require.NotContains(t, entries[0].Summary, "zarquon")

	// The body is still on disk and still reachable — by path, which is how
	// the model reads it now. This is the capability that must survive the
	// deletion of ReadKnowledge.
	raw, err := files.Read(entries[0].Path)
	require.NoError(t, err)
	require.Contains(t, string(raw), "zarquon")
}

// TestLoadDerivesSummaryForContentlessEntry is the counterweight to the test
// above: the summary is derived from the body even though the body is
// discarded, because summary is what RebuildIndex and FindDuplicate read.
func TestLoadDerivesSummaryForContentlessEntry(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/no-frontmatter.md", []byte(
		"---\nid: no-frontmatter\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# Heading\n\nPayments must reuse the idempotency key on retry.\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].Content)
	require.Contains(t, entries[0].Summary, "Payments must reuse the idempotency key on retry.")
}

// TestRebuildIndexUsesDerivedSummary locks in the behaviour that replaced the
// removed firstLine(Content) fallback: the index file is rendered from
// entry.Summary, which Load has already derived when frontmatter omitted it.
func TestRebuildIndexUsesDerivedSummary(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/no-frontmatter.md", []byte(
		"---\nid: no-frontmatter\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# Heading\n\nPayments must reuse the idempotency key on retry.\n",
	)))

	require.NoError(t, store.RebuildIndex(context.Background()))

	data, err := files.Read(".agents/knowledge/index.md")
	require.NoError(t, err)
	require.Contains(t, string(data), "Payments must reuse the idempotency key on retry.")
	require.Contains(t, string(data), "flow/no-frontmatter.md")
}

func TestArchiveRejectsUnknownID(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.Error(t, store.Archive(context.Background(), "nope"))
	require.Error(t, store.Archive(context.Background(), "  "))
}

func TestDerivedSummaryOfEmptyBodyIsEmpty(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/empty.md", []byte(
		fmt.Sprintf("---\nid: empty\nscope:\n  type: flow\nstatus: active\n---\n\n%s\n", ""),
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Empty(t, entries[0].Summary)
	require.Equal(t, "empty", entries[0].ID)
}

// TestDerivedSummaryFromHeadingPlusListBody pins the shape that made the
// original paragraph-based rule fail on this repository's own data.
//
// A document written as "# Title", blank, "## Section", then a bullet list with
// no blank line after the sub-heading has no blank-line-separated paragraph
// that does not start with "#". A rule that skips such paragraphs yields an
// empty summary, and an entry with an empty summary can only be found by its
// ID — the knowledge is present but effectively unreachable.
func TestDerivedSummaryFromHeadingPlusListBody(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	require.NoError(t, files.Write(".agents/knowledge/flow/seo.md", []byte(
		"---\nid: seo-guide\nscope:\n  type: flow\nstatus: active\n---\n\n"+
			"# SEO 写作指南\n\n"+
			"## 标题优化\n"+
			"- 标题长度 20-70 字符最佳\n"+
			"- 包含主要关键词在前 30% 位置\n\n"+
			"## 内容结构\n"+
			"- 每段不超过 3-4 句话\n",
	)))

	entries, err := store.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 1)

	summary := entries[0].Summary
	require.NotEmpty(t, summary, "a heading+list body must still yield a usable summary")
	require.Contains(t, summary, "标题长度 20-70 字符最佳",
		"the summary must describe coverage, taken from the list under the heading")
	// The body must not be adopted wholesale as the summary.
	require.NotContains(t, summary, "每段不超过", "only the opening block belongs in a summary")
}
