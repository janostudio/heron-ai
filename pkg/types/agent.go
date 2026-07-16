package types

type AgentConfig struct {
	Name        string            `yaml:"name" json:"name"`
	Persona     PersonaConfig     `yaml:"persona" json:"persona"`
	Model       ModelConfig       `yaml:"model" json:"model"`
	Tools       ToolConfig        `yaml:"tools" json:"tools"`
	Skills      []string          `yaml:"skills" json:"skills"`
	Knowledge   []string          `yaml:"knowledge" json:"knowledge"`
	Rules       []string          `yaml:"rules" json:"rules"`
	Loop        LoopConfig        `yaml:"loop" json:"loop"`
	Structured  *StructuredOutput `yaml:"structured_output,omitempty" json:"structured_output,omitempty"`
	HITL        *HITLConfig       `yaml:"hitl,omitempty" json:"hitl,omitempty"`
	Hooks       []HookConfig      `yaml:"hooks,omitempty" json:"hooks,omitempty"`
	Handoffs    []string          `yaml:"handoffs,omitempty" json:"handoffs,omitempty"`
	Body        string            `yaml:"-" json:"body"`
}

type PersonaConfig struct {
	Role      string `yaml:"role" json:"role"`
	Goal      string `yaml:"goal" json:"goal"`
	Backstory string `yaml:"backstory" json:"backstory"`
}

type ModelConfig struct {
	Provider    string  `yaml:"provider" json:"provider"`
	Model       string  `yaml:"model" json:"model"`
	Temperature float64 `yaml:"temperature" json:"temperature"`
	MaxTokens   int     `yaml:"max_tokens,omitempty" json:"max_tokens,omitempty"`
	APIKey      string  `yaml:"api_key,omitempty" json:"api_key,omitempty"`
	BaseURL     string  `yaml:"base_url,omitempty" json:"base_url,omitempty"`
}

type ToolConfig struct {
	Builtin []string `yaml:"builtin" json:"builtin"`
	Custom  []string `yaml:"custom,omitempty" json:"custom,omitempty"`
	MCP     []string `yaml:"mcp,omitempty" json:"mcp,omitempty"`
}

type LoopConfig struct {
	MaxRounds int    `yaml:"max_rounds" json:"max_rounds"`
	ToolMode  string `yaml:"tool_mode" json:"tool_mode"` // sequential | parallel
	Timeout   string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
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
