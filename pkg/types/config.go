package types

import (
	"strings"
	"time"
)

// EngineConfig is the optional global engine configuration. Flow, Team,
// Call, Session, and Turn definitions live in their own domain types.
type EngineConfig struct {
	Settings SettingsConfig    `json:"settings"`
	Models   []ProviderConfig  `json:"models"`
	MCP      []MCPServerConfig `json:"mcp,omitempty"`
	Runtime  RuntimeLimits     `json:"runtime,omitempty"`
}

type SettingsConfig struct {
	Logging   LoggingConfig       `json:"logging"`
	Paths     PathsConfig         `json:"paths"`
	Agent     AgentSettingsConfig `json:"agent"`
	Knowledge KnowledgeConfig     `json:"knowledge,omitempty"`
	// Env 是注入给所有子进程（command call、Bash 工具、MCP stdio server、
	// hooks、codenav）的全局环境变量。值里出现相对路径时按配置目录解析
	// （与 internal/logging 的 dir 同语义）。
	Env map[string]string `json:"env,omitempty"`
	// Web 配置内置 WebSearch / WebFetch 两个工具的数据源（settings.json 的
	// web 段）。空 = 用默认 provider（duckduckgo）。
	Web WebConfig `json:"web,omitempty"`
}

// KnowledgeConfig 配置知识自动学习相关能力。
type KnowledgeConfig struct {
	// SummaryModel 为知识总结指定的模型名；空 = 用默认模型。
	SummaryModel string `json:"summary_model,omitempty"`
}

// Web 工具支持的 provider。
const (
	// WebProviderTinyFish 提供 search / fetch，需要 API Key。
	WebProviderTinyFish = "tinyfish"
	WebProviderBrave    = "brave"
	// WebProviderDuckDuckGo 免 key：它是 Instant Answer API 而非网页搜索，
	// 只回答百科 / 实体类查询，但零配置可用，所以是全链路的默认 provider。
	WebProviderDuckDuckGo = "duckduckgo"
	// WebProviderTavily 提供 search / fetch，需要 API Key。
	WebProviderTavily = "tavily"
	// WebProviderDirect 只用于 fetch：表示本地直连抓取，不经任何外部 API。
	WebProviderDirect = "direct"
)

// WebConfig 配置内置 WebSearch / WebFetch 工具的数据源。
//
// Provider 决定默认的 search / fetch endpoint；Search.URL、Fetch.URL 可以逐项
// 覆盖。APIKey 由 search 与 fetch 共用（同一个 provider 的同一个 key）。
type WebConfig struct {
	// Provider 为 tinyfish | brave | duckduckgo | tavily；空 = duckduckgo。
	Provider string          `json:"provider,omitempty"`
	APIKey   string          `json:"api_key,omitempty"`
	Search   WebSearchConfig `json:"search,omitempty"`
	Fetch    WebFetchConfig  `json:"fetch,omitempty"`
}

// FetchProvider 返回 fetch 实际生效的 provider：Fetch.Provider 为空时跟随
// Provider。这样整段换 provider 时不必在 fetch 里重复写一遍，又允许「search
// 走 TinyFish、fetch 强制本地直连」这种组合。
func (c WebConfig) FetchProvider() string {
	if strings.TrimSpace(c.Fetch.Provider) == "" {
		return c.Provider
	}
	return c.Fetch.Provider
}

type WebSearchConfig struct {
	// URL 为完整 endpoint；空 = 取 provider 内置默认。
	URL string `json:"url,omitempty"`
	// MaxResults 为返回条数上限；空 = 10。
	MaxResults int `json:"max_results,omitempty"`
	// Location 为国家码，tinyfish 专有。
	Location string `json:"location,omitempty"`
	// Language 为语言码，tinyfish 专有。
	Language string `json:"language,omitempty"`
	// TimeoutMs 为超时毫秒；空 = 30000。
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

type WebFetchConfig struct {
	// Provider 为 tinyfish | direct；空 = 跟随 web.provider。想让 search 走
	// TinyFish 而 fetch 强制本地直连（离线环境、不想把 URL 发给第三方）时写
	// direct。默认值不在这里下发，否则「空 = 跟随」这一语义会被抹掉。
	Provider string `json:"provider,omitempty"`
	// URL 为完整 endpoint；空 = 取 provider 内置默认。
	URL string `json:"url,omitempty"`
	// Format 为 markdown | html | json；空 = markdown。
	Format string `json:"format,omitempty"`
	// TimeoutMs 为超时毫秒；空 = 45000。
	TimeoutMs int `json:"timeout_ms,omitempty"`
}

// DefaultWebSearchURL 返回 provider 内置的 search endpoint；未知 provider 返回空。
func DefaultWebSearchURL(provider string) string {
	switch provider {
	case WebProviderTinyFish:
		return "https://api.search.tinyfish.ai"
	case WebProviderBrave:
		return "https://api.search.brave.com/res/v1/web/search"
	case WebProviderDuckDuckGo:
		return "https://api.duckduckgo.com/"
	case WebProviderTavily:
		return "https://api.tavily.com/search"
	default:
		return ""
	}
}

// DefaultWebFetchURL 返回 provider 内置的 fetch endpoint；未知 provider 或该
// provider 不支持 fetch（brave / duckduckgo）返回空，由 tool 层处理。
func DefaultWebFetchURL(provider string) string {
	switch provider {
	case WebProviderTinyFish:
		return "https://api.fetch.tinyfish.ai"
	case WebProviderTavily:
		return "https://api.tavily.com/extract"
	default:
		return ""
	}
}

// WithDefaults 补齐 WebConfig 的空字段并返回新值，不修改入参。
//
// Format 的非法值保持原样：不在这里静默改成 markdown，留给 tool 层报错，避免
// 用户以为自己配错了值却仍然生效。
func (c WebConfig) WithDefaults() WebConfig {
	out := c
	if out.Provider == "" {
		// 默认 duckduckgo：免 key、零配置就能搜（开箱即用）。它没有 fetch API，
		// DefaultWebFetchURL 返回空，tool 层落成 direct（本地直连），fetch 同样
		// 可用。要真搜索就得显式配 tinyfish / brave / tavily 的 key。
		out.Provider = WebProviderDuckDuckGo
	}
	if out.Search.URL == "" {
		out.Search.URL = DefaultWebSearchURL(out.Provider)
	}
	// fetch.provider 留空表示「跟随 web.provider」，因此这里不能回填默认值，
	// 只在显式配置时按它取 endpoint。
	if out.Fetch.URL == "" {
		out.Fetch.URL = DefaultWebFetchURL(out.FetchProvider())
	}
	if out.Search.MaxResults <= 0 {
		out.Search.MaxResults = 10
	}
	if out.Search.TimeoutMs <= 0 {
		out.Search.TimeoutMs = 30000
	}
	if out.Fetch.TimeoutMs <= 0 {
		out.Fetch.TimeoutMs = 45000
	}
	if out.Fetch.Format == "" {
		out.Fetch.Format = "markdown"
	}
	return out
}

type LoggingConfig struct {
	Level         string `json:"level"`
	Output        string `json:"output"`
	Dir           string `json:"dir"`
	MaxFileSize   string `json:"max_file_size"`
	MaxBackups    int    `json:"max_backups"`
	RetentionDays int    `json:"retention_days"`
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
