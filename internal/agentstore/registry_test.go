package agentstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestRegistry_EnsureEntityReturnsKey(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	entity, err := registry.EnsureEntity(context.Background(), "code-fixer", "role-a")
	require.NoError(t, err)
	require.NotNil(t, entity)
	assert.Equal(t, "code-fixer", entity.Agent)
	assert.Equal(t, "role-a", entity.Key)

	// No entity.json is persisted anymore (design doc 26).
	assert.False(t, files.Exists(".agents/data/agents/code-fixer/role-a/entity.json"))
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
	}
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entity, err := registry.EnsureEntity(context.Background(), "writer", tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, entity.Key)
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

	second, err := registry.EnsureEntity(context.Background(), "worker", "")
	require.NoError(t, err)
	assert.NotEqual(t, first.Key, second.Key)
}

func TestRegistry_EnsureEntityRequiresAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	_, err := registry.EnsureEntity(context.Background(), "", "k1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}
