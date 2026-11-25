package types

type AgentConfig struct {
	Name       string            `yaml:"name" json:"name"`
	Persona    PersonaConfig     `yaml:"persona" json:"persona"`
	Model      ModelConfig       `yaml:"model" json:"model"`
	Tools      ToolConfig        `yaml:"tools" json:"tools"`
	Skills     []string          `yaml:"skills" json:"skills"`
	Knowledge  []string          `yaml:"knowledge" json:"knowledge"`
	Rules      []string          `yaml:"rules" json:"rules"`
	Loop       LoopConfig        `yaml:"loop" json:"loop"`
	Context    ContextConfig     `yaml:"context,omitempty" json:"context,omitempty"`
	Budget     AgentBudget       `yaml:"budget,omitempty" json:"budget,omitempty"`
	Completion CompletionConfig  `yaml:"completion,omitempty" json:"completion,omitempty"`
	Structured *StructuredOutput `yaml:"structured_output,omitempty" json:"structured_output,omitempty"`
	HITL       *HITLConfig       `yaml:"hitl,omitempty" json:"hitl,omitempty"`
	Hooks      []HookConfig      `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Workspace  *WorkspaceConfig  `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Body       string            `yaml:"-" json:"body"`
}

type PersonaConfig struct {
	Role      string `yaml:"role" json:"role"`
	Goal      string `yaml:"goal" json:"goal"`
	Backstory string `yaml:"backstory" json:"backstory"`
}

type ModelConfig struct {
	Provider          string           `yaml:"provider" json:"provider"`
	Model             string           `yaml:"model" json:"model"`
	MaxInputTokens    int              `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	Temperature       *float64         `yaml:"temperature,omitempty" json:"temperature,omitempty"`
	TopP              *float64         `yaml:"top_p,omitempty" json:"top_p,omitempty"`
	TopK              *int             `yaml:"top_k,omitempty" json:"top_k,omitempty"`
	RepetitionPenalty *float64         `yaml:"repetition_penalty,omitempty" json:"repetition_penalty,omitempty"`
	Reasoning         *ReasoningConfig `yaml:"reasoning,omitempty" json:"reasoning,omitempty"`
	MaxOutputTokens   *int             `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	MaxTokens         int              `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"` // Deprecated alias.
	APIKey            string           `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL           string           `yaml:"base_url,omitempty" json:"base_url,omitempty"`
	// ResponseFormat is populated by the Agent runtime for one request. It is
	// not part of the model registry; the schema belongs to AgentConfig.
	ResponseFormat *StructuredOutput `yaml:"-" json:"-"`
}

// OutputTokenLimit returns the explicit per-request output limit. The old
// max_tokens spelling remains readable so existing Agent files continue to
// work while new configuration can use max_output_tokens.
func (c ModelConfig) OutputTokenLimit() *int {
	if c.MaxOutputTokens != nil {
		return c.MaxOutputTokens
	}
	if c.MaxTokens > 0 {
		value := c.MaxTokens
		return &value
	}
	return nil
}

// ReasoningConfig is intentionally provider-neutral. Providers decide which
// fields they can map to their native request format.
type ReasoningConfig struct {
	Type         string `yaml:"type,omitempty" json:"type,omitempty"`
	Effort       string `yaml:"effort,omitempty" json:"effort,omitempty"`
	Summary      string `yaml:"summary,omitempty" json:"summary,omitempty"`
	BudgetTokens *int   `yaml:"budget_tokens,omitempty" json:"budget_tokens,omitempty"`
}

type ToolConfig struct {
	Builtin []string `yaml:"builtin" json:"builtin"`
	Custom  []string `yaml:"custom,omitempty" json:"custom,omitempty"`
	MCP     []string `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	// Direct is the escape hatch for a surface that is neither "everything
	// declared is injected" nor "REPL only". When set, exactly this list is
	// rendered into the request and the rest of Builtin is reachable only from
	// inside a program. Leave it empty and the surface follows from whether REPL
	// is declared — see InjectedBuiltin.
	Direct []string `yaml:"direct,omitempty" json:"direct,omitempty"`
}

