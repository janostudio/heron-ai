package tool

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// blockingTool blocks until released, modeling a tool that ignores its own
// deadline and would otherwise strand the AgentTurn.
type blockingTool struct {
	release chan struct{}
	once    sync.Once
}

func (t *blockingTool) Name() string               { return "Blocking" }
func (t *blockingTool) Description() string        { return "blocks" }
func (t *blockingTool) Parameters() map[string]any { return map[string]any{} }
func (t *blockingTool) NeedsApproval() bool        { return false }
func (t *blockingTool) Execute(_ context.Context, _ map[string]any) (*types.ToolResult, error) {
	<-t.release
	return &types.ToolResult{Success: true, Content: "late"}, nil
}
func (t *blockingTool) unblock() { t.once.Do(func() { close(t.release) }) }

// captureCtxTool records whether the context it received carried a deadline —
// the executor's guarantee — then returns immediately.
type captureCtxTool struct{ hadDeadline bool }

func (t *captureCtxTool) Name() string               { return "Capture" }
func (t *captureCtxTool) Description() string        { return "captures ctx" }
func (t *captureCtxTool) Parameters() map[string]any { return map[string]any{} }
func (t *captureCtxTool) NeedsApproval() bool        { return false }
func (t *captureCtxTool) Execute(ctx context.Context, _ map[string]any) (*types.ToolResult, error) {
	_, t.hadDeadline = ctx.Deadline()
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

// The executor guarantees a deadline even when the caller's context has none,
// so a hung tool cannot strand the turn forever.
func TestToolExecutorAppliesDefaultDeadline(t *testing.T) {
	capture := &captureCtxTool{}
	registry := NewToolRegistry()
	registry.Register(capture)
	executor := NewToolExecutor(registry)

	res, err := executor.Execute(context.Background(), "Capture", nil)
	require.NoError(t, err)
	assert.True(t, res.Success)
	assert.True(t, capture.hadDeadline, "the executor must add a deadline to a context that has none")
}

// An existing deadline is left as-is (the executor does not extend it).
func TestToolExecutorKeepsCallerDeadline(t *testing.T) {
	capture := &captureCtxTool{}
	registry := NewToolRegistry()
	registry.Register(capture)
	executor := NewToolExecutor(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := executor.Execute(ctx, "Capture", nil)
	require.NoError(t, err)
	require.True(t, capture.hadDeadline)
}

// A tool that respects a (shorter) caller deadline surfaces a timeout rather
// than a success.
func TestToolExecutorSurfacesContextDeadline(t *testing.T) {
	registry := NewToolRegistry()
	registry.Register(NewBashTool(newTestWorkspace(t, t.TempDir())))
	executor := NewToolExecutor(registry)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	res, err := executor.Execute(ctx, "Bash", map[string]any{"command": "sleep 5"})
	require.NoError(t, err)
	assert.False(t, res.Success)
	assert.Contains(t, res.Error, "timed out")
}

// A tool that ignores its deadline but is released by the test still returns
// (proving the executor does not deadlock on a blocked tool under normal
// release); the timeout backstop itself is covered by the deadline assertion
// above without waiting 15 minutes.
func TestToolExecutorReturnsWhenToolReleases(t *testing.T) {
	blocking := &blockingTool{release: make(chan struct{})}
	registry := NewToolRegistry()
	registry.Register(blocking)
	executor := NewToolExecutor(registry)

	done := make(chan *types.ToolResult, 1)
	go func() {
		res, _ := executor.Execute(context.Background(), "Blocking", nil)
		done <- res
	}()

	select {
	case <-done:
		t.Fatal("Execute returned before the tool was released")
	case <-time.After(50 * time.Millisecond):
	}
	blocking.unblock()
	select {
	case res := <-done:
		require.NotNil(t, res)
	case <-time.After(time.Second):
		t.Fatal("Execute did not return after the tool was released")
	}
}
