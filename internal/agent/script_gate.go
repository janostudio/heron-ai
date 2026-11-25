package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ScriptToolName is the single tool that exposes the sandbox. It has to be
// spelled here as well as in the tool package because the runtime makes three
// separate decisions by name: schema visibility, gate injection, and the
// recursion guard below.
const ScriptToolName = script.ToolName

// isScriptTool reports whether name is the code-execution tool. The gate, the
// recursion guard and the allowlist all key on the same name; a call spelled
// any other way is an ordinary tool call.
func isScriptTool(name string) bool {
	return name == script.ToolName
}

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

// scriptAsyncTools never bind inside a program. Team, Spawn and Collect hand work
// to another Team and wake up later; a program is one synchronous run, so these
// calls would break dispatch and wake-up semantics rather than do what the model
// meant. This used to be a convention written in Agent definitions; binding is
// where it becomes true.
var scriptAsyncTools = map[string]struct{}{
	TeamToolName: {},
	"Spawn":      {},
	"Collect":    {},
}

// PosturePolicy is the part of a tool policy the global posture answers. It is a
// narrow interface rather than a concrete type so a host can supply its own policy
// and still get the REPL behaviour right.
type PosturePolicy interface {
	DenyTools() []string
}

// agentToolUniverse is every tool a program may call, spelled the way the executor's
// allowlist spells it: builtin, custom and the MCP names the Agent declares. The
// builtin half comes from SandboxBuiltin(), i.e. the declared set minus tools.direct:
// a tool the model calls itself is not also bound inside a program, which is what
// makes a REPL-only definition a single surface instead of two overlapping ones.
//
// Custom and MCP tools are always part of the universe: they have no sandbox-only
// counterpart, so excluding them here would silently weaken a program relative to
// the model.
func agentToolUniverse(agent types.AgentConfig) []string {
	sandbox := agent.Tools.SandboxBuiltin()
	names := make([]string, 0, len(sandbox)+len(agent.Tools.Custom)+len(agent.Tools.MCP))
	names = append(names, sandbox...)
	names = append(names, agent.Tools.Custom...)
	names = append(names, agent.Tools.MCP...)
	sort.Strings(names)
	return uniqueStrings(names)
}

