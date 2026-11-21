package script

import (
	"context"
	"sort"
	"sync"
)

// Grant is the set of tools a human approved for one program run.
//
// A grant is what a resumed program carries: the step a human just answered for.
// Without it the same step would pause again immediately, because the policy has
// no memory of the answer. Deny decisions are never covered — a grant answers ask,
// and only ask.
//
// The type is a context value rather than a field of the runner because the
// policy layer needs it at the moment it evaluates an inner call, and that layer
// only sees the context.
type Grant struct {
	mu     sync.Mutex
	names  map[string]struct{}
	used   map[string]int
	source string
	// step is the single (script, step) pair a human answered for. A resumed
	// program re-runs from the top, so the approval has to be pinned to the step
	// that was asked about — a tool-wide grant would cover every later call too.
	step     *GrantStep
	stepUsed int
}

// GrantStep identifies one paused call inside one program.
type GrantStep struct {
	ScriptID string
	Step     int
}

// NewStepGrant builds a grant for exactly one approved step of one program.
func NewStepGrant(scriptID string, step int) *Grant {
	return &Grant{source: "step-approval", step: &GrantStep{ScriptID: scriptID, Step: step}}
}

// CoversStep reports whether this exact (script, step) pair was approved.
func (g *Grant) CoversStep(scriptID string, step int) bool {
	if g == nil || g.step == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.step.ScriptID == scriptID && g.step.Step == step
}

// RecordStep counts one downgraded step call.
func (g *Grant) RecordStep() {
	if g == nil || g.step == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stepUsed++
}

// NewGrant builds a grant for the approved tools. Empty input yields nil,
// which keeps "no approval happened" and "an empty approval" indistinguishable
// downstream: both mean nothing is covered.
func NewGrant(names []string, source string) *Grant {
	if len(names) == 0 {
		return nil
	}
	g := &Grant{
		names:  make(map[string]struct{}, len(names)),
		used:   make(map[string]int, len(names)),
		source: source,
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		g.names[name] = struct{}{}
	}
	if len(g.names) == 0 {
		return nil
	}
	return g
}

// Covers reports whether this tool was approved for the current run.
func (g *Grant) Covers(name string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.names[name]
	return ok
}

// Record counts one downgraded call. The count is what makes an approval
// auditable after the fact: one question covered N uses, and the record has to
// say N.
func (g *Grant) Record(name string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.used[name]++
}

// Usage reports how many times each covered tool was used.
func (g *Grant) Usage() map[string]int {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]int, len(g.used))
	for name, count := range g.used {
		out[name] = count
	}
	return out
}

// Covered lists the approved tools, sorted for stable reporting.
func (g *Grant) Covered() []string {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]string, 0, len(g.names))
	for name := range g.names {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Source names what produced the grant, for the audit trail.
func (g *Grant) Source() string {
	if g == nil {
		return ""
	}
	return g.source
}

type grantKey struct{}

// WithGrant publishes the approved tools for one program run.
func WithGrant(ctx context.Context, grant *Grant) context.Context {
	if grant == nil {
		return ctx
	}
	return context.WithValue(ctx, grantKey{}, grant)
}

// GrantFromContext returns the grant of the program being executed, if any.
func GrantFromContext(ctx context.Context) *Grant {
	if ctx == nil {
		return nil
	}
	grant, _ := ctx.Value(grantKey{}).(*Grant)
	return grant
}

// blockedKey carries the tools this program may not use because the runtime's
// permissions refuse. Binding them as refusals is what turns
// "tools.Bash is not a function" into a sentence the model can act on: the tool
// exists, the job is not allowed to use it.
type blockedKey struct{}

// WithBlocked publishes the tools that must refuse with an explanatory error.
func WithBlocked(ctx context.Context, names []string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	return context.WithValue(ctx, blockedKey{}, append([]string(nil), names...))
}

// BlockedFromContext returns the tools this program may not use.
func BlockedFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	names, _ := ctx.Value(blockedKey{}).([]string)
	return names
}
