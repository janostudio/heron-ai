package knowledge

import (
	"context"
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

	err = store.Save(context.Background(), types.KnowledgeEntry{
		ID:      "bad",
		Path:    ".agents/outside.md",
		Content: "bad",
		Scope:   types.Scope{Type: "flow"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes root")
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

	// The archived file must still exist on disk (in its layer subdirectory).
	require.True(t, files.Exists(".agents/knowledge/flow/archive-me.md"))
}

func TestMarkdownStore_FindDuplicate(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")

	_, err := store.UpsertActive(context.Background(), types.KnowledgeEntry{
		ID:      "payment-idempotency",
		Title:   "Payment Idempotency",
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

