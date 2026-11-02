package script

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ToolName is the name the model sees. It is also the recursion guard's key and
// the name the runtime uses to decide whether to publish a gate, so it is
// declared once here and referenced from the agent package.
const ToolName = "Script"

// CatalogSource renders the catalog for a set of tool names. A nil slice means
// "every tool this instance can see", which is what the fallback path uses when
// the caller did not publish a whitelist.
type CatalogSource func(names []string) []CatalogTool

// ScriptTool exposes the sandbox as a single tool. It is the only tool the
// model sees in a Code Mode surface; everything else is reached through it.
//
// It deliberately owns no execution state: the runner, the replay store and the
// catalog are injected so this type stays a thin adapter between the tool
// protocol and the engine.
type ScriptTool struct {
	caller  ToolCaller
	store   StepStore
	catalog CatalogSource
	opts    Options
}

// Options configures one Script tool instance.
type Options struct {
	// Timeout bounds a single execution when the caller does not ask for one.
	Timeout time.Duration
	// MaxSteps bounds inner tool calls per execution.
	MaxSteps int
	// MaxOutput bounds returned text plus console output.
	MaxOutput int
	// CatalogBudget bounds the rendered catalog in the `code` description.
	CatalogBudget int
	// SpecOf reports how a tool may be scheduled. The engine uses it to decide
	// whether a call is effectful, which decides whether a replay may repeat it.
	// When nil every call is treated as effectful, which is slower but safe.
	SpecOf func(name string) types.ToolExecutionSpec
}

// specAdapter turns the injected lookup into the engine's SpecProvider.
type specAdapter func(name string) types.ToolExecutionSpec

func (s specAdapter) ExecutionSpec(name string) types.ToolExecutionSpec { return s(name) }

// composedCaller lets the runner see both the execution path and the scheduling
// spec, which may come from different places (the gate from the context, the
// spec from the registry).
type composedCaller struct {
	call ToolCaller
	spec SpecProvider
}

func (c composedCaller) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	return c.call.Execute(ctx, name, args)
}

func (c composedCaller) ExecutionSpec(name string) types.ToolExecutionSpec {
	if c.spec == nil {
		return types.ToolExecutionSpec{}
	}
	return c.spec.ExecutionSpec(name)
}

// NewScriptTool wires the adapter. catalog may be nil, in which case the
// description carries only the static contract; that is the shape used by tests.
func NewScriptTool(caller ToolCaller, store StepStore, catalog CatalogSource, opts Options) *ScriptTool {
	return &ScriptTool{caller: caller, store: store, catalog: catalog, opts: opts}
}

func (t *ScriptTool) Name() string { return "Script" }

// Description states the contract, not the catalog. The catalog is per-session
// (it depends on the agent's tool whitelist) and is rendered into the code
// parameter instead, so it is not cached in a static string.
func (t *ScriptTool) Description() string {
	return "Run a JavaScript program that calls this session's tools synchronously. " +
		"Intermediate results stay inside the program; only the returned value and console output come back. " +
		"Use it for batch work: scan many files, aggregate, filter, then return only the conclusion."
}

// Parameters mirrors what the model must know before writing code. It carries
// the static contract only: the catalog is injected into the schema by the
// runtime (CodeDescriptionFor), because rendering it here would make
// Parameters() re-enter the registry — and the registry holds this tool.
func (t *ScriptTool) Parameters() map[string]any {
	return map[string]any{
		"code": map[string]any{
			"type":        "string",
			"description": t.contractText(),
		},
		"timeout_ms": map[string]any{
			"type":        "integer",
			"description": "Compute budget in milliseconds. Tool calls do not count against it. Default 60000, max 600000.",
		},
	}
}

// NeedsApproval is true because the whole program is the unit a human reviews.
// Inner calls are still gated one by one by the caller's policy layer; this flag
// covers the "model can run arbitrary code with tool access" decision.
func (t *ScriptTool) NeedsApproval() bool { return true }

// Execution is serial: a program may write, and running it beside another tool
// call would make the write order depend on scheduling.
func (t *ScriptTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}

// CodeDescription renders the catalog for the model-facing schema. The agent
// runtime calls it while building tool schemas; tests call it directly.
func (t *ScriptTool) CodeDescription() string { return t.codeDescription(nil) }

// CodeDescriptionFor renders the catalog restricted to the tools one agent may
// use. The runtime has the agent config when it builds schemas, so this is where
// the listing gets narrowed: a catalog that advertises tools the agent cannot
// call teaches the model that the refusal is unreliable.
func (t *ScriptTool) CodeDescriptionFor(names []string) string {
	if len(names) == 0 {
		return t.codeDescription(nil)
	}
	return t.codeDescription(names)
}

func (t *ScriptTool) codeDescription(names []string) string {
	var b strings.Builder
	b.WriteString(t.contractText())

	if t.catalog == nil {
		return b.String()
	}
	entries := t.catalog(names)
	if len(entries) == 0 {
		return b.String()
	}
	b.WriteString("\nAvailable tools:\n")
	b.WriteString(RenderCatalog(entries, t.opts.CatalogBudget))
	return b.String()
}