// replToolName is the code-execution tool. It is spelled here because this
// package cannot import the tool implementation that owns the constant.
const replToolName = "REPL"

// REPLCodeTools are the tools a program owns once REPL is declared. The set is a
// constant rather than a second config list because "which tools are code tools"
// is a property of the engine: an Agent that works through a program wants all of
// them in there, and repeating the list in every definition only creates a second
// place to get wrong.
var REPLCodeTools = []string{"Bash", "Read", "Write", "Grep", "Glob"}

// IsREPLCodeTool reports whether name is one of the tools a program owns.
func IsREPLCodeTool(name string) bool {
	for _, code := range REPLCodeTools {
		if code == name {
			return true
		}
	}
	return false
}

// DeclaresREPL reports whether the Agent works through a program. Declaring REPL
// is the whole switch: it moves REPLCodeTools into the sandbox, so a definition
// that wants that surface lists REPL and the tools that are NOT code work.
func (c ToolConfig) DeclaresREPL() bool {
	for _, name := range c.Builtin {
		if name == replToolName {
			return true
		}
	}
	return false
}

// InjectedBuiltin is the builtin list rendered into the model-facing schema.
// In order: Direct when set; otherwise Builtin minus REPLCodeTools when REPL is
// declared; otherwise Builtin unchanged. Every branch keeps one tool in exactly
// one place — a model that can call Bash directly has no reason to write a
// program that calls it, and vice versa.
func (c ToolConfig) InjectedBuiltin() []string {
	if len(c.Direct) > 0 {
		return c.Direct
	}
	if !c.DeclaresREPL() {
		return c.Builtin
	}
	out := make([]string, 0, len(c.Builtin))
	for _, name := range c.Builtin {
		if IsREPLCodeTool(name) {
			continue
		}
		out = append(out, name)
	}
	return out
}

// SandboxBuiltin is what a program may call. In order: Builtin minus Direct when
// Direct is set (the two lists complementary); otherwise all of REPLCodeTools when
// REPL is declared, listed or not — a definition that declares REPL is asking for
// them, and making the author repeat the set would be the second surface this
// design removes; otherwise nothing.
func (c ToolConfig) SandboxBuiltin() []string {
	if len(c.Direct) == 0 {
		if !c.DeclaresREPL() {
			return nil
		}
		return append([]string(nil), REPLCodeTools...)
	}
	direct := make(map[string]struct{}, len(c.Direct))
	for _, name := range c.Direct {
		direct[name] = struct{}{}
	}
	out := make([]string, 0, len(c.Builtin))
	for _, name := range c.Builtin {
		if _, injected := direct[name]; injected {
			continue
		}
		out = append(out, name)
	}
	return out
}

