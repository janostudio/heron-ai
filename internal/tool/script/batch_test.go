package script

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestToolsAllReturnsResultsInOrder(t *testing.T) {
	caller := newFakeCaller()
	caller.specs["Read"] = types.ToolExecutionSpec{Class: types.ToolReadOnly}
	caller.results["Read"] = &types.ToolResult{Success: true, Content: "body"}
	r := newRunner(caller)

	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID: "b1",
		Code: `const rs = tools.all([{name:"Read", args:{file:"a"}}, {name:"Read", args:{file:"b"}}]);
		       return rs.map(r => r.ok).join(",");`,
		ToolNames: []string{"Read"},
	})
	require.NoError(t, err)
	assert.Equal(t, `true,true`, ex.Value)
	assert.Equal(t, 2, caller.callCount())
	assert.Len(t, ex.Steps, 2)
}

// Read-only members must overlap: that is the whole reason a batch exists.
func TestToolsAllRunsReadOnlyConcurrently(t *testing.T) {
	caller := newFakeCaller()
	caller.delay = 80 * time.Millisecond
	caller.specs["Read"] = types.ToolExecutionSpec{Class: types.ToolReadOnly}
	r := newRunner(caller)

	start := time.Now()
	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID: "b2",
		Code: `const rs = tools.all([
		         {name:"Read", args:{file:"a"}},
		         {name:"Read", args:{file:"b"}},
		         {name:"Read", args:{file:"c"}}]);
		       return rs.length;`,
		ToolNames: []string{"Read"},
	})
	require.NoError(t, err)
	assert.Equal(t, "3", ex.Value)
	assert.Less(t, time.Since(start), 200*time.Millisecond,
		"three 80ms reads should overlap, not serialize")
}

// Effectful members are never run concurrently, whatever their position.
func TestToolsAllSerializesEffectfulCalls(t *testing.T) {
	caller := newFakeCaller()
	caller.delay = 40 * time.Millisecond
	caller.specs["Write"] = types.ToolExecutionSpec{Class: types.ToolSerial}
	r := newRunner(caller)

	start := time.Now()
	_, err := r.Run(context.Background(), RunRequest{
		ScriptID: "b3",
		Code: `const rs = tools.all([
		         {name:"Write", args:{file:"a"}},
		         {name:"Write", args:{file:"b"}}]);
		       return rs.length;`,
		ToolNames: []string{"Write"},
	})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 80*time.Millisecond,
		"writes must not overlap")
}

// A batch is recorded step by step, so a replay replaces the whole batch with
// recorded results and touches no tool.
func TestToolsAllBatchIsReplayable(t *testing.T) {
	caller := newFakeCaller()
	caller.specs["Read"] = types.ToolExecutionSpec{Class: types.ToolReadOnly}
	store := newMemStore()
	r := &Runner{Caller: caller, Store: store}

	req := RunRequest{
		ScriptID: "b4",
		Code: `const rs = tools.all([{name:"Read", args:{file:"a"}}, {name:"Read", args:{file:"b"}}]);
		       return rs.length;`,
		ToolNames: []string{"Read"},
	}
	_, err := r.Run(context.Background(), req)
	require.NoError(t, err)
	require.Equal(t, 2, caller.callCount())

	ex, err := r.Run(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, 2, caller.callCount(), "replay must not execute the batch again")
	assert.Empty(t, ex.Steps)
}

func TestToolsAllRejectsBadEntries(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"not an array", `return tools.all("nope");`},
		{"missing name", `return tools.all([{args:{}}]);`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(newFakeCaller())
			_, err := r.Run(context.Background(), RunRequest{ScriptID: "b5", Code: tc.code})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "tools.all")
		})
	}
}

// A failing member must not abort the batch: the script decides what to do with
// a failed entry, and the others already ran.
func TestToolsAllReportsFailuresPerEntry(t *testing.T) {
	caller := newFakeCaller()
	caller.specs["Read"] = types.ToolExecutionSpec{Class: types.ToolReadOnly}
	caller.results["Read"] = &types.ToolResult{Success: false, Error: "missing"}
	r := newRunner(caller)

	ex, err := r.Run(context.Background(), RunRequest{
		ScriptID:  "b6",
		Code:      `const rs = tools.all([{name:"Read", args:{file:"a"}}]); return rs[0].ok + ":" + rs[0].error;`,
		ToolNames: []string{"Read"},
	})
	require.NoError(t, err)
	assert.Equal(t, "false:missing", ex.Value)
}