// contractText is the part of the description that never depends on the
// session. Splitting it out is what lets Parameters() stay pure: a description
// that calls the catalog would recurse through the registry.
func (t *ScriptTool) contractText() string {
	var b strings.Builder
	b.WriteString("JavaScript program body. It runs as a function body, so `return` is allowed and top-level declarations are local.\n")
	b.WriteString("Contract:\n")
	b.WriteString("- Tools are synchronous: `const r = tools.Read({file: \"x\"})`; there is no async, no Promise, no setTimeout.\n")
	b.WriteString("- Every tool returns {ok, content, metadata?}; failures throw a ToolCallError you can catch.\n")
	b.WriteString("- Only the returned value and console.log output are sent back. Filter and aggregate before returning.\n")
	b.WriteString("- Batch independent calls with `tools.all([{name, args}, ...])`: read-only members run concurrently, results come back in the same order. Never batch calls that depend on each other.\n")
	b.WriteString("- No require, no fs, no fetch. All side effects go through tools.\n")
	b.WriteString("- Date.now() and Math.random() are pinned so a rerun takes the same path. Do not use new Date().\n")
	b.WriteString("- Write an existing file with mode \"edit\" (old_text + base_revision from Read); mode \"replace\" overwrites the whole file.\n")
	return b.String()
}

// gateCaller adapts the caller-side Gate to the engine's ToolCaller.
type gateCaller struct{ gate Gate }

func (g gateCaller) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	return g.gate(ctx, name, args)
}

// Execute runs one program.
func (t *ScriptTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	code, _ := params["code"].(string)
	if strings.TrimSpace(code) == "" {
		return &types.ToolResult{Success: false, Error: "code parameter is required"}, nil
	}
	if t.caller == nil {
		return &types.ToolResult{Success: false, Error: "tool executor is not configured"}, nil
	}

	// The visible set comes from the context when the runtime published one: the
	// agent's whitelist is per-turn state, so the tool itself cannot know it.
	names := ToolNamesFromContext(ctx)
	if len(names) == 0 && t.catalog != nil {
		for _, e := range t.catalog(nil) {
			if e.Name == ToolName {
				// Never bind the tool to itself: a nested script would get its own
				// budget and replay log, and the outer step count would stop
				// describing the work.
				continue
			}
			names = append(names, e.Name)
		}
	}

	scriptID := scriptIDOf(code)
	timeout := time.Duration(0)
	if ms, ok := numberParam(params, "timeout_ms"); ok {
		timeout = time.Duration(ms) * time.Millisecond
	}

	// Prefer the caller-side gate: it is the only path that keeps policy, the
	// bash gate, hooks and approvals in force for inner calls. The bare caller
	// is the fallback for tests and for hosts that have no gate to publish.
	caller := t.caller
	if gate, ok := GateFromContext(ctx); ok {
		caller = gateCaller{gate: gate}
	}
	var spec SpecProvider
	if t.opts.SpecOf != nil {
		spec = specAdapter(t.opts.SpecOf)
	} else if provider, ok := t.caller.(SpecProvider); ok {
		spec = provider
	}

	runner := &Runner{Caller: composedCaller{call: caller, spec: spec}, Store: t.store}
	ex, err := runner.Run(ctx, RunRequest{
		ScriptID:  scriptID,
		Code:      code,
		Timeout:   timeout,
		MaxSteps:  t.opts.MaxSteps,
		MaxOutput: t.opts.MaxOutput,
		ToolNames: names,
	})

	result := &types.ToolResult{
		Success:      err == nil,
		Content:      t.renderContent(ex),
		Metadata:     t.metadata(ex),
		WorkspaceOps: collectWorkspaceOps(ex.Steps),
	}
	if err != nil {
		result.Error = err.Error()
		if result.Content == "" {
			result.Content = err.Error()
		}
	}
	return result, nil
}

// renderContent is what the model reads. The returned value comes first because
// it is the answer; console output follows because it is usually diagnostics.
func (t *ScriptTool) renderContent(ex Execution) string {
	var b strings.Builder
	if ex.Value != "" {
		b.WriteString(ex.Value)
	}
	if len(ex.Console) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("console:\n")
		b.WriteString(strings.Join(ex.Console, "\n"))
	}
	if ex.Truncated {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[output truncated at the configured limit]")
	}
	return b.String()
}

// metadata is the machine-readable half: how many inner calls landed, whether
// the run was cut short, and what the replay key was.
func (t *ScriptTool) metadata(ex Execution) map[string]any {
	meta := map[string]any{"steps": len(ex.Steps)}
	if ex.Canceled {
		meta["canceled"] = true
	}
	if ex.TimedOut {
		meta["timed_out"] = true
	}
	if ex.Truncated {
		meta["truncated"] = true
	}
	if len(ex.Steps) > 0 {
		calls := make([]map[string]any, 0, len(ex.Steps))
		for _, step := range ex.Steps {
			calls = append(calls, map[string]any{
				"step": step.Step,
				"tool": step.ToolName,
			})
		}
		meta["calls"] = calls
	}
	return meta
}

// collectWorkspaceOps lifts inner write bookkeeping to the outer result. Without
// this the turn's file history would miss every write a script performed.
func collectWorkspaceOps(steps []StepRecord) []types.WorkspaceOperation {
	var ops []types.WorkspaceOperation
	for _, step := range steps {
		if step.Result == nil {
			continue
		}
		ops = append(ops, step.Result.WorkspaceOps...)
	}
	return ops
}

// scriptIDOf derives the replay key from the program text. Two identical
// programs in one session therefore share a replay log, which is correct: the
// same code over the same tool results must produce the same outcome. The store
// is scoped per session, so nothing leaks across conversations.
func scriptIDOf(code string) string {
	sum := sha256.Sum256([]byte(code))
	return "script-" + hex.EncodeToString(sum[:12])
}

func numberParam(params map[string]any, key string) (int64, bool) {
	raw, ok := params[key]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	case jsonNumber:
		if i, err := v.Int64(); err == nil {
			return i, true
		}
	}
	return 0, false
}

// jsonNumber matches encoding/json's number type without importing it here for
// a single type assertion.
type jsonNumber interface{ Int64() (int64, error) }

func (t *ScriptTool) String() string { return fmt.Sprintf("Script(budget=%d)", t.opts.CatalogBudget) }
