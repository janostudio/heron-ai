package types

// EngineConfig represents the global engine configuration
type EngineConfig struct {
	Settings SettingsConfig   `json:"settings"`
	Models   []ProviderConfig `json:"models"`
	MCP      []MCPServerConfig `json:"mcp,omitempty"`
}

type SettingsConfig struct {
	Logging       LoggingConfig       `json:"logging"`
	Observability ObservabilityConfig `json:"observability"`
	Paths         PathsConfig         `json:"paths"`
	Agent         AgentSettingsConfig `json:"agent"`
}

type LoggingConfig struct {
	Level       string `json:"level"`
	Output      string `json:"output"`
	MaxFileSize string `json:"max_file_size"`
	MaxBackups  int    `json:"max_backups"`
}

type ObservabilityConfig struct {
	RetentionDays int `json:"retention_days"`
	EventBusSize  int `json:"event_bus_size"`
}

type PathsConfig struct {
	Data string `json:"data"`
}

type AgentSettingsConfig struct {
	MaxParallel int              `json:"max_parallel"`
	MaxTeam     int              `json:"max_team"`
	Tracing     TracingConfig    `json:"tracing"`
	DefaultLoop DefaultLoopConfig `json:"default_loop"`
}

type TracingConfig struct {
	Enabled              bool    `json:"enabled"`
	SampleRate           float64 `json:"sample_rate"`
	IncludeSensitiveData bool    `json:"include_sensitive_data"`
}

type DefaultLoopConfig struct {
	MaxRounds int    `json:"max_rounds"`
	Timeout   string `json:"timeout"`
	ToolMode  string `json:"tool_mode"`
	Streaming bool   `json:"streaming"`
}

type ProviderConfig struct {
	Name    string        `json:"name"`
	Type    string        `json:"type"`
	BaseURL string        `json:"base_url"`
	APIKey  string        `json:"api_key"`
	Models  []ModelConfig `json:"models"`
}

type MCPServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// RunRequest represents a complete run configuration
type RunRequest struct {
	Flow      FlowConfig              `json:"flow"`
	Teams     map[string]TeamConfig   `json:"teams"`
	Agents    map[string]AgentConfig  `json:"agents"`
	Knowledge []KnowledgeEntry        `json:"knowledge,omitempty"`
	Rules     []RuleItem              `json:"rules,omitempty"`
	Prompts   map[string]string       `json:"prompts,omitempty"`
	Variables map[string]string       `json:"variables,omitempty"`
}

// RunState represents the state of a run
type RunState struct {
	RunID        string       `json:"run_id"`
	FlowPath     string       `json:"flow_path"`
	ConfigRoot   string       `json:"config_root"`
	RoundNum     int          `json:"round_num"`
	CurrentStage string       `json:"current_stage"`
	StageCount   int          `json:"stage_count"`
	Results      []TeamResult `json:"results"`
	Signal       Signal       `json:"signal"`
	Status       string       `json:"status"` // running | waiting | ended
	Input        string       `json:"input"`
	CreatedAt    string       `json:"created_at"`
	UpdatedAt    string       `json:"updated_at"`
}

// TeamResult represents the result of a team execution
type TeamResult struct {
	Content      string        `json:"content"`
	Signal       Signal        `json:"signal"`
	Raw          string        `json:"raw"`
	Usage        TokenUsage    `json:"usage"`
	Error        string        `json:"error,omitempty"`
	AgentResults []AgentResult `json:"agent_results,omitempty"`
}

// AgentResult represents the result of an agent execution
type AgentResult struct {
	Raw    string     `json:"raw"`
	Parsed any        `json:"parsed,omitempty"`
	Signal Signal     `json:"signal"`
	Usage  TokenUsage `json:"usage"`
	Error  string     `json:"error,omitempty"`
}

// Checkpoint represents a save point for run recovery
type Checkpoint struct {
	RunID     string   `json:"run_id"`
	RoundNum  int      `json:"round_num"`
	State     RunState `json:"state"`
	CreatedAt string   `json:"created_at"`
}

// SSEEvent represents a Server-Sent Event
type SSEEvent struct {
	Seq       int    `json:"seq"`
	AgentName string `json:"agent_name,omitempty"`
	Content   string `json:"content,omitempty"`
	Type      string `json:"type"`
}

// GuardrailRule defines an input/output guardrail
type GuardrailRule struct {
	Type    string `yaml:"type" json:"type"` // regex | schema
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Schema  string `yaml:"schema,omitempty" json:"schema,omitempty"`
	Message string `yaml:"message" json:"message"`
}

// HITLRequest represents a human-in-the-loop approval request
type HITLRequest struct {
	RequestID string         `json:"request_id"`
	AgentID   string         `json:"agent_id"`
	AgentName string         `json:"agent_name"`
	ToolName  string         `json:"tool_name"`
	ToolArgs  map[string]any `json:"tool_args"`
	Reason    string         `json:"reason"`
}

// HITLResponse represents a human-in-the-loop approval response
type HITLResponse struct {
	RequestID string `json:"request_id"`
	Approved  bool   `json:"approved"`
	Reason    string `json:"reason,omitempty"`
}

// HandoffContext represents context for agent handoff
type HandoffContext struct {
	Task    string    `json:"task"`
	Input   string    `json:"input"`
	History []Message `json:"history,omitempty"`
}

// HookPayload represents data passed to hook functions
type HookPayload struct {
	RunID      string         `json:"run_id"`
	RoundNum   int            `json:"round_num"`
	TeamName   string         `json:"team_name"`
	AgentName  string         `json:"agent_name"`
	Event      string         `json:"event"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolArgs   map[string]any `json:"tool_args,omitempty"`
	ToolResult *ToolResult    `json:"tool_result,omitempty"`
	HandoffTo  string         `json:"handoff_to,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// Skill represents a skill definition
type Skill struct {
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description"`
	Tools       []string `yaml:"tools" json:"tools"`
	Knowledge   []string `yaml:"knowledge,omitempty" json:"knowledge,omitempty"`
	Prompt      string   `yaml:"-" json:"prompt"`
	Body        string   `yaml:"-" json:"body"`
}

// SkillSummary represents a brief skill summary for discovery
type SkillSummary struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}
