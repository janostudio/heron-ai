package types

import "time"

// EngineConfig is the optional global engine configuration. Flow, Team,
// Call, Session, and Turn definitions live in their own domain types.
type EngineConfig struct {
	Settings SettingsConfig    `json:"settings"`
	Models   []ProviderConfig  `json:"models"`
	MCP      []MCPServerConfig `json:"mcp,omitempty"`
	Runtime  RuntimeLimits     `json:"runtime,omitempty"`
}

type SettingsConfig struct {
	Logging       LoggingConfig       `json:"logging"`
	Observability ObservabilityConfig `json:"observability"`
	Paths         PathsConfig         `json:"paths"`
	Agent         AgentSettingsConfig `json:"agent"`
	Knowledge     KnowledgeConfig     `json:"knowledge,omitempty"`
}

// KnowledgeConfig 配置知识自动学习相关能力。
type KnowledgeConfig struct {
	// SummaryModel 为知识总结指定的模型名；空 = 用默认模型。
	SummaryModel string `json:"summary_model,omitempty"`
}

type LoggingConfig struct {
	Level         string `json:"level"`
	Output        string `json:"output"`
	Dir           string `json:"dir"`
	MaxFileSize   string `json:"max_file_size"`
	MaxBackups    int    `json:"max_backups"`
	RetentionDays int    `json:"retention_days"`
}

type ObservabilityConfig struct {
	RetentionDays int `json:"retention_days"`
	EventBusSize  int `json:"event_bus_size"`
}

type PathsConfig struct {
	Data string `json:"data"`
}

type AgentSettingsConfig struct {
	MaxParallel int               `json:"max_parallel"`
	MaxTeam     int               `json:"max_team"`
	Tracing     TracingConfig     `json:"tracing"`
	DefaultLoop DefaultLoopConfig `json:"default_loop"`
}

type TracingConfig struct {
	Enabled              bool    `json:"enabled"`
	SampleRate           float64 `json:"sample_rate"`
	IncludeSensitiveData bool    `json:"include_sensitive_data"`
}

type DefaultLoopConfig struct {
	MaxRounds     int    `json:"max_rounds"`
	Timeout       string `json:"timeout"`
	ToolExecution string `json:"tool_execution"`
	Streaming     bool   `json:"streaming"`
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

// SSEEvent is the view-layer event envelope. Runtime session events are
// persisted as SessionEvent; this smaller projection is only for streaming.
type SSEEvent struct {
	Seq     int    `json:"seq"`
	CallID  string `json:"call_id,omitempty"`
	Content string `json:"content,omitempty"`
	Type    string `json:"type"`
}

// GuardrailRule defines an input or output guardrail.
type GuardrailRule struct {
	Type    string `yaml:"type" json:"type"` // regex | contains
	Pattern string `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	Schema  string `yaml:"schema,omitempty" json:"schema,omitempty"`
	Message string `yaml:"message" json:"message"`
}

// HITLRequest represents a human approval request for a call tool call.
type HITLRequest struct {
	RequestID   string         `json:"request_id"`
	CallID      string         `json:"call_id"`
	CallType    CallType       `json:"call_type,omitempty"`
	ToolName    string         `json:"tool_name"`
	ToolArgs    map[string]any `json:"tool_args"`
	Reason      string         `json:"reason"`
	RequestedAt time.Time      `json:"requested_at,omitempty"`
	Channel     string         `json:"channel,omitempty"`
}

type HITLResponse struct {
	RequestID  string            `json:"request_id"`
	Approved   bool              `json:"approved"`
	Reason     string            `json:"reason,omitempty"`
	ApproverID string            `json:"approver_id,omitempty"`
	Approver   string            `json:"approver,omitempty"`
	Channel    string            `json:"channel,omitempty"`
	DecidedAt  time.Time         `json:"decided_at,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

// HookPayload identifies the current Flow/Team/Call execution boundary.
type HookPayload struct {
	FlowSessionID string         `json:"flow_session_id,omitempty"`
	FlowTurnID    string         `json:"flow_turn_id,omitempty"`
	TeamID        string         `json:"team_id,omitempty"`
	TeamTurnID    string         `json:"team_turn_id,omitempty"`
	CallID        string         `json:"call_id,omitempty"`
	CallType      CallType       `json:"call_type,omitempty"`
	AgentID       string         `json:"agent_id,omitempty"`
	AgentTurnID   string         `json:"agent_turn_id,omitempty"`
	Event         string         `json:"event"`
	Round         int            `json:"round,omitempty"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolCallID    string         `json:"tool_call_id,omitempty"`
	ToolArgs      map[string]any `json:"tool_args,omitempty"`
	ToolResult    *ToolResult    `json:"tool_result,omitempty"`
	Error         string         `json:"error,omitempty"`
}
