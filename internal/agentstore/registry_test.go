package agentstore

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestRegistry_EnsureEntityCreatesAndPersists(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	entity, err := registry.EnsureEntity(context.Background(), "code-fixer", "role-a")
	require.NoError(t, err)
	require.NotNil(t, entity)
	assert.Equal(t, "code-fixer", entity.Agent)
	assert.Equal(t, "role-a", entity.Key)
	assert.False(t, entity.CreatedAt.IsZero())
	assert.False(t, entity.LastUsedAt.IsZero())

	data, err := files.Read(".agents/data/agents/code-fixer/role-a/entity.json")
	require.NoError(t, err)
	var stored Entity
	require.NoError(t, json.Unmarshal(data, &stored))
	assert.Equal(t, "role-a", stored.Key)
	assert.Equal(t, "code-fixer", stored.Agent)
}

func TestRegistry_EnsureEntityReusesExisting(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	first, err := registry.EnsureEntity(context.Background(), "writer", "k1")
	require.NoError(t, err)

	second, err := registry.EnsureEntity(context.Background(), "writer", "k1")
	require.NoError(t, err)
	assert.Equal(t, first.Key, second.Key)
	assert.False(t, second.LastUsedAt.Before(first.LastUsedAt))

	entities, err := registry.List(context.Background(), "writer")
	require.NoError(t, err)
	assert.Len(t, entities, 1)
}

func TestRegistry_EnsureEntityRejectsWhenMaxReached(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files, WithMaxEntities(2))

	_, err := registry.EnsureEntity(context.Background(), "reviewer", "a")
	require.NoError(t, err)
	_, err = registry.EnsureEntity(context.Background(), "reviewer", "b")
	require.NoError(t, err)

	_, err = registry.EnsureEntity(context.Background(), "reviewer", "c")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_entities")
	assert.Contains(t, err.Error(), "2")

	// Reuse of an existing entity is still allowed at the limit.
	_, err = registry.EnsureEntity(context.Background(), "reviewer", "a")
	require.NoError(t, err)
}

func TestRegistry_EnsureEntitySanitizesKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"keeps allowed characters", "Role_1.v-2", "Role_1.v-2"},
		{"replaces spaces", "a b", "a_b"},
		{"replaces slashes and colons", "a/b:c", "a_b_c"},
		{"replaces unicode", "角色", "__"},
		{"truncates long keys", strings.Repeat("x", 200), ""},
	}
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entity, err := registry.EnsureEntity(context.Background(), "writer", tt.in)
			require.NoError(t, err)
			if tt.want != "" {
				assert.Equal(t, tt.want, entity.Key)
			} else {
				assert.LessOrEqual(t, len(entity.Key), maxKeyLength)
				assert.True(t, strings.HasPrefix(entity.Key, strings.Repeat("x", 100)))
			}
			assert.NotContains(t, entity.Key, "/")
		})
	}
}

func TestRegistry_EnsureEntityGeneratesKeyWhenEmpty(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	first, err := registry.EnsureEntity(context.Background(), "worker", "")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(first.Key, "e-"))
	assert.NotEmpty(t, strings.TrimPrefix(first.Key, "e-"))

	second, err := registry.EnsureEntity(context.Background(), "worker", "")
	require.NoError(t, err)
	assert.NotEqual(t, first.Key, second.Key)
}

func TestRegistry_Get(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	_, err := registry.Get(context.Background(), "writer", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	_, err = registry.EnsureEntity(context.Background(), "writer", "k1")
	require.NoError(t, err)
	entity, err := registry.Get(context.Background(), "writer", "k1")
	require.NoError(t, err)
	assert.Equal(t, "k1", entity.Key)
}

func TestRegistry_ListSkipsUnreadableEntries(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	_, err := registry.EnsureEntity(context.Background(), "writer", "a")
	require.NoError(t, err)
	require.NoError(t, files.Write(".agents/data/agents/writer/broken/entity.json", []byte("{invalid")))

	entities, err := registry.List(context.Background(), "writer")
	require.NoError(t, err)
	require.Len(t, entities, 1)
	assert.Equal(t, "a", entities[0].Key)
}

func TestRegistry_ListEmptyTemplate(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	entities, err := registry.List(context.Background(), "nobody")
	require.NoError(t, err)
	assert.Empty(t, entities)
}
