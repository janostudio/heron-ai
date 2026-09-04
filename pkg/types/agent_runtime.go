package types

import (
	"context"
	"time"
)

// CurrentAgentCheckpointVersion is the on-disk Agent checkpoint schema
// version. Older versions remain readable when their compatibility metadata
// is absent; future versions must not be resumed silently.
const CurrentAgentCheckpointVersion = 2

// AgentBudget is the execution budget for one AgentTurn. Zero means use the
// runtime default for MaxModelRounds and no explicit limit for the other
// dimensions.
type AgentBudget struct {
	MaxModelRounds  int    `yaml:"max_model_rounds,omitempty" json:"max_model_rounds,omitempty"`
	MaxToolCalls    int    `yaml:"max_tool_calls,omitempty" json:"max_tool_calls,omitempty"`
	MaxWallTime     string `yaml:"max_wall_time,omitempty" json:"max_wall_time,omitempty"`
	MaxInputTokens  int    `yaml:"max_input_tokens,omitempty" json:"max_input_tokens,omitempty"`
	MaxOutputTokens int    `yaml:"max_output_tokens,omitempty" json:"max_output_tokens,omitempty"`
	MaxFileChanges  int    `yaml:"max_file_changes,omitempty" json:"max_file_changes,omitempty"`
	MaxToolOutput   int    `yaml:"max_tool_output,omitempty" json:"max_tool_output,omitempty"`
}

