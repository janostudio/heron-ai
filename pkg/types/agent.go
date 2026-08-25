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
	MaxRounds        int    `yaml:"max_rounds" json:"max_rounds"`
	ToolExecution    string `yaml:"tool_execution,omitempty" json:"tool_execution,omitempty"` // sequential | parallel_safe
	MaxParallelTools int    `yaml:"max_parallel_tools,omitempty" json:"max_parallel_tools,omitempty"`
	Timeout          string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

type StructuredOutput struct {
	Type   string         `yaml:"type" json:"type"`
	Schema map[string]any `yaml:"schema" json:"schema"`
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
