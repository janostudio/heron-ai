package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

func TestAddListGetUpdateRemove(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	require.NoError(t, store.Add(ctx, "agent-a", FieldConfirmed, "first todo"))
	require.NoError(t, store.Add(ctx, "agent-a", FieldConfirmed, "second todo"))

	items, err := store.List(ctx, "agent-a", FieldConfirmed)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "first todo", items[0].Text)
	assert.NotEmpty(t, items[0].ID)
	assert.NotEqual(t, items[0].ID, items[1].ID)

	got, err := store.Get(ctx, "agent-a", FieldConfirmed, items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "first todo", got.Text)

	require.NoError(t, store.Update(ctx, "agent-a", FieldConfirmed, items[0].ID, "updated todo"))
	got, err = store.Get(ctx, "agent-a", FieldConfirmed, items[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "updated todo", got.Text)

	require.NoError(t, store.Remove(ctx, "agent-a", FieldConfirmed, items[0].ID))
	items, err = store.List(ctx, "agent-a", FieldConfirmed)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "second todo", items[0].Text)
}

func TestSetClearGoal(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	require.NoError(t, store.SetGoal(ctx, "agent-a", "ship the feature"))
	snapshot, err := store.LoadEntity(ctx, "agent-a")
	require.NoError(t, err)
	assert.Equal(t, "ship the feature", snapshot.Goal)

	require.NoError(t, store.ClearGoal(ctx, "agent-a"))
	snapshot, err = store.LoadEntity(ctx, "agent-a")
	require.NoError(t, err)
	assert.Empty(t, snapshot.Goal)
}

func TestCRUDRejectsUnsupportedField(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	err := store.Add(ctx, "agent-a", StateField("bogus"), "x")
	require.Error(t, err)

	err = store.Add(ctx, "agent-a", FieldGoal, "x")
	require.Error(t, err)

	err = store.Add(ctx, "agent-a", FieldWorkspace, "x")
	require.Error(t, err)
}

func TestRemoveMissingReturnsError(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	err := store.Remove(ctx, "agent-a", FieldConfirmed, "does-not-exist")
	require.Error(t, err)
}

func TestRecordIDsAddRemoveList(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	require.NoError(t, store.Add(ctx, "agent-a", FieldRecordIDs, "rec-1"))
	require.NoError(t, store.Add(ctx, "agent-a", FieldRecordIDs, "rec-2"))

	items, err := store.List(ctx, "agent-a", FieldRecordIDs)
	require.NoError(t, err)
	require.Len(t, items, 2)

	require.NoError(t, store.Remove(ctx, "agent-a", FieldRecordIDs, "rec-1"))
	items, err = store.List(ctx, "agent-a", FieldRecordIDs)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "rec-2", items[0].ID)
}
