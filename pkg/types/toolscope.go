package types

import "context"

// ToolScope is the narrow slice of the executing AgentTurn that a Tool may
// need in order to decide what it is allowed to touch.
//
// It exists because the identity that identifies a caller (the TurnLoop's
// spawn identity, internal/agent/spawn.go) is unexported, and every attempt to
// keep it unexported while letting internal/tool read it fails the same way:
// a context key is only reachable by the package that declares its type. Since
// internal/agent and internal/tool do not import each other in either
// direction, neither can own a key the other reads without inventing an
// import that does not belong there. This package is the lowest common
// ancestor: both already depend on it, and it depends on nothing inside the
// engine, so it can carry the key without risking a cycle.
//
// The fields are deliberately not the whole identity. Internal/agent knows who
// the parent call is, what the request's turn ids are, which instance key a
// spawned child runs under, and the agent's entire configuration; none of that
// is exported here, so moving the key cannot silently promote internal/agent's
// internals into public API. Adding a field is a deliberate act with a
// security consequence — each one is another thing a Tool can be told about
// its caller and another thing a future path filter might trust — so the set
// stays at "who is asking" and grows only when a concrete filter needs it.
//
// Empty fields are meaningful, not "unknown": an empty AgentID means the
// caller is not an agent (or is an agent whose id could not be resolved), and
// a filter reading this value must treat that as "matches no private owner"
// rather than "matches every owner". See ToolScopeFromContext for why a
// missing scope is reported as absence rather than as a zero value.
type ToolScope struct {
	// AgentID is the id of the agent executing the tool call. It is what
	// makes an agent-private knowledge directory reachable to its owner and
	// to nobody else.
	AgentID string
	// TeamID is the id of the team the call is executing under, empty when
	// the agent runs outside a team.
	TeamID string
}

// toolScopeKey is the single context key carrying a ToolScope. It is an
// unexported struct type, so no other package can collide with it, and there
// is exactly one of them: a second key in another package would be invisible
// to readers of this one, and a context that carried both would resolve
// differently depending on which accessor a caller happened to use. That
// failure is silent, which is why the value travels by exactly this route —
// internal/agent writes it, and it is what internal/agent's own spawn
// identity is built from, so the two cannot diverge.
type toolScopeKey struct{}

// WithToolScope returns a context carrying scope. It is called by the
// TurnLoop, once per AgentTurn, before any tool executes.
//
// A zero scope is still written rather than skipped: "this turn has no
// resolvable agent/team" is a fact downstream filters must be able to observe,
// and dropping the value instead would make it indistinguishable from a
// context that never came from a turn at all. The distinction matters because
// the two cases get the same treatment today (see internal/tool's deny list)
// but for different reasons, and collapsing them here would hide the second
// reason.
func WithToolScope(ctx context.Context, scope ToolScope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, toolScopeKey{}, scope)
}

// ToolScopeFromContext returns the scope of the AgentTurn that led to this
// context, and whether one was present.
//
// The boolean is not decoration. A caller that cannot tell "no scope" from
// "scope with empty ids" will write `scope, _ := ToolScopeFromContext(ctx)`
// and then filter on scope.AgentID, which silently degrades to "matches no
// owner" — correct by luck today, and wrong the moment an empty id is a legal
// id. Callers must decide explicitly, and the deny-list helper in
// internal/tool is the place that decision is made and documented.
func ToolScopeFromContext(ctx context.Context) (ToolScope, bool) {
	if ctx == nil {
		return ToolScope{}, false
	}
	scope, ok := ctx.Value(toolScopeKey{}).(ToolScope)
	return scope, ok
}
