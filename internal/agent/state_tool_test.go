package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func stateToolContext(agentID string) context.Context {
	return withSpawnIdentity(context.Background(), types.AgentConfig{Name: agentID}, types.AgentRequest{AgentID: agentID})
}

func stateToolInstanceContext(agentID, instanceKey string) context.Context {
	ctx := withSpawnIdentity(context.Background(), types.AgentConfig{Name: agentID}, types.AgentRequest{AgentID: agentID})
	return withSpawnInstanceKey(ctx, instanceKey)
}

func TestStateToolCRUD(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)
	ctx := stateToolContext("agent-a")

	// add
	res, err := tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "do the thing"})
	require.NoError(t, err)
	assert.True(t, res.Success)

	// list
	res, err = tool.Execute(ctx, map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	list := decodeStateList(t, res.Content)
	items := list.Items
	require.Len(t, items, 1)
	assert.Equal(t, "do the thing", items[0].Text)
	assert.NotEmpty(t, items[0].ID)
	assert.NotZero(t, list.Revision, "list must report the snapshot revision")
	assert.False(t, list.UpdatedAt.IsZero(), "list must report updated_at")

	// get
	res, err = tool.Execute(ctx, map[string]any{"action": "get", "field": "next_steps", "id": items[0].ID})
	require.NoError(t, err)
	var item types.StateItem
	require.NoError(t, json.Unmarshal([]byte(res.Content), &item))
	assert.Equal(t, "do the thing", item.Text)

	// update
	res, err = tool.Execute(ctx, map[string]any{"action": "update", "field": "next_steps", "id": items[0].ID, "text": "updated"})
	require.NoError(t, err)
	assert.True(t, res.Success)

	// remove
	res, err = tool.Execute(ctx, map[string]any{"action": "remove", "field": "next_steps", "id": items[0].ID})
	require.NoError(t, err)
	assert.True(t, res.Success)

	res, err = tool.Execute(ctx, map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	assert.Empty(t, decodeStateList(t, res.Content).Items)
}

func TestStateToolSetGoal(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)
	ctx := stateToolContext("agent-a")

	res, err := tool.Execute(ctx, map[string]any{"action": "add", "field": "goal", "text": "ship it"})
	require.NoError(t, err)
	assert.True(t, res.Success)

	res, err = tool.Execute(ctx, map[string]any{"action": "list", "field": "goal"})
	require.NoError(t, err)
	items := decodeStateList(t, res.Content).Items
	require.Len(t, items, 1)
	assert.Equal(t, "ship it", items[0].Text)
}

func TestStateToolRequiresAgentContext(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)

	_, err := tool.Execute(context.Background(), map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
}

func TestStateToolIsolatedPerAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)

	_, err := tool.Execute(stateToolContext("agent-a"), map[string]any{"action": "add", "field": "confirmed", "text": "a"})
	require.NoError(t, err)

	res, err := tool.Execute(stateToolContext("agent-b"), map[string]any{"action": "list", "field": "confirmed"})
	require.NoError(t, err)
	items := decodeStateList(t, res.Content).Items
	assert.Empty(t, items, "agent-b must not see agent-a's state")
}

func TestStateToolRecordsAttribution(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)

	// Two instances of the same agent share one whiteboard but attribute
	// their writes to distinct instance identifiers.
	_, err := tool.Execute(stateToolInstanceContext("role-actor", "e-1"), map[string]any{"action": "add", "field": "next_steps", "text": "review draft"})
	require.NoError(t, err)

	res, err := tool.Execute(stateToolInstanceContext("role-actor", "e-1"), map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	items := decodeStateList(t, res.Content).Items
	require.Len(t, items, 1)
	assert.Equal(t, "role-actor(e-1)", items[0].AddedBy)
	assert.Empty(t, items[0].HandledBy)

	// A different instance updates the same shared entry; HandledBy records
	// the second instance while AddedBy stays with the first.
	_, err = tool.Execute(stateToolInstanceContext("role-actor", "e-2"), map[string]any{"action": "update", "field": "next_steps", "id": items[0].ID, "text": "reviewed"})
	require.NoError(t, err)

	res, err = tool.Execute(stateToolInstanceContext("role-actor", "e-2"), map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	items = decodeStateList(t, res.Content).Items
	require.Len(t, items, 1)
	assert.Equal(t, "reviewed", items[0].Text)
	assert.Equal(t, "role-actor(e-1)", items[0].AddedBy)
	assert.Equal(t, "role-actor(e-2)", items[0].HandledBy)
}

func TestStateToolNoInstanceKeyFallsBackToBareAgent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)

	_, err := tool.Execute(stateToolContext("agent-a"), map[string]any{"action": "add", "field": "confirmed", "text": "bare"})
	require.NoError(t, err)

	res, err := tool.Execute(stateToolContext("agent-a"), map[string]any{"action": "list", "field": "confirmed"})
	require.NoError(t, err)
	items := decodeStateList(t, res.Content).Items
	require.Len(t, items, 1)
	assert.Equal(t, "agent-a", items[0].AddedBy)
}

