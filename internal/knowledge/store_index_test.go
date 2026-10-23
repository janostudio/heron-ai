package knowledge

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestRebuildIndexListsOnlyCurrentVersions is the regression guard for
// duplicate index entries: every update keeps the superseded version on disk as
// <id>.vN.md with status "deprecated", and Load returns those too (search and
// FindDuplicate filter by status). The generated index listed every version, so
// one id appeared once per update it had ever received.
func TestRebuildIndexListsOnlyCurrentVersions(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewMarkdownStore(files, ".agents/knowledge")
	ctx := context.Background()

	first, err := store.UpsertActive(ctx, types.KnowledgeEntry{
		ID: "entry-1", Title: "entry", Content: "first", Scope: types.Scope{Type: "flow"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, first.Version)

	_, err = store.UpsertActive(ctx, types.KnowledgeEntry{
		ID: "entry-1", Title: "entry", Content: "second", Scope: types.Scope{Type: "flow"},
	})
	require.NoError(t, err)
	require.NoError(t, store.RebuildIndex(ctx))

	index, err := files.Read(".agents/knowledge/index.md")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(index), "[entry]"),
		"the index must list the entry once, not once per version:\n%s", index)
}
