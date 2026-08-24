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
		Scope:   types.Scope{Type: "all"},
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
		Scope:   types.Scope{Type: "all"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes root")
}
