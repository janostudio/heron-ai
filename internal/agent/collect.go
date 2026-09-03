package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// CollectTool implements the builtin Collect tool (design 20 §2.1, decision
// D10). It blocks until the spawned children referenced by handles (returned
// by an asynchronous Spawn, wait=false + deliver=parent) reach a terminal
// state, then returns their results aligned with the handles. Handles that
// already finished return immediately. A failed child is reported as a
// per-handle error entry, not as a failure of the Collect call itself.
//
// Durability: the referenced tasks live in the durable tool task store, so a
// Collect in a later parent turn (or after a process restart) still resolves
// them. Children interrupted by a restart are failed by AsyncToolExecutor
// recovery and surface as per-handle errors here.
type CollectTool struct {
	runner *AsyncToolExecutor
}

// NewCollectTool creates the Collect tool on top of the shared async tool
// executor (task store + subscriptions).
func NewCollectTool(runner *AsyncToolExecutor) *CollectTool {
	return &CollectTool{runner: runner}
}

func (t *CollectTool) Name() string { return "Collect" }

func (t *CollectTool) Description() string {
	return "Block until spawned children from asynchronous Spawn calls (wait=false) finish and return their results, aligned with the given handles. Failed children are reported per handle."
}

func (t *CollectTool) NeedsApproval() bool { return false }

func (t *CollectTool) Execution() types.ToolExecutionSpec {
	// Collect blocks until children finish; it must not run in parallel with
	// other tool calls of the same round.
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}

func (t *CollectTool) Parameters() map[string]any {
	return map[string]any{
		"handles": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "Handles (task ids) returned by asynchronous Spawn calls",
		},
	}
}

func (t *CollectTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.runner == nil || t.runner.TaskStore() == nil {
		return collectError("Collect tool is not configured"), nil
	}
	raw, ok := params["handles"]
	if !ok {
		return collectError("Collect requires handles from an asynchronous Spawn"), nil
	}
	list, ok := raw.([]any)
	if !ok {
		return collectError("Collect handles must be an array of task ids"), nil
	}
	if len(list) == 0 {
		return collectError("Collect handles must not be empty"), nil
	}
	handles := make([]string, 0, len(list))
	for i, entry := range list {
		switch value := entry.(type) {
		case string:
			handles = append(handles, value)
		case map[string]any:
			id, _ := value["task_id"].(string)
			if id == "" {
				return collectError(fmt.Sprintf("Collect handle %d is missing task_id", i)), nil
			}
			handles = append(handles, id)
		default:
			return collectError(fmt.Sprintf("Collect handle %d must be a task id string or an object with task_id", i)), nil
		}
	}

	entries := make([]map[string]any, len(handles))
	resolved := 0
	interrupted := ""
	for i, handle := range handles {
		task, err := t.waitTerminal(ctx, handle)
		if err != nil {
			entries[i] = map[string]any{"handle": handle, "error": err.Error()}
			if ctx.Err() != nil {
				interrupted = ctx.Err().Error()
			}
			continue
		}
		resolved++
		entries[i] = collectEntry(handle, task)
	}
	content, _ := json.Marshal(entries)
	result := &types.ToolResult{
		Success: resolved == len(handles),
		Content: string(content),
		Metadata: map[string]any{
			"handles":  len(handles),
			"resolved": resolved,
		},
	}
	if resolved < len(handles) {
		result.Error = fmt.Sprintf("%d of %d handles could not be resolved", len(handles)-resolved, len(handles))
		if interrupted != "" {
			result.Error += ": " + interrupted
		}
	}
	return result, nil
}

// waitTerminal returns the task once it reaches a terminal status. It first
// checks the durable store, then subscribes to live updates; a subscription
// that closes without a terminal state (dropped slow subscriber) re-checks
// the store instead of busy-looping on a fresh subscription.
func (t *CollectTool) waitTerminal(ctx context.Context, id string) (*types.ToolTask, error) {
	for {
		task, err := t.runner.Load(ctx, id)
		if err != nil {
			return nil, err
		}
		if toolTaskTerminal(task.Status) {
			return task, nil
		}
		updates, err := t.runner.Subscribe(ctx, id)
		if err != nil {
			return nil, err
		}
		for update := range updates {
			if toolTaskTerminal(update.Status) {
				task = &update
				return task, nil
			}
		}
		// Channel closed without a terminal update: context cancelled or the
		// subscriber was dropped. Re-check the durable task.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func collectEntry(handle string, task *types.ToolTask) map[string]any {
	entry := map[string]any{
		"handle":  handle,
		"task_id": task.ID,
		"status":  string(task.Status),
	}
	if key, ok := task.Arguments["key"].(string); ok && key != "" {
		entry["key"] = key
	}
	if task.Result != nil {
		var payload map[string]any
		if json.Unmarshal([]byte(task.Result.Content), &payload) == nil {
			// Merge the child outcome (reply / error); the task-level status
			// and the task's own key remain authoritative.
			for field, value := range payload {
				if field == "status" || field == "key" {
					continue
				}
				entry[field] = value
			}
		} else if task.Result.Content != "" {
			entry["reply"] = task.Result.Content
		}
	}
	if task.Status != types.ToolTaskCompleted && task.Error != "" {
		entry["error"] = task.Error
	}
	return entry
}

func toolTaskTerminal(status types.ToolTaskStatus) bool {
	return status == types.ToolTaskCompleted ||
		status == types.ToolTaskFailed ||
		status == types.ToolTaskCancelled
}

func collectError(message string) *types.ToolResult {
	return &types.ToolResult{Success: false, Error: message, Content: message}
}
