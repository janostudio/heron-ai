package agentstore

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestRegistry_NextInstanceKeyReturnsKey(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	instance, err := registry.NextInstanceKey(context.Background(), "code-fixer", "role-a")
	require.NoError(t, err)
	require.NotNil(t, instance)
	assert.Equal(t, "code-fixer", instance.Agent)
	assert.Equal(t, "role-a", instance.Key)

	// No entity.json is persisted anymore (design doc 26).
	assert.False(t, files.Exists(".agents/data/agents/code-fixer/role-a/entity.json"))
}

func TestRegistry_NextInstanceKeySanitizesKey(t *testing.T) {
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
			instance, err := registry.NextInstanceKey(context.Background(), "writer", tt.in)
			require.NoError(t, err)
			assert.Equal(t, tt.want, instance.Key)
			assert.NotContains(t, instance.Key, "/")
		})
	}
}

func TestRegistry_NextInstanceKeyGeneratesKeyWhenEmpty(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	first, err := registry.NextInstanceKey(context.Background(), "worker", "")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(first.Key, "e-"))

	second, err := registry.NextInstanceKey(context.Background(), "worker", "")
	require.NoError(t, err)
	assert.NotEqual(t, first.Key, second.Key)
}

func TestRegistry_NextInstanceKeyRequiresAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	registry := NewRegistry(files)

	_, err := registry.NextInstanceKey(context.Background(), "", "k1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}
