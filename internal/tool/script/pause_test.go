package script

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// approvalCaller refuses one tool until it is told the step was approved.
//
// It mimics the gate: the refusal is an ErrApprovalRequired, which is the sandbox's
// signal to stop the program rather than report a tool failure.
type approvalCaller struct {
	approveTool string
	gatedStep   int
	calls       []string
	approved    bool
}

func (c *approvalCaller) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	c.calls = append(c.calls, name)
	if name == c.approveTool && !c.approved {
		return nil, &ErrApprovalRequired{Request: ApprovalRequest{
			Tool: name, Args: args, Reason: "permissions: bash=ask",
		}}
	}
	return &types.ToolResult{Success: true, Content: "ok:" + name}, nil
}

// The program stops at the gated step, reports it, and leaves a `paused` record —
// not a `started` one, because the call certainly did not run.
func TestRunPausesAtGatedStep(t *testing.T) {
	caller := &approvalCaller{approveTool: "Bash"}
	store := newMemStore()
	runner := &Runner{Caller: caller, Store: store}

	ex, err := runner.Run(context.Background(), RunRequest{
		ScriptID:  "p1",
		Code:      `tools.Read({file: "a"}); tools.Bash({command: "ls"}); return 1;`,
		ToolNames: []string{"Read", "Bash"},
	})
	require.NoError(t, err, "a pause is not a failure")
	require.NotNil(t, ex.Paused)
	assert.Equal(t, 1, ex.Paused.Step)
	assert.Equal(t, "Bash", ex.Paused.Tool)
	assert.Equal(t, "permissions: bash=ask", ex.Paused.Reason)
	assert.Equal(t, []string{"Read", "Bash"}, caller.calls, "the gated call must not be executed twice")

	records, err := store.Load(context.Background(), "p1")
	require.NoError(t, err)
	statuses := map[int]string{}
	for _, rec := range records {
		statuses[rec.Step] = rec.Status
	}
	assert.Equal(t, stepDone, statuses[0])
	assert.Equal(t, stepPaused, statuses[1], "paused, so a denial cannot look like a half-executed effect")
}

// Resuming replays the finished steps and executes only the approved one.
func TestRunResumesAfterApproval(t *testing.T) {
	// The caller is shared with the paused run: it remembers the approval.
	caller := &approvalCaller{approveTool: "Bash", gatedStep: 1}
	store := newMemStore()
	runner := &Runner{Caller: caller, Store: store}
	code := `const a = tools.Read({file: "a"}).content; tools.Bash({command: "ls"}); return a;`

	paused, err := runner.Run(context.Background(), RunRequest{
		ScriptID: "p2", Code: code, ToolNames: []string{"Read", "Bash"},
	})
	require.NoError(t, err)
	require.NotNil(t, paused.Paused)

	caller.approved = true
	caller.calls = nil
	step := paused.Paused.Step
	resumed, err := runner.Run(context.Background(), RunRequest{
		ScriptID:     "p2",
		Code:         code,
		ToolNames:    []string{"Read", "Bash"},
		ApprovedStep: &step,
	})
	require.NoError(t, err)
	require.Nil(t, resumed.Paused, "the approved step must run, not pause again")
	assert.Equal(t, []string{"Bash"}, caller.calls, "the finished Read is replayed, not re-executed")
	assert.Contains(t, resumed.Value, "ok:Read", "the program returns the replayed read result")

	// The log is append-only, so the paused entry stays as history; what matters is
	// that the step itself now ends in a completed record.
	latest := map[int]StepRecord{}
	records, err := store.Load(context.Background(), "p2")
	require.NoError(t, err)
	for _, rec := range records {
		latest[rec.Step] = rec
	}
	for step, rec := range latest {
		assert.NotEqual(t, stepPaused, rec.Status, "step %d must be completed after the approval", step)
	}
}

// A denial leaves no approval behind: re-running the same program pauses again
// rather than being refused as an interrupted effect.
func TestRunPausesAgainAfterDenial(t *testing.T) {
	caller := &approvalCaller{approveTool: "Write"}
	store := newMemStore()
	runner := &Runner{Caller: caller, Store: store}
	code := `tools.Write({file: "a", content: "x"}); return 1;`

	_, err := runner.Run(context.Background(), RunRequest{
		ScriptID: "p3", Code: code, ToolNames: []string{"Write"},
	})
	require.NoError(t, err)

	// No approval was given; the same program must be runnable again.
	second, err := runner.Run(context.Background(), RunRequest{
		ScriptID: "p3", Code: code, ToolNames: []string{"Write"},
	})
	require.NoError(t, err)
	require.NotNil(t, second.Paused, "a denial must not turn into an interrupted-effect refusal")
}

// An approval for one step does not excuse a different interrupted effect.
func TestRunApprovalDoesNotExcuseOtherInterruptedEffects(t *testing.T) {
	store := newMemStore()
	require.NoError(t, store.Append(context.Background(), "p4", StepRecord{
		Step: 0, ToolName: "Write", ArgsHash: "x", Status: stepStarted,
	}))
	runner := &Runner{Caller: &approvalCaller{approveTool: "Bash"}, Store: store}

	step := 1
	_, err := runner.Run(context.Background(), RunRequest{ScriptID: "p4", Code: `return 1;`, ApprovedStep: &step})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrEffectInterrupted))
}
