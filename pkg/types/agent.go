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
}

type LoopConfig struct {
	MaxRounds           int      `yaml:"max_rounds" json:"max_rounds"`
	ToolExecution       string   `yaml:"tool_execution,omitempty" json:"tool_execution,omitempty"` // sequential | parallel_safe
	MaxParallelTools    int      `yaml:"max_parallel_tools,omitempty" json:"max_parallel_tools,omitempty"`
	AsyncTools          []string `yaml:"async_tools,omitempty" json:"async_tools,omitempty"`
	MaxSameToolCalls    int      `yaml:"max_same_tool_calls,omitempty" json:"max_same_tool_calls,omitempty"`
	MaxNoProgressRounds int      `yaml:"max_no_progress_rounds,omitempty" json:"max_no_progress_rounds,omitempty"`
	MaxSameModelTexts   int      `yaml:"max_same_model_texts,omitempty" json:"max_same_model_texts,omitempty"`
	StuckAction         string   `yaml:"stuck_action,omitempty" json:"stuck_action,omitempty"` // fail | ask_user
	// MaxModelRetries bounds retries for transient provider failures. Context
	// limit recovery is handled separately and is attempted once.
	MaxModelRetries int    `yaml:"max_model_retries,omitempty" json:"max_model_retries,omitempty"`
	Timeout         string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
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
type ContextConfig struct {
	MaxInputTokens             int     `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	TargetRatio                float64 `yaml:"target_ratio,omitempty" json:"target_ratio,omitempty"`
	CompactionThreshold        float64 `yaml:"compaction_threshold,omitempty" json:"compaction_threshold,omitempty"`
	HardLimitRatio             float64 `yaml:"hard_limit_ratio,omitempty" json:"hard_limit_ratio,omitempty"`
	OutputReserveRatio         float64 `yaml:"output_reserve_ratio,omitempty" json:"output_reserve_ratio,omitempty"`
	ToolOutputRatio            float64 `yaml:"tool_output_ratio,omitempty" json:"tool_output_ratio,omitempty"`
	MaxToolOutputChars         int     `yaml:"max_tool_output_chars,omitempty" json:"max_tool_output_chars,omitempty"`
	MicrocompactThresholdChars int     `yaml:"microcompact_threshold_chars,omitempty" json:"microcompact_threshold_chars,omitempty"`
	MicrocompactMaxChars       int     `yaml:"microcompact_max_chars,omitempty" json:"microcompact_max_chars,omitempty"`
	RecentMessageGroups        int     `yaml:"recent_message_groups,omitempty" json:"recent_message_groups,omitempty"`
	Compactor                  string  `yaml:"compactor,omitempty" json:"compactor,omitempty"` // "model"(默认) | "mechanical"
}

func (c ContextConfig) WithDefaults() ContextConfig {
	if c.TargetRatio <= 0 || c.TargetRatio >= 1 {
		c.TargetRatio = 0.70
	}
	if c.CompactionThreshold <= 0 || c.CompactionThreshold >= 1 {
		c.CompactionThreshold = 0.80
	}
	if c.HardLimitRatio <= 0 || c.HardLimitRatio > 1 {
		c.HardLimitRatio = 0.90
	}
	if c.CompactionThreshold > c.HardLimitRatio {
		c.CompactionThreshold = c.HardLimitRatio
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
