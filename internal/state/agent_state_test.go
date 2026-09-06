package state

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestStoreAgentStateRoundTrip(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	loaded, err := store.LoadAgentState(context.Background(), "code-fixer")
	require.NoError(t, err)
	assert.Equal(t, types.StateScopeAgent, loaded.Scope)
	assert.Equal(t, 0, loaded.Revision)

	err = store.SaveAgentState(context.Background(), "code-fixer", types.StateSnapshot{
		Goal:      "fix the assigned file",
		Confirmed: []types.StateItem{{ID: "i-1", Text: "first outcome"}},
	})
	require.NoError(t, err)

	data, err := files.Read(".agents/data/agents/code-fixer/state/state.md")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(string(data), "---\n"))

	reloaded, err := store.LoadAgentState(context.Background(), "code-fixer")
	require.NoError(t, err)
	assert.Equal(t, types.StateScopeAgent, reloaded.Scope)
	assert.Equal(t, "fix the assigned file", reloaded.Goal)
	assert.Equal(t, []types.StateItem{{ID: "i-1", Text: "first outcome"}}, reloaded.Confirmed)
	assert.Equal(t, 1, reloaded.Revision)
}

func TestStoreAgentStateIsolatedPerAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	require.NoError(t, store.SaveAgentState(context.Background(), "writer", types.StateSnapshot{Goal: "goal-a"}))
	require.NoError(t, store.SaveAgentState(context.Background(), "reviewer", types.StateSnapshot{Goal: "goal-other"}))

	snapshot, err := store.LoadAgentState(context.Background(), "writer")
	require.NoError(t, err)
	assert.Equal(t, "goal-a", snapshot.Goal)

	snapshot, err = store.LoadAgentState(context.Background(), "reviewer")
	require.NoError(t, err)
	assert.Equal(t, "goal-other", snapshot.Goal)
}

func TestStoreAgentStatePersistsAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	first := NewStore(storage.NewFileStore(dir), Limits{})
	require.NoError(t, first.SaveAgentState(context.Background(), "worker", types.StateSnapshot{
		Decisions: []types.StateItem{{ID: "i-1", Text: "use option B"}},
	}))

	second := NewStore(storage.NewFileStore(dir), Limits{})
	snapshot, err := second.LoadAgentState(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, []types.StateItem{{ID: "i-1", Text: "use option B"}}, snapshot.Decisions)
	assert.Equal(t, 1, snapshot.Revision)
}

func TestStoreAgentStateRevisionBumpsOnUpdates(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	require.NoError(t, store.SaveAgentState(context.Background(), "worker", types.StateSnapshot{Goal: "one"}))
	snapshot, err := store.LoadAgentState(context.Background(), "worker")
	require.NoError(t, err)
	snapshot.Goal = "two"
	require.NoError(t, store.SaveAgentState(context.Background(), "worker", snapshot))
	snapshot, err = store.LoadAgentState(context.Background(), "worker")
	require.NoError(t, err)
	assert.Equal(t, 2, snapshot.Revision)
	assert.Equal(t, "two", snapshot.Goal)
}

func TestStoreAgentStateRequiresAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})

	err := store.SaveAgentState(context.Background(), "", types.StateSnapshot{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}

func TestStoreAgentStateBounded(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{AgentStateMaxChars: 300})

	err := store.SaveAgentState(context.Background(), "worker", types.StateSnapshot{
		Goal:      strings.Repeat("goal ", 100),
		Confirmed: []types.StateItem{{ID: "i-1", Text: strings.Repeat("fact ", 100)}},
	})
	require.NoError(t, err)
	data, readErr := files.Read(".agents/data/agents/worker/state/state.md")
	require.NoError(t, readErr)
	assert.LessOrEqual(t, len(data), 300)
}
