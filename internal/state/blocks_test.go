package state

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestBlockRoundTrip(t *testing.T) {
	now := time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)

	goal, err := decodeBlock(types.StateBlockGoal, mustEncode(t, types.StateBlock{
		Name: types.StateBlockGoal, Revision: 3, UpdatedAt: now, Goal: "ship the fix",
	}))
	require.NoError(t, err)
	assert.Equal(t, "ship the fix", goal.Goal)
	assert.Equal(t, 3, goal.Revision)

	steps, err := decodeBlock(types.StateBlockNextSteps, mustEncode(t, types.StateBlock{
		Name: types.StateBlockNextSteps, Revision: 4, UpdatedAt: now,
		Items: []types.StateItem{
			{ID: "i-1", Text: "write the test", Status: types.StateStatusInProgress, AddedBy: "a(e-1)"},
			{ID: "i-2", Text: "run it", Status: types.StateStatusPending},
		},
	}))
	require.NoError(t, err)
	require.Len(t, steps.Items, 2)
	assert.Equal(t, "write the test", steps.Items[0].Text)
	assert.Equal(t, types.StateStatusInProgress, steps.Items[0].Status)
	assert.Equal(t, "a(e-1)", steps.Items[0].AddedBy)
	assert.Equal(t, types.StateStatusPending, steps.Items[1].Status)

	workspace, err := decodeBlock(types.StateBlockWorkspace, mustEncode(t, types.StateBlock{
		Name: types.StateBlockWorkspace, Workspace: []types.StateWorkspaceRef{
			{Path: "internal/state/store.go", Revision: "abc123"},
		},
	}))
	require.NoError(t, err)
	require.Len(t, workspace.Workspace, 1)
	assert.Equal(t, "internal/state/store.go", workspace.Workspace[0].Path)
	assert.Equal(t, "abc123", workspace.Workspace[0].Revision)
}

func TestDecodeItemLineWithoutAttributes(t *testing.T) {
	// A hand-edited file (or a pre-status one) must still parse.
	item := parseItemLine("i-7: 已核实主分支是 master")
	assert.Equal(t, "i-7", item.ID)
	assert.Equal(t, "已核实主分支是 master", item.Text)
	assert.Empty(t, item.Status)
}

func TestIndexRoundTrip(t *testing.T) {
	index := types.StateIndex{
		Revision:  7,
		UpdatedAt: time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC),
		Goal:      "g",
		Blocks: []types.StateBlockMeta{
			{Name: types.StateBlockNextSteps, Entries: 2, InProgress: 1, Revision: 7, UpdatedAt: time.Date(2026, 10, 29, 7, 0, 0, 0, time.UTC)},
			{Name: types.StateBlockConfirmed, Entries: 0, Revision: 0},
		},
	}
	decoded, err := decodeIndex(encodeIndex(index))
	require.NoError(t, err)
	assert.Equal(t, 7, decoded.Revision)
	assert.Equal(t, "g", decoded.Goal)
	require.Len(t, decoded.Blocks, 2)
	assert.Equal(t, 2, decoded.Blocks[0].Entries)
	assert.Equal(t, 1, decoded.Blocks[0].InProgress)
	assert.True(t, decoded.Blocks[1].UpdatedAt.IsZero())
}

// A block-scoped write must leave the other blocks untouched: that is the whole
// reason the state is a directory.
func TestSaveBlockTouchesOnlyOneFile(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})
	ctx := context.Background()

	require.NoError(t, store.SetGoal(ctx, "worker", "task one"))
	before, err := files.Read(agentBlockPath("worker", types.StateBlockGoal))
	require.NoError(t, err)

	require.NoError(t, store.Add(ctx, "worker", FieldNextSteps, "step", types.StateStatusPending, "worker"))
	after, err := files.Read(agentBlockPath("worker", types.StateBlockGoal))
	require.NoError(t, err)
	assert.Equal(t, string(before), string(after), "adding a next step must not rewrite the goal block")

	index, err := store.LoadIndex(ctx, "worker")
	require.NoError(t, err)
	assert.Equal(t, "task one", index.Goal)
	meta := map[string]types.StateBlockMeta{}
	for _, block := range index.Blocks {
		meta[block.Name] = block
	}
	assert.Equal(t, 1, meta[types.StateBlockNextSteps].Entries)
	assert.Equal(t, 1, meta[types.StateBlockGoal].Entries)
}

// The pre-directory file is split on first access and kept as a backup.
func TestMigrateLegacyStateFile(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := NewStore(files, Limits{})
	ctx := context.Background()

	legacy := types.StateSnapshot{
		Goal:      "old task",
		Revision:  5,
		UpdatedAt: time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC),
		Confirmed: []types.StateItem{{ID: "i-1", Text: "old fact"}},
		NextSteps: []types.StateItem{{ID: "i-2", Text: "old step"}},
	}
	data, err := encode(legacy)
	require.NoError(t, err)
	require.NoError(t, files.Write(".agents/data/agents/legacy-agent/state/state.md", data))

	index, err := store.LoadIndex(ctx, "legacy-agent")
	require.NoError(t, err)
	assert.Equal(t, 5, index.Revision)
	assert.Equal(t, "old task", index.Goal)

	goal, err := store.LoadBlock(ctx, "legacy-agent", types.StateBlockGoal)
	require.NoError(t, err)
	assert.Equal(t, "old task", goal.Goal)

	steps, err := store.LoadBlock(ctx, "legacy-agent", types.StateBlockNextSteps)
	require.NoError(t, err)
	require.Len(t, steps.Items, 1)
	assert.Equal(t, "old step", steps.Items[0].Text)

	assert.True(t, files.Exists(".agents/data/agents/legacy-agent/state/state.md.bak"),
		"the old file must be kept as a backup")
	assert.False(t, files.Exists(".agents/data/agents/legacy-agent/state/state.md"),
		"the old file is retired after a successful migration")
}

