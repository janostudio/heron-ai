package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ScriptToolName is the single tool that exposes the sandbox. It has to be
// spelled here as well as in the tool package because the runtime makes three
// separate decisions by name: schema visibility, gate injection, and the
// recursion guard below.
const ScriptToolName = script.ToolName

// SetScriptDescription wires the description renderer for the Script tool.
// Without it the tool is neither advertised nor gateable, which is how the
// feature stays off by default.
//
// The callback takes the agent's tool whitelist because the catalog has to
// match it: a listing that offers tools the agent cannot call teaches the model
// that refusals are unreliable.
func (t *TurnLoop) SetScriptDescription(describe func(names []string) string) {
	t.scriptDescriber = describe
}

// scriptVisibleTools is the set the sandbox may call, mirroring the schema-side
// rule that Collect comes with Team or Spawn. It is deliberately the same
// expression the tool schema loop uses, so the catalog and the model's own tool
// list cannot disagree.
func scriptVisibleTools(agent types.AgentConfig) []string {
	names := append([]string(nil), agent.Tools.Builtin...)
	has := func(want string) bool {
		for _, name := range names {
			if name == want {
				return true
			}
		}
		return false
	}
	if (has(TeamToolName) || has("Spawn")) && !has("Collect") {
		names = append(names, "Collect")
	}
	return names
}

// tagScriptEvent adds the script identity to a tool event payload when the call
// originated inside a script. Keeping it in one place is deliberate: the emit
// sites are numerous and a missed one produces an event that looks like a
// top-level tool call, which is invisible until someone tries to group by script.
func tagScriptEvent(ctx context.Context, payload map[string]any) map[string]any {
	scriptID, step, ok := script.StepFromContext(ctx)
	if !ok {
		return payload
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["script_call_id"] = scriptID
	payload["script_step"] = step
	return payload
}

// withScriptGate publishes the caller-side gate when the batch contains a Script
// call.
//
// The gate is what keeps inner calls inside the engine's normal execution path.
// A script that called the tool executor directly would bypass the policy
// layer, the bash gate, PreToolUse hooks and approval accounting: every one of
// those lives in executeToolCalls, not in the executor. Delegating back into
// executeToolCalls with a single call is the cheapest way to guarantee the two
// paths cannot drift.
func (t *TurnLoop) withScriptGate(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, round int, calls []types.ToolCall) context.Context {
	wantsScript := false
	for _, call := range calls {
		if call.Name == ScriptToolName {
			wantsScript = true
			break
		}
	}
	if !wantsScript {
		return ctx
	}
	// The whitelist travels with the turn: the sandbox lists it and binds it, so
	// the catalog and the callable set are the same list by construction.
	ctx = script.WithToolNames(ctx, scriptVisibleTools(agent))
	return script.WithGate(ctx, func(gatedCtx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
		if name == ScriptToolName {
			// Nesting would mean a script inside a script: the inner one would
			// get its own gate, its own step budget and its own replay log, and
			// the outer step count would no longer describe the work done.
			return &types.ToolResult{
				Success: false,
				Error:   "Script cannot call itself; call the inner tools directly",
			}, nil
		}
		call := types.ToolCall{
			ID:        fmt.Sprintf("script-%d", time.Now().UnixNano()),
			Name:      name,
			Arguments: args,
		}
		results := t.executeToolCalls(gatedCtx, agent, req, round, 1, []types.ToolCall{call})
		if len(results) == 0 {
			return &types.ToolResult{Success: false, Error: "tool produced no result"}, nil
		}
		result := results[0]
		if result != nil && result.PendingApproval != nil {
			// A script cannot host an approval dialog: there is no way to pause
			// mid-program and resume it after the human answers. Say so plainly
			// instead of returning a pending state the sandbox would treat as a
			// tool error with no explanation.
			return &types.ToolResult{
				Success: false,
				Error: fmt.Sprintf(
					"%s needs human approval, which a script cannot request. Call it directly once so the session records the approval, then run the script again",
					name,
				),
			}, nil
		}
		return result, nil
	})
}