// scriptVisibleTools is the catalog the model reads: the same set the sandbox will
// bind, so the listing and the callable set cannot disagree. Tools this Flow denies
// are left out rather than listed and refused: a catalog offering what the gate
// refuses teaches the model that refusals are noise.
func scriptVisibleTools(agent types.AgentConfig, denied map[string]bool) []string {
	var out []string
	for _, name := range agentToolUniverse(agent) {
		if isScriptTool(name) {
			continue
		}
		if _, async := scriptAsyncTools[name]; async {
			continue
		}
		if denied[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// scriptBindableTools is what a program may call: the Agent's tools minus the
// program tool itself, minus the hand-off tools, minus anything this Flow denies.
// It is the same set a direct model call is limited to, which is what makes the
// program and the model two callers of one policy rather than two systems.
func (t *TurnLoop) scriptBindableTools(agent types.AgentConfig) []string {
	denied := t.deniedTools()
	var out []string
	for _, name := range agentToolUniverse(agent) {
		if isScriptTool(name) {
			continue
		}
		if _, async := scriptAsyncTools[name]; async {
			continue
		}
		if denied[name] {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// scriptBlockedTools lists the Agent's tools the posture denies, so the sandbox can
// refuse them by name with the reason. A tool that silently disappears reads like
// a missing feature; a refusal that names the posture reads like a decision.
func (t *TurnLoop) scriptBlockedTools(agent types.AgentConfig) []string {
	denied := t.deniedTools()
	var out []string
	for _, name := range agentToolUniverse(agent) {
		if isScriptTool(name) {
			continue
		}
		if denied[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func (t *TurnLoop) deniedTools() map[string]bool {
	out := map[string]bool{}
	policy, ok := t.toolPolicy.(PosturePolicy)
	if !ok {
		return out
	}
	for _, name := range policy.DenyTools() {
		out[name] = true
	}
	return out
}

// stepGrant reads the resume coordinates out of a REPL call's arguments. Only a
// call that came back from an approval has them; a fresh program has none, which
// means no grant and every gated step pauses.
func stepGrant(args map[string]any) *script.Grant {
	if args == nil {
		return nil
	}
	scriptID, _ := args["resume_script_id"].(string)
	if scriptID == "" {
		return nil
	}
	step, ok := intArg(args["resume_step"])
	if !ok {
		return nil
	}
	return script.NewStepGrant(scriptID, step)
}

func intArg(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	}
	return 0, false
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := values[:1]
	for _, value := range values[1:] {
		if value == out[len(out)-1] {
			continue
		}
		out = append(out, value)
	}
	return out
}

// withScriptGate publishes the caller-side gate when the batch contains a Script
// call, and narrows what that program can even see.
//
// The gate is what keeps inner calls inside the engine's normal execution path. A
// script that called the tool executor directly would bypass the policy layer, the
// bash gate, PreToolUse hooks and approval accounting: every one of those lives in
// executeToolCalls, not in the executor. Delegating back into executeToolCalls with
// a single call is the cheapest way to guarantee the two paths cannot drift.
func (t *TurnLoop) withScriptGate(ctx context.Context, agent types.AgentConfig, req types.AgentRequest, round int, calls []types.ToolCall) context.Context {
	scriptCalls := filterScriptCalls(calls)
	if len(scriptCalls) == 0 {
		return ctx
	}
	bindable := t.scriptBindableTools(agent)
	allowed := make(map[string]struct{}, len(bindable))
	for _, name := range bindable {
		allowed[name] = struct{}{}
	}
	// The whitelist travels with the turn: the sandbox lists it and binds it, so the
	// catalog and the callable set are the same list by construction.
	ctx = script.WithToolNames(ctx, bindable)
	ctx = script.WithBlocked(ctx, t.scriptBlockedTools(agent))
	// A resumed program carries the step a human just answered for. The grant is
	// pinned to it, not to the tool: the program re-runs from the top, and a
	// tool-wide grant would silently cover every later call of the same tool.
	ctx = script.WithGrant(ctx, stepGrant(scriptCalls[0].Arguments))
	return script.WithGate(ctx, func(gatedCtx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
		if isScriptTool(name) {
			// Nesting would mean a script inside a script: the inner one would get
			// its own gate, its own step budget and its own replay log, and the outer
			// step count would no longer describe the work done.
			return &types.ToolResult{
				Success: false,
				Error:   script.ToolName + " cannot call itself; call the inner tools directly",
			}, nil
		}
		// The gate is the one door every inner call walks through, so the posture is
		// enforced here as well as in the binding: `tools.call(name)` and
		// `tools.all([{name, ...}])` take the name as data and never touch the bound
		// properties.
		if _, ok := allowed[name]; !ok {
			return &types.ToolResult{
				Success: false,
				Error: fmt.Sprintf(
					"%s is not available to this program: this runtime's permissions do not allow it", name),
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
			// The call needs a human. Instead of failing the program, hand the request
			// back through the sandbox: it stops the VM at this step, the turn parks as
			// waiting_approval, and the resumed run replays everything before the step
			// and executes it under the approval. Refusing here would make an ordinary
			// permission question indistinguishable from a broken program.
			scriptID, step, ok := script.StepFromContext(gatedCtx)
			request := script.ApprovalRequest{
				Tool:   name,
				Args:   args,
				Reason: result.PendingApproval.Reason,
			}
			if ok {
				request.ScriptID = scriptID
				request.Step = step
			}
			return nil, &script.ErrApprovalRequired{Request: request}
		}
		return result, nil
	})
}

func filterScriptCalls(calls []types.ToolCall) []types.ToolCall {
	var out []types.ToolCall
	for _, call := range calls {
		if isScriptTool(call.Name) {
			out = append(out, call)
		}
	}
	return out
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