// Block-level locking: holding one block must not block another.
func TestDifferentBlocksDoNotContend(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})

	unlock, ok := store.locks.TryLock("agent-a/" + types.StateBlockConfirmed)
	require.True(t, ok)
	defer unlock()

	unlock2, ok2 := store.locks.TryLock("agent-a/" + types.StateBlockNextSteps)
	require.True(t, ok2, "a different block must be editable while one block is busy")
	unlock2()

	_, busy := store.locks.TryLock("agent-a/" + types.StateBlockConfirmed)
	assert.False(t, busy, "the same block must stay exclusive")
}

func TestNextStepsAllowOnlyOneInProgress(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	require.NoError(t, store.Add(ctx, "worker", FieldNextSteps, "first", types.StateStatusInProgress, "worker"))
	err := store.Add(ctx, "worker", FieldNextSteps, "second", types.StateStatusInProgress, "worker")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "in_progress")

	items, err := store.List(ctx, "worker", FieldNextSteps)
	require.NoError(t, err)
	require.Len(t, items, 1)

	// Finishing the first one frees the slot.
	require.NoError(t, store.Update(ctx, "worker", FieldNextSteps, items[0].ID, "", types.StateStatusCompleted, "worker"))
	require.NoError(t, store.Add(ctx, "worker", FieldNextSteps, "second", types.StateStatusInProgress, "worker"))

	items, err = store.List(ctx, "worker", FieldNextSteps)
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, types.StateStatusCompleted, items[0].Status)
	assert.Equal(t, types.StateStatusInProgress, items[1].Status)
}

func TestUpdateKeepsTextAndStatusWhenOmitted(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	require.NoError(t, store.Add(ctx, "worker", FieldNextSteps, "step", types.StateStatusPending, "worker"))
	items, err := store.List(ctx, "worker", FieldNextSteps)
	require.NoError(t, err)
	id := items[0].ID

	// Status-only update.
	require.NoError(t, store.Update(ctx, "worker", FieldNextSteps, id, "", types.StateStatusInProgress, "worker"))
	// Text-only update.
	require.NoError(t, store.Update(ctx, "worker", FieldNextSteps, id, "renamed", "", "worker"))

	items, err = store.List(ctx, "worker", FieldNextSteps)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, "renamed", items[0].Text)
	assert.Equal(t, types.StateStatusInProgress, items[0].Status)
}

func TestStatusOnlyAppliesToNextSteps(t *testing.T) {
	store := NewStore(storage.NewFileStore(t.TempDir()), Limits{})
	ctx := context.Background()

	// A status on a non-todo field is rejected instead of silently stored.
	err := store.Update(ctx, "worker", FieldConfirmed, "missing", "", types.StateStatusCompleted, "worker")
	require.Error(t, err)

	// add() ignores it: a completed fact has no meaning.
	require.NoError(t, store.Add(ctx, "worker", FieldConfirmed, "fact", types.StateStatusCompleted, "worker"))
	items, err := store.List(ctx, "worker", FieldConfirmed)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Empty(t, items[0].Status)
}

func TestReduceItemsKeepsUnfinishedFirst(t *testing.T) {
	items := []types.StateItem{
		{ID: "i-1", Text: "done", Status: types.StateStatusCompleted},
		{ID: "i-2", Text: "todo", Status: types.StateStatusPending},
		{ID: "i-3", Text: "done too", Status: types.StateStatusCompleted},
		{ID: "i-4", Text: "doing", Status: types.StateStatusInProgress},
	}
	reduced := reduceItems(items, 2)
	require.Len(t, reduced, 2)
	assert.Equal(t, "todo", reduced[0].Text)
	assert.Equal(t, "doing", reduced[1].Text)
}

func TestEmptyBlockDecode(t *testing.T) {
	block, err := decodeBlock(types.StateBlockNextSteps, nil)
	require.NoError(t, err)
	assert.Empty(t, block.Items)
	assert.Equal(t, types.StateBlockNextSteps, block.Name)
}

func mustEncode(t *testing.T, block types.StateBlock) []byte {
	t.Helper()
	data, err := encodeBlock(block)
	require.NoError(t, err)
	return data
}

func TestBlockFileNamesAreStable(t *testing.T) {
	// The pointer tells the model the directory; the file names are the
	// contract between the tool and the files on disk.
	assert.Equal(t, ".agents/data/agents/a/state", agentStateDir("a"))
	assert.True(t, strings.HasSuffix(agentBlockPath("a", types.StateBlockNextSteps), "/state/next_steps.md"))
	assert.True(t, strings.HasSuffix(agentIndexPath("a"), "/state/index.md"))
}
