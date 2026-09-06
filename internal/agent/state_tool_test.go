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
	var items []types.StateItem
	require.NoError(t, json.Unmarshal([]byte(res.Content), &items))
	require.Len(t, items, 1)
	assert.Equal(t, "do the thing", items[0].Text)
	assert.NotEmpty(t, items[0].ID)

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
	require.NoError(t, json.Unmarshal([]byte(res.Content), &items))
	assert.Empty(t, items)
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
	var items []types.StateItem
	require.NoError(t, json.Unmarshal([]byte(res.Content), &items))
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
	var items []types.StateItem
	require.NoError(t, json.Unmarshal([]byte(res.Content), &items))
	assert.Empty(t, items, "agent-b must not see agent-a's state")
}
