package agent

import (
	"context"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// SpawnTaskDispatcher is the ToolExecutor handed to the shared
// AsyncToolExecutor (design 21 §4.3). It routes the internal SpawnChild task
// name to the Spawn tool — which runs one spawned child entity turn — and
// forwards every other tool to the base executor unchanged. SpawnChild is
// never registered in the Tool registry, so models cannot invoke it; only
// durable tasks created by an asynchronous Spawn (wait=false) reach this
// path.
type SpawnTaskDispatcher struct {
	spawn *SpawnTool
	base  ToolExecutor
}

// NewSpawnTaskDispatcher creates the dispatcher. base may be nil in tests
// that only exercise SpawnChild routing.
func NewSpawnTaskDispatcher(spawn *SpawnTool, base ToolExecutor) *SpawnTaskDispatcher {
	return &SpawnTaskDispatcher{spawn: spawn, base: base}
}

func (d *SpawnTaskDispatcher) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if d == nil {
		return &types.ToolResult{Success: false, Error: "spawn task dispatcher is not configured"}, nil
	}
	if name == SpawnChildToolName {
		if d.spawn == nil {
			return &types.ToolResult{Success: false, Error: "spawn tool is not configured"}, nil
		}
		return d.spawn.executeChildTask(ctx, args)
	}
	if d.base == nil {
		return &types.ToolResult{Success: false, Error: "tool executor is not configured"}, nil
	}
	return d.base.Execute(ctx, name, args)
}

// ExecutionSpec keeps the AsyncToolExecutor's restart classification working
// for tasks routed through the dispatcher: an interrupted SpawnChild turn is
// failed on recovery (children may have side effects and are never silently
// re-run), while every other tool keeps its base execution spec.
func (d *SpawnTaskDispatcher) ExecutionSpec(name string) types.ToolExecutionSpec {
	if name == SpawnChildToolName {
		return types.ToolExecutionSpec{Class: types.ToolSerial, RestartSafe: false}
	}
	if d != nil && d.base != nil {
		if inspector, ok := d.base.(toolExecutionInspector); ok {
			return inspector.ExecutionSpec(name)
		}
	}
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
