package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// StateTool is the builtin state tool (design doc 26 §6). It lets an Agent
// read and mutate its own cross-session todo state inside the TurnLoop. The
// agent id is resolved from the execution context (the same mechanism the
// Spawn tool uses), so an Agent can never touch another Agent's state.
type StateTool struct {
	states *state.Store
}

// NewStateTool creates the state tool backed by a state.Store.
func NewStateTool(states *state.Store) *StateTool {
	return &StateTool{states: states}
}

func (t *StateTool) Name() string { return "State" }

func (t *StateTool) Description() string {
	return "Read and update your own cross-session state. The state is a directory of blocks " +
		"(goal / confirmed / open_questions / decisions / next_steps / workspace / record_ids) and is NOT injected into your prompt. " +
		"Call action=list with no field to read the index (blocks, entry counts, in_progress, updated_at, revision), " +
		"then action=list with field=<name> when you need the entries. " +
		"Use action=add to append an entry, action=remove to delete by id, action=update to rewrite text and/or status, action=get to read one entry. " +
		"next_steps entries carry status pending / in_progress / completed, and at most one may be in_progress."
}

func (t *StateTool) NeedsApproval() bool { return false }

func (t *StateTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}

func (t *StateTool) Parameters() map[string]any {
	return map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"add", "remove", "update", "list", "get"},
			"description": "The state operation to perform",
		},
		"field": map[string]any{
			"type":        "string",
			"enum":        []string{"goal", "confirmed", "open_questions", "decisions", "next_steps", "workspace", "record_ids"},
			"description": "Which block to operate on. Omit it with action=list to read the index.",
		},
		"text": map[string]any{
			"type":        "string",
			"description": "Entry content for add/update, or the new goal for action=add with field=goal. Empty on update keeps the current text.",
		},
		"status": map[string]any{
			"type":        "string",
			"enum":        []string{"pending", "in_progress", "completed"},
			"description": "Todo status for next_steps entries (default pending). Only one entry may be in_progress. Empty on update keeps the current status.",
		},
		"id": map[string]any{
			"type":        "string",
			"description": "Entry id for remove/update/get (workspace path or record value where applicable)",
		},
	}
}

func (t *StateTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if t == nil || t.states == nil {
		return &types.ToolResult{Success: false, Error: "state tool is not configured"}, nil
	}
	agentID := currentAgentID(ctx)
	if agentID == "" {
		return &types.ToolResult{Success: false, Error: "state tool is only available inside an Agent execution context"}, nil
	}
	actor := currentActor(ctx)

	action, _ := params["action"].(string)
	fieldStr, _ := params["field"].(string)
	text, _ := params["text"].(string)
	id, _ := params["id"].(string)
	status, _ := params["status"].(string)

	field := state.StateField(strings.TrimSpace(fieldStr))

	switch strings.TrimSpace(action) {
	case "add":
		if field == state.FieldGoal {
			if err := t.states.SetGoal(ctx, agentID, text); err != nil {
				return &types.ToolResult{Success: false, Error: err.Error()}, nil
			}
			return &types.ToolResult{Success: true, Content: "goal set"}, nil
		}
		if err := t.states.Add(ctx, agentID, field, text, status, actor); err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return &types.ToolResult{Success: true, Content: "added"}, nil

	case "remove":
		if err := t.states.Remove(ctx, agentID, field, id); err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return &types.ToolResult{Success: true, Content: "removed"}, nil

	case "update":
		if err := t.states.Update(ctx, agentID, field, id, text, status, actor); err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return &types.ToolResult{Success: true, Content: "updated"}, nil

	case "list":
		// Freshness travels with the entries: the state is not in the prompt,
		// so updated_at and revision are the only way to tell this task's
		// entries from another task's leftovers. With no field, list returns
		// the index, which is usually enough to decide what to load.
		if strings.TrimSpace(fieldStr) == "" {
			index, err := t.states.LoadIndex(ctx, agentID)
			if err != nil {
				return &types.ToolResult{Success: false, Error: err.Error()}, nil
			}
			return t.jsonResult(index)
		}
		result, err := t.states.ListWithMeta(ctx, agentID, field)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return t.jsonResult(result)

	case "get":
		item, err := t.states.Get(ctx, agentID, field, id)
		if err != nil {
			return &types.ToolResult{Success: false, Error: err.Error()}, nil
		}
		return t.jsonResult(item)

	default:
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("unknown action %q", action)}, nil
	}
}

func (t *StateTool) jsonResult(value any) (*types.ToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

// currentAgentID resolves the executing agent id from the context. It reuses
// the spawn identity carried by the TurnLoop (see withSpawnIdentity).
//
// It reads the identity rather than the published scope on purpose: this
// package owns both, and the identity is the one that also carries instance
// keys and the agent definition. The scope exists so other packages can see
// the same caller; the precedence rule for "request id first, definition name
// second" lives in agentIDOf so the scope cannot disagree with this function.
func currentAgentID(ctx context.Context) string {
	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return ""
	}
	return agentIDOf(identity.agent, identity.req)
}

// currentActor resolves the full "agent(instance)" identifier used to
// attribute shared-whiteboard entries. When the context carries an instance
// key (a spawned child), it returns "agent(instance)"; otherwise it falls
// back to the bare agent id.
func currentActor(ctx context.Context) string {
	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return ""
	}
	agentID := agentIDOf(identity.agent, identity.req)
	if identity.instanceKey == "" {
		return agentID
	}
	return fmt.Sprintf("%s(%s)", agentID, identity.instanceKey)
}