// decodeStateList parses a State tool list result, which carries the snapshot
// metadata next to the entries.
func decodeStateList(t *testing.T, content string) types.StateList {
	t.Helper()
	var list types.StateList
	require.NoError(t, json.Unmarshal([]byte(content), &list))
	return list
}

// list without a field returns the index: blocks, entry counts, in_progress and
// freshness. It is the default read, because loading entries before knowing
// which block matters is how a stale block gets quoted.
func TestStateToolListReturnsIndex(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)
	ctx := stateToolContext("agent-a")

	_, err := tool.Execute(ctx, map[string]any{"action": "add", "field": "goal", "text": "ship it"})
	require.NoError(t, err)
	_, err = tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "write the test", "status": "in_progress"})
	require.NoError(t, err)
	_, err = tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "run it"})
	require.NoError(t, err)

	res, err := tool.Execute(ctx, map[string]any{"action": "list"})
	require.NoError(t, err)
	var index types.StateIndex
	require.NoError(t, json.Unmarshal([]byte(res.Content), &index))
	assert.Equal(t, "ship it", index.Goal)
	require.NotEmpty(t, index.Blocks)

	blocks := map[string]types.StateBlockMeta{}
	for _, block := range index.Blocks {
		blocks[block.Name] = block
	}
	assert.Equal(t, 1, blocks["goal"].Entries)
	assert.Equal(t, 2, blocks["next_steps"].Entries)
	assert.Equal(t, 1, blocks["next_steps"].InProgress)
	assert.False(t, blocks["next_steps"].UpdatedAt.IsZero())
}

// A second in_progress entry is rejected at the tool boundary: the model gets a
// reason, not a silent second "in progress".
func TestStateToolRejectsSecondInProgress(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)
	ctx := stateToolContext("agent-a")

	res, err := tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "first", "status": "in_progress"})
	require.NoError(t, err)
	require.True(t, res.Success)

	res, err = tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "second", "status": "in_progress"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "in_progress")
}

// Marking an entry completed is a status update, not a delete: "done" stays
// readable until the model decides to remove it.
func TestStateToolCompleteKeepsEntry(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	store := state.NewStore(files, state.Limits{})
	tool := NewStateTool(store)
	ctx := stateToolContext("agent-a")

	_, err := tool.Execute(ctx, map[string]any{"action": "add", "field": "next_steps", "text": "step"})
	require.NoError(t, err)
	listRes, err := tool.Execute(ctx, map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	list := decodeStateList(t, mustContent(t, listRes))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "pending", list.Items[0].Status)

	res, err := tool.Execute(ctx, map[string]any{"action": "update", "field": "next_steps", "id": list.Items[0].ID, "status": "completed"})
	require.NoError(t, err)
	require.True(t, res.Success)

	listRes, err = tool.Execute(ctx, map[string]any{"action": "list", "field": "next_steps"})
	require.NoError(t, err)
	list = decodeStateList(t, mustContent(t, listRes))
	require.Len(t, list.Items, 1)
	assert.Equal(t, "completed", list.Items[0].Status)
	assert.Equal(t, "step", list.Items[0].Text)
}

func mustContent(t *testing.T, res *types.ToolResult) string {
	t.Helper()
	require.True(t, res.Success, res.Error)
	return res.Content
}
