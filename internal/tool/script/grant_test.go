package script

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// A tool this Flow's permissions deny refuses with a sentence that names the
// reason. Left unbound, goja would report "not a function", which reads like a
// missing feature and sends the model into variations instead of a different plan.
func TestBlockedToolExplainsThePosture(t *testing.T) {
	caller := newFakeCaller()
	caller.results["Read"] = &types.ToolResult{Success: true, Content: "body"}
	runner := newRunner(caller)

	ctx := WithBlocked(context.Background(), []string{"Bash"})
	_, err := runner.Run(ctx, RunRequest{
		ScriptID:  "blocked",
		Code:      `tools.Bash({command: "ls"});`,
		ToolNames: []string{"Read"},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not available to this program")
	assert.Contains(t, err.Error(), "runtime's permissions")
	assert.Equal(t, 0, caller.callCount(), "a blocked tool must not reach the executor")
}

// tools.call takes the name as data, so it walks past the bound properties — which
// is why the caller-side gate, not the binding, is where the posture is enforced.
func TestGenericEntryPointsReachToolsThatAreNotBound(t *testing.T) {
	caller := newFakeCaller()
	caller.results["Write"] = &types.ToolResult{Success: true, Content: "written"}
	runner := newRunner(caller)

	ctx := WithBlocked(context.Background(), []string{"Write"})
	_, err := runner.Run(ctx, RunRequest{
		ScriptID: "generic-entry",
		Code:     `tools.call("Write", {file: "a", content: "b"}); return 1;`,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, caller.callCount(), "the gate is what refuses this call, not the binding")
}

// The grant records the capability set an approval covered and how often each one
// was used, which is what makes "one question, N side effects" auditable.
func TestGrantCoversOnlyWhatWasApproved(t *testing.T) {
	grant := NewGrant([]string{"Write", "Bash"}, "flow-permissions")
	require.NotNil(t, grant)

	assert.True(t, grant.Covers("Write"))
	assert.True(t, grant.Covers("Bash"))
	assert.False(t, grant.Covers("Read"), "read-only tools never need an approval")
	assert.Equal(t, []string{"Bash", "Write"}, grant.Covered())
	assert.Equal(t, "flow-permissions", grant.Source())

	grant.Record("Write")
	grant.Record("Write")
	assert.Equal(t, map[string]int{"Write": 2}, grant.Usage())
}

// An empty approval and no approval mean the same thing downstream, so both are a
// nil grant: otherwise "covered nothing" would be indistinguishable from "answered".
func TestNewGrantEmptyIsNil(t *testing.T) {
	assert.Nil(t, NewGrant(nil, "x"))
	assert.Nil(t, NewGrant([]string{"", ""}, "x"))
	assert.False(t, (*Grant)(nil).Covers("Write"))
	assert.Empty(t, (*Grant)(nil).Usage())

	ctx := WithGrant(context.Background(), nil)
	assert.Nil(t, GrantFromContext(ctx))
}