// AgentBudgetUsage is the cumulative usage recorded in a checkpoint.
type AgentBudgetUsage struct {
	ModelRounds  int       `json:"model_rounds"`
	ToolCalls    int       `json:"tool_calls"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	FileChanges  int       `json:"file_changes"`
	ToolOutput   int       `json:"tool_output"`
	StartedAt    time.Time `json:"started_at"`
}

// AgentPendingInput describes why an AgentTurn is waiting for user input.
type AgentPendingInput struct {
	Question    string   `json:"question,omitempty"`
	Options     []string `json:"options,omitempty"`
	Header      string   `json:"header,omitempty"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

type AgentPendingApproval struct {
	RequestID    string         `json:"request_id"`
	CallID       string         `json:"call_id,omitempty"`
	CheckpointID string         `json:"checkpoint_id,omitempty"`
	ToolCallID   string         `json:"tool_call_id"`
	ToolName     string         `json:"tool_name"`
	Arguments    map[string]any `json:"arguments,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	RequestedAt  time.Time      `json:"requested_at,omitempty"`
	Channel      string         `json:"channel,omitempty"`
}

type AgentCheckpointCompatibility struct {
	AgentConfigHash   string `json:"agent_config_hash,omitempty"`
	ModelFingerprint  string `json:"model_fingerprint,omitempty"`
	ToolSchemaHash    string `json:"tool_schema_hash,omitempty"`
	PromptVersion     string `json:"prompt_version,omitempty"`
	ContextPolicyHash string `json:"context_policy_hash,omitempty"`
}

type AgentPendingTool struct {
	TaskID     string `json:"task_id"`
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
}

// AgentLoopState is the minimal durable state needed to continue long
// AgentTurns without losing completion evidence or loop safety counters.
// The complete transcript remains in session history and the bounded active
// messages remain in AgentCheckpoint.Messages.
type AgentLoopState struct {
	LastToolSignature string   `json:"last_tool_signature,omitempty"`
	SameToolCalls     int      `json:"same_tool_calls,omitempty"`
	NoProgressRounds  int      `json:"no_progress_rounds,omitempty"`
	SameModelTexts    int      `json:"same_model_texts,omitempty"`
	UsedTools         []string `json:"used_tools,omitempty"`
	SuccessfulTools   []string `json:"successful_tools,omitempty"`
}

// PendingToolTask is the Team-level view of an Agent checkpoint waiting for
// an asynchronous Tool. A Team may have more than one of these at the same
// time when independent Agent Calls are executed in parallel.
type PendingToolTask struct {
	CallID       string     `json:"call_id"`
	CallTurnID   string     `json:"call_turn_id,omitempty"`
	AgentID      string     `json:"agent_id,omitempty"`
	AgentTurnID  string     `json:"agent_turn_id,omitempty"`
	TaskID       string     `json:"task_id"`
	CheckpointID string     `json:"checkpoint_id"`
	Status       TurnStatus `json:"status"`
}

// AgentCheckpoint is the resumable pointer for one AgentTurn. Messages are
// the bounded Active Context; the complete transcript remains in the session
// event stream.
type AgentCheckpoint struct {
	Version         int                          `json:"version"`
	ID              string                       `json:"id"`
	FlowSessionID   string                       `json:"flow_session_id,omitempty"`
	TeamID          string                       `json:"team_id,omitempty"`
	TeamTurnID      string                       `json:"team_turn_id,omitempty"`
	CallID          string                       `json:"call_id,omitempty"`
	AgentID         string                       `json:"agent_id,omitempty"`
	AgentTurnID     string                       `json:"agent_turn_id,omitempty"`
	Attempt         int                          `json:"attempt,omitempty"`
	RecoveryOf      string                       `json:"recovery_of,omitempty"`
	Status          TurnStatus                   `json:"status"`
	Error           string                       `json:"error,omitempty"`
	NextRound       int                          `json:"next_round"`
	LastText        string                       `json:"last_text,omitempty"`
	Messages        []Message                    `json:"messages,omitempty"`
	Usage           TokenUsage                   `json:"usage"`
	BudgetUsage     AgentBudgetUsage             `json:"budget_usage"`
	WorkspaceOps    []WorkspaceOperation         `json:"workspace_ops,omitempty"`
	PendingInput    *AgentPendingInput           `json:"pending_input,omitempty"`
	PendingApproval *AgentPendingApproval        `json:"pending_approval,omitempty"`
	PendingTool     *AgentPendingTool            `json:"pending_tool,omitempty"`
	LoopState       AgentLoopState               `json:"loop_state,omitempty"`
	Compatibility   AgentCheckpointCompatibility `json:"compatibility,omitempty"`
	CreatedAt       time.Time                    `json:"created_at"`
	UpdatedAt       time.Time                    `json:"updated_at"`
}

type ToolTaskStatus string

const (
	ToolTaskQueued    ToolTaskStatus = "queued"
	ToolTaskRunning   ToolTaskStatus = "running"
	ToolTaskCompleted ToolTaskStatus = "completed"
	ToolTaskFailed    ToolTaskStatus = "failed"
	ToolTaskCancelled ToolTaskStatus = "cancelled"
)

type ToolTask struct {
	Version       int            `json:"version"`
	ID            string         `json:"id"`
	FlowSessionID string         `json:"flow_session_id,omitempty"`
	TeamID        string         `json:"team_id,omitempty"`
	TeamTurnID    string         `json:"team_turn_id,omitempty"`
	CallTurnID    string         `json:"call_turn_id,omitempty"`
	AgentTurnID   string         `json:"agent_turn_id,omitempty"`
	CallID        string         `json:"call_id,omitempty"`
	ToolCallID    string         `json:"tool_call_id"`
	ToolName      string         `json:"tool_name"`
	Arguments     map[string]any `json:"arguments,omitempty"`
	Status        ToolTaskStatus `json:"status"`
	Progress      float64        `json:"progress,omitempty"`
	Message       string         `json:"message,omitempty"`
	Result        *ToolResult    `json:"result,omitempty"`
	Error         string         `json:"error,omitempty"`
	RestartSafe   bool           `json:"restart_safe,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type ToolTaskStore interface {
	Save(ctx context.Context, task ToolTask) error
	Load(ctx context.Context, id string) (*ToolTask, error)
	List(ctx context.Context) ([]ToolTask, error)
	Delete(ctx context.Context, id string) error
}

// ToolTaskSubscriber is an optional live update stream for a durable task.
// Implementations should publish the current task after subscription and
// never make task execution wait for a slow subscriber.
type ToolTaskSubscriber interface {
	Subscribe(ctx context.Context, id string) (<-chan ToolTask, error)
}

// ToolTaskProgressUpdater is an optional externally-driven progress hook for
// long-running Tools. The durable task remains the source of truth.
type ToolTaskProgressUpdater interface {
	UpdateProgress(ctx context.Context, id string, progress float64, message string) error
}

type ToolTaskCanceller interface {
	Cancel(ctx context.Context, id string) error
}

// AgentCheckpointStore persists checkpoints independently from the Agent
// loop. It can be backed by files, a database, or an in-memory test store.
type AgentCheckpointStore interface {
	Save(ctx context.Context, checkpoint AgentCheckpoint) error
	Load(ctx context.Context, id string) (*AgentCheckpoint, error)
	List(ctx context.Context) ([]AgentCheckpoint, error)
	Delete(ctx context.Context, id string) error
}
