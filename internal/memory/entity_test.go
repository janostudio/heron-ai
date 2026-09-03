package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestStoreEntityMemoryRoundTrip(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	loaded, err := store.LoadEntity(context.Background(), "code-fixer", "role-a")
	require.NoError(t, err)
	assert.Equal(t, types.MemoryScopeEntity, loaded.Scope)
	assert.Equal(t, 0, loaded.Revision)

	err = store.SaveEntity(context.Background(), "code-fixer", "role-a", types.MemorySnapshot{
		Goal:      "fix the assigned file",
		Confirmed: []string{"first outcome"},
	})
	require.NoError(t, err)

	data, err := files.Read(".agents/data/agents/code-fixer/role-a/memory/memory.md")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "---\n"))

	reloaded, err := store.LoadEntity(context.Background(), "code-fixer", "role-a")
	require.NoError(t, err)
	assert.Equal(t, types.MemoryScopeEntity, reloaded.Scope)
	assert.Equal(t, "fix the assigned file", reloaded.Goal)
	assert.Equal(t, []string{"first outcome"}, reloaded.Confirmed)
	assert.Equal(t, 1, reloaded.Revision)
}

func TestStoreEntityMemoryIsolatedPerEntity(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	require.NoError(t, store.SaveEntity(context.Background(), "writer", "a", types.MemorySnapshot{Goal: "goal-a"}))
	require.NoError(t, store.SaveEntity(context.Background(), "writer", "b", types.MemorySnapshot{Goal: "goal-b"}))
	// Cross-template isolation too.
	require.NoError(t, store.SaveEntity(context.Background(), "reviewer", "a", types.MemorySnapshot{Goal: "goal-other"}))

	for _, tt := range []struct {
		agent, key, goal string
	}{
		{"writer", "a", "goal-a"},
		{"writer", "b", "goal-b"},
		{"reviewer", "a", "goal-other"},
	} {
		snapshot, err := store.LoadEntity(context.Background(), tt.agent, tt.key)
		require.NoError(t, err)
		assert.Equal(t, tt.goal, snapshot.Goal)
	}
}

func TestStoreEntityMemoryPersistsAcrossSessions(t *testing.T) {
	// Entity memory is scoped by entity, not by session: a fresh Store over
	// the same directory (e.g. a new process) must observe the same snapshot.
	dir := t.TempDir()
	first := NewStore(storage.NewFileStore(dir), Limits{})
	require.NoError(t, first.SaveEntity(context.Background(), "worker", "k1", types.MemorySnapshot{
		Decisions: []string{"use option B"},
	}))

	second := NewStore(storage.NewFileStore(dir), Limits{})
	snapshot, err := second.LoadEntity(context.Background(), "worker", "k1")
	require.NoError(t, err)
	assert.Equal(t, []string{"use option B"}, snapshot.Decisions)
	assert.Equal(t, 1, snapshot.Revision)
}

func TestStoreEntityMemoryRevisionBumpsOnUpdates(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	require.NoError(t, store.SaveEntity(context.Background(), "worker", "k1", types.MemorySnapshot{Goal: "one"}))
	snapshot, err := store.LoadEntity(context.Background(), "worker", "k1")
	require.NoError(t, err)
	snapshot.Goal = "two"
	require.NoError(t, store.SaveEntity(context.Background(), "worker", "k1", snapshot))
	snapshot, err = store.LoadEntity(context.Background(), "worker", "k1")
	require.NoError(t, err)
	assert.Equal(t, 2, snapshot.Revision)
	assert.Equal(t, "two", snapshot.Goal)
}

func TestStoreEntityMemoryRequiresIdentity(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	err := store.SaveEntity(context.Background(), "", "k1", types.MemorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")

	err = store.SaveEntity(context.Background(), "worker", "", types.MemorySnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key")
}

func TestStoreEntityMemoryBounded(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{EntityMaxChars: 300})

	err := store.SaveEntity(context.Background(), "worker", "k1", types.MemorySnapshot{
		Goal:      strings.Repeat("goal ", 100),
		Confirmed: []string{strings.Repeat("fact ", 100)},
	})
	require.NoError(t, err)
	data, readErr := files.Read(".agents/data/agents/worker/k1/memory/memory.md")
	require.NoError(t, readErr)
	assert.LessOrEqual(t, len(data), 300)
}