// CallableBuiltin is every builtin name the Agent may call: its sandbox tools
// included, because those are not free text — a program may only call what the
// executor allows. The execution allowlist and the Bash gate read this rather than
// Builtin, or a sandboxed tool would be advertised inside a program and refused at
// the executor, and a sandboxed Bash would slip past the knowledge gate.
func (c ToolConfig) CallableBuiltin() []string {
	sandbox := c.SandboxBuiltin()
	if len(sandbox) == 0 {
		return c.Builtin
	}
	seen := make(map[string]struct{}, len(c.Builtin)+len(sandbox))
	out := make([]string, 0, len(c.Builtin)+len(sandbox))
	for _, name := range append(append([]string(nil), c.Builtin...), sandbox...) {
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// HasBuiltin reports whether the Agent declared one builtin tool. Callers use
// it to decide whether an instruction about a tool applies at all: a state
// policy handed to an Agent without the State tool is an instruction it cannot
// follow.
func (c ToolConfig) HasBuiltin(name string) bool {
	for _, tool := range c.Builtin {
		if tool == name {
			return true
		}
	}
	return false
}

type LoopConfig struct {
	MaxRounds           int      `yaml:"max_rounds" json:"max_rounds"`
	ToolExecution       string   `yaml:"tool_execution,omitempty" json:"tool_execution,omitempty"` // sequential | parallel_safe
	MaxParallelTools    int      `yaml:"max_parallel_tools,omitempty" json:"max_parallel_tools,omitempty"`
	AsyncTools          []string `yaml:"async_tools,omitempty" json:"async_tools,omitempty"`
	MaxSameToolCalls    int      `yaml:"max_same_tool_calls,omitempty" json:"max_same_tool_calls,omitempty"`
	MaxNoProgressRounds int      `yaml:"max_no_progress_rounds,omitempty" json:"max_no_progress_rounds,omitempty"`
	MaxSameModelTexts   int      `yaml:"max_same_model_texts,omitempty" json:"max_same_model_texts,omitempty"`
	// MaxRepeatedReads bounds how many times one read-only call may return with
	// identical arguments before the turn is treated as stuck. Repeats that
	// follow a workspace write do not count: that is an edit-and-verify loop,
	// not a stall. Zero uses the built-in default.
	MaxRepeatedReads int    `yaml:"max_repeated_reads,omitempty" json:"max_repeated_reads,omitempty"`
	StuckAction      string `yaml:"stuck_action,omitempty" json:"stuck_action,omitempty"` // fail | ask_user
	// MaxModelRetries bounds retries for transient provider failures. Context
	// limit recovery is handled separately and is attempted once.
	MaxModelRetries int    `yaml:"max_model_retries,omitempty" json:"max_model_retries,omitempty"`
	Timeout         string `yaml:"timeout,omitempty" json:"timeout,omitempty"` // no longer honoured: kept so a stale config is rejected, not silently ignored
}

// CompletionConfig describes task-level completion evidence. All fields are
// opt-in so ordinary conversational Agents retain the current behavior.
type CompletionConfig struct {
	RequireTool             bool     `yaml:"require_tool,omitempty" json:"require_tool,omitempty"`
	RequiredTools           []string `yaml:"required_tools,omitempty" json:"required_tools,omitempty"`
	RequireToolSuccess      bool     `yaml:"require_tool_success,omitempty" json:"require_tool_success,omitempty"`
	RequireWorkspaceRead    bool     `yaml:"require_workspace_read,omitempty" json:"require_workspace_read,omitempty"`
	RequireWorkspaceChange  bool     `yaml:"require_workspace_change,omitempty" json:"require_workspace_change,omitempty"`
	RequireStructuredOutput bool     `yaml:"require_structured_output,omitempty" json:"require_structured_output,omitempty"`
}

// ContextConfig controls the Agent's active model context. Ratios are
// relative to the selected model's input context capacity. MaxInputTokens is
// an optional explicit capacity override for runtimes that do not expose the
// model profile to the Agent.
//
// Three numbers describe one axis — how much of the window may be sent, when to
// compact, and where compaction lands — and they must stay ordered:
//
//	TargetRatio < CompactionThreshold < 1 - OutputReserveRatio
//
// The hard limit is not a separate knob: it is exactly 1 - OutputReserveRatio,
// because that is the most that can be sent while still leaving room for the
// response. Keeping a second configurable ceiling for the same boundary only
// produced states where two limits silently collapsed onto one value.
type ContextConfig struct {
	MaxInputTokens      int     `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	TargetRatio         float64 `yaml:"target_ratio,omitempty" json:"target_ratio,omitempty"`
	CompactionThreshold float64 `yaml:"compaction_threshold,omitempty" json:"compaction_threshold,omitempty"`
	// HardLimitRatio is removed from the model. The field stays so an old
	// config can be rejected with a migration hint instead of being silently
	// ignored by the non-strict YAML decoder; the loader fails when it is set.
	HardLimitRatio             float64 `yaml:"hard_limit_ratio,omitempty" json:"hard_limit_ratio,omitempty"`
	OutputReserveRatio         float64 `yaml:"output_reserve_ratio,omitempty" json:"output_reserve_ratio,omitempty"`
	ToolOutputRatio            float64 `yaml:"tool_output_ratio,omitempty" json:"tool_output_ratio,omitempty"`
	MaxToolOutputChars         int     `yaml:"max_tool_output_chars,omitempty" json:"max_tool_output_chars,omitempty"`
	MicrocompactThresholdChars int     `yaml:"microcompact_threshold_chars,omitempty" json:"microcompact_threshold_chars,omitempty"`
	MicrocompactMaxChars       int     `yaml:"microcompact_max_chars,omitempty" json:"microcompact_max_chars,omitempty"`
	RecentMessageGroups        int     `yaml:"recent_message_groups,omitempty" json:"recent_message_groups,omitempty"`
	Compactor                  string  `yaml:"compactor,omitempty" json:"compactor,omitempty"` // "llm"(默认) | "mechanical"
}

// HardLimit returns the fraction of the window that may be sent, i.e. the hard
// limit. It is derived rather than configured so the "how much can I send" and
// "how much room is left for the answer" answers cannot disagree.
func (c ContextConfig) HardLimit() float64 {
	resolved := c.WithDefaults()
	reserve := resolved.OutputReserveRatio
	if reserve < 0 {
		return 0
	}
	if reserve >= 1 {
		return 0
	}
	return 1 - reserve
}

func (c ContextConfig) WithDefaults() ContextConfig {
	// Defaults follow what a coding harness measured in practice: compact late
	// (90% of the window) and compact hard (down to 45%), rather than compacting
	// early and often. Each compaction is one summarisation call plus a cache
	// reset, so the fixed cost per compaction dominates: freeing 10 points at a
	// time means paying that cost every few tool calls, and each pass loses
	// detail the model then has to re-read.
	if c.TargetRatio <= 0 || c.TargetRatio >= 1 {
		c.TargetRatio = 0.45
	}
	if c.CompactionThreshold <= 0 || c.CompactionThreshold >= 1 {
		c.CompactionThreshold = 0.90
	}
	if c.OutputReserveRatio < 0 || c.OutputReserveRatio >= 1 {
		c.OutputReserveRatio = 0.15
	}
	if c.ToolOutputRatio <= 0 || c.ToolOutputRatio >= 1 {
		c.ToolOutputRatio = 0.10
	}
	if c.MaxToolOutputChars < 0 {
		c.MaxToolOutputChars = 0
	}
	if c.MicrocompactThresholdChars <= 0 {
		c.MicrocompactThresholdChars = 8192
	}
	if c.MicrocompactMaxChars <= 0 {
		c.MicrocompactMaxChars = 4096
	}
	if c.MicrocompactMaxChars > c.MicrocompactThresholdChars {
		c.MicrocompactMaxChars = c.MicrocompactThresholdChars
	}
	if c.RecentMessageGroups <= 0 {
		c.RecentMessageGroups = 2
	}
	return c
}

type StructuredOutput struct {
	Type            string         `yaml:"type" json:"type"`
	Schema          map[string]any `yaml:"schema" json:"schema"`
	MaxOutputTokens int            `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	// EnforceProviderFormat opts back into sending response_format (and the
	// matching sampling overrides) on every loop round. Default off: JSON mode
	// suppresses tool_calls on some OpenAI-compatible gateways, and an agent
	// that must call tools before emitting its envelope would never call them
	// — it would fabricate "已派给…" inside the JSON instead.
	EnforceProviderFormat bool `yaml:"enforce_provider_format,omitempty" json:"enforce_provider_format,omitempty"`
}

type HITLConfig struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type HookConfig struct {
	Event   string `yaml:"event" json:"event"`
	Command string `yaml:"command" json:"command"`
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}
