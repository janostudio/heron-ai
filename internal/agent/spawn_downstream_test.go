package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// fakeChildInserter captures InsertSpawnedCall requests instead of joining a
// Team DAG.
type fakeChildInserter struct {
	inserts  []fakeInsert
	failWith string
}

type fakeInsert struct {
	parentCallID string
	spec         agentstore.SpawnedCallSpec
}

func (f *fakeChildInserter) InsertSpawnedCall(ctx context.Context, parentCallID string, spec agentstore.SpawnedCallSpec) error {
	if f.failWith != "" {
		return errString(f.failWith)
	}
	f.inserts = append(f.inserts, fakeInsert{parentCallID: parentCallID, spec: spec})
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }

func TestSpawnTool_AsyncDownstreamInsertsSyntheticCalls(t *testing.T) {
	fixture := newSpawnFixture(t)
	inserter := &fakeChildInserter{}
	ctx := agentstore.WithChildInserter(fixture.ctx(), inserter)

	result, err := fixture.spawn.Execute(ctx, map[string]any{
		"item":    map[string]any{"file": "a.go"},
		"key":     "k1",
		"wait":    false,
		"deliver": "downstream",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Success)
	assert.Empty(t, result.Error)
	assert.Equal(t, "downstream", result.Metadata["deliver"])
	assert.Equal(t, false, result.Metadata["wait"])
	assert.Equal(t, 1, result.Metadata["children"])

	// The handle identifies the child call that will run in the Team DAG.
	var handles []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &handles))
	require.Len(t, handles, 1)
	assert.Equal(t, "k1", handles[0]["key"])
	assert.Equal(t, "call-1/k1", handles[0]["call_id"])

	// The child is registered under the parent call with its full spawn
	// identity; the tool does NOT execute it inline.
	require.Len(t, inserter.inserts, 1)
	insert := inserter.inserts[0]
	assert.Equal(t, "call-1", insert.parentCallID)
	assert.Equal(t, "parent-agent", insert.spec.AgentID)
	assert.Equal(t, "k1", insert.spec.Key)
	assert.Equal(t, map[string]any{"file": "a.go"}, insert.spec.Item)
	assert.Equal(t, 1, insert.spec.Depth)
	assert.Empty(t, runnerCallsTo(fixture.runner, "call-1/k1"))
}

func runnerCallsTo(runner *fakeSpawnRunner, callID string) int {
	count := 0
	for _, call := range runner.recorded() {
		if call.req.CallID == callID {
			count++
		}
	}
	return count
}

func TestSpawnTool_AsyncDownstreamItemsOneChildPerItem(t *testing.T) {
	fixture := newSpawnFixture(t)
	inserter := &fakeChildInserter{}
	ctx := agentstore.WithChildInserter(fixture.ctx(), inserter)

	result, err := fixture.spawn.Execute(ctx, map[string]any{
		"items":   []any{"one", "two", "three"},
		"wait":    false,
		"deliver": "downstream",
	})
	require.NoError(t, err)
	assert.True(t, result.Success)

	var handles []map[string]any
	require.NoError(t, json.Unmarshal([]byte(result.Content), &handles))
	require.Len(t, handles, 3)
	keys := map[string]bool{}
	callIDs := map[string]bool{}
	for _, handle := range handles {
		keys[handle["key"].(string)] = true
		callIDs[handle["call_id"].(string)] = true
	}
	assert.Len(t, keys, 3)
	assert.Len(t, callIDs, 3)
	for key := range keys {
		assert.Contains(t, callIDs, "call-1/"+key)
	}

	require.Len(t, inserter.inserts, 3)
	items := []any{}
	for _, insert := range inserter.inserts {
		items = append(items, insert.spec.Item)
	}
	assert.ElementsMatch(t, []any{"one", "two", "three"}, items)
	assert.Empty(t, fixture.runner.recorded())
}

func TestSpawnTool_AsyncDownstreamPropagatesInsertError(t *testing.T) {
	fixture := newSpawnFixture(t)
	inserter := &fakeChildInserter{failWith: "parent call is not a scheduled call"}
	ctx := agentstore.WithChildInserter(fixture.ctx(), inserter)

	result, err := fixture.spawn.Execute(ctx, map[string]any{
		"item": "a", "key": "k1", "wait": false, "deliver": "downstream",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not a scheduled call")
}

func TestSpawnTool_AsyncParentChildCannotSpawnDownstream(t *testing.T) {
	// Batch B leftover #4: a durable SpawnChild task (wait=false +
	// deliver=parent) executes detached from the Team Run; a downstream spawn
	// attempted inside it must fail with the no-channel error.
	fixture := newAsyncSpawnFixture(t)
	nestedErrors := make(chan string, 1)
	fixture.runner.result = func(call spawnRunnerCall) (*types.AgentResult, error) {
		// TurnLoop.Run attaches the spawn identity to the execution context;
		// mimic that before invoking the nested Spawn so the test reaches the
		// insertion-channel check instead of the identity check.
		turnCtx := withSpawnIdentity(call.ctx, types.AgentConfig{Name: "parent-agent"}, call.req)
		result, execErr := fixture.spawn.Execute(turnCtx, map[string]any{
			"item": "grandchild", "key": "g1", "wait": false, "deliver": "downstream",
		})
		require.NoError(t, execErr)
		nestedErrors <- result.Error
		return &types.AgentResult{Status: types.TurnCompleted, Reply: "nested attempt done"}, nil
	}

	result, err := fixture.spawn.Execute(fixture.ctx(), map[string]any{
		"item": "child", "key": "k1", "wait": false,
	})
	require.NoError(t, err)
	assert.True(t, result.Success)
	handles := fixture.spawnHandles(t, result)
	require.Len(t, handles, 1)
	task := fixture.waitForTask(t, handles[0]["task_id"].(string))
	assert.Equal(t, types.ToolTaskCompleted, task.Status)

	select {
	case nestedErr := <-nestedErrors:
		assert.Contains(t, nestedErr, "scheduled by a Team")
	case <-time.After(5 * time.Second):
		t.Fatal("nested spawn result was not produced")
	}
}
