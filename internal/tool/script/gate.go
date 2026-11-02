package script

import (
	"context"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Gate is the caller-side path for one inner tool call. The agent runtime
// publishes it into the context before invoking the Script tool, so inner calls
// go through the same policy, bash gate, hooks and approval path as a direct
// call would.
//
// Without this indirection the sandbox would have to reimplement the gate, and
// two implementations of a security check is one too many: they drift, and the
// drift is invisible because both keep working.
type Gate func(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)

type gateKey struct{}

// WithGate attaches the caller-side gate to ctx.
func WithGate(ctx context.Context, gate Gate) context.Context {
	if gate == nil {
		return ctx
	}
	return context.WithValue(ctx, gateKey{}, gate)
}

// GateFromContext returns the gate published by the agent runtime.
func GateFromContext(ctx context.Context) (Gate, bool) {
	if ctx == nil {
		return nil, false
	}
	gate, ok := ctx.Value(gateKey{}).(Gate)
	return gate, ok && gate != nil
}

// stepKey carries the script identity and step number of the call in flight.
// The runtime reads it to tag the tool events it emits, which is what lets a
// reader group inner calls under their script instead of seeing them as
// unrelated top-level tool calls.
type stepKey struct{}

type stepInfo struct {
	scriptID string
	step     int
}

// WithStep tags ctx with the script call and step an inner tool call belongs to.
func WithStep(ctx context.Context, scriptID string, step int) context.Context {
	if scriptID == "" {
		return ctx
	}
	return context.WithValue(ctx, stepKey{}, stepInfo{scriptID: scriptID, step: step})
}

// StepFromContext returns the script identity and step for the call in flight.
func StepFromContext(ctx context.Context) (string, int, bool) {
	if ctx == nil {
		return "", 0, false
	}
	info, ok := ctx.Value(stepKey{}).(stepInfo)
	if !ok {
		return "", 0, false
	}
	return info.scriptID, info.step, true
}

// toolNamesKey carries the tool whitelist of the agent that owns the turn. The
// tool itself has no access to the agent config, so the runtime publishes the
// resolved list here; both the catalog and the sandbox bindings read it, which
// keeps "what is listed" and "what is callable" the same set.
type toolNamesKey struct{}

// WithToolNames publishes the per-turn tool whitelist.
func WithToolNames(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, toolNamesKey{}, append([]string(nil), names...))
}

// ToolNamesFromContext returns the published whitelist, if any.
func ToolNamesFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	names, _ := ctx.Value(toolNamesKey{}).([]string)
	return names
}
