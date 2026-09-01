package types

import (
	"context"
	"encoding/json"
)

// AgentRequest is the explicit input to one AgentTurn executed by an Agent
// Call. It contains the call responsibility and the
// collaboration context visible to the Agent. The business payload remains
// inside SharedRecord.Data.
type AgentRequest struct {
	FlowSessionID      string
	TeamID             string
	TeamTurnID         string
	CallID             string
	CallTurnID         string
	AgentID            string
	AgentTurnID        string
	Attempt            int
	RecoveryOf         string
	ResumeCheckpointID string
	ResumeTaskID       string
	ResumeApprovalID   string
	ResumeApproval     *HITLResponse
	ContextBlocks      []ContextBlock
	Variables          map[string]string
	// MaxAgentRounds is the maximum number of model/tool loop iterations
	// allowed inside this one AgentTurn.
	MaxAgentRounds   int
	MaxParallelTools int
}

// AgentResult is the result of one Agent execution.
type AgentResult struct {
	Status          TurnStatus
	Reply           string
	Parsed          any
	Next            *Route
	Usage           TokenUsage
	Requests        []ModelRequestStats
	WorkspaceOps    []WorkspaceOperation
	ToolCalls       int
	Error           string
	Checkpoint      *AgentCheckpoint
	TaskID          string
	PendingApproval *AgentPendingApproval
	Approval        *HITLResponse
}

// ContextBlock is a structured, bounded input unit before PromptRenderer
// converts it into model messages.
type ContextBlock struct {
	ID           string        `yaml:"id,omitempty" json:"id,omitempty"`
	Kind         string        `yaml:"kind" json:"kind"`
	Text         string        `yaml:"text" json:"text"`
	Parts        []ContentPart `yaml:"parts,omitempty" json:"parts,omitempty"`
	Source       string        `yaml:"source,omitempty" json:"source,omitempty"`
	Placement    string        `yaml:"placement,omitempty" json:"placement,omitempty"` // system | user
	Stability    string        `yaml:"stability,omitempty" json:"stability,omitempty"` // stable | semi_stable | dynamic
	Priority     int           `yaml:"priority,omitempty" json:"priority,omitempty"`
	MaxChars     int           `yaml:"max_chars,omitempty" json:"max_chars,omitempty"`
	Sensitive    bool          `yaml:"sensitive,omitempty" json:"sensitive,omitempty"`
	Compressible bool          `yaml:"compressible,omitempty" json:"compressible,omitempty"`
}

// CallRequest is the normalized input passed to one Team call executor.
type CallRequest struct {
	FlowSession        FlowSession
	FlowTurn           FlowTurn
	TeamSession        TeamSession
	TeamTurn           TeamTurn
	Call               Call
	AgentDefinition    *AgentConfig
	Input              string
	ContextBlocks      []ContextBlock
	Records            []SharedRecord
	Variables          map[string]string
	CallTurnID         string
	AgentTurnID        string
	Attempt            int
	RecoveryOf         string
	ResumeCheckpointID string
	ResumeTaskID       string
	ResumeApprovalID   string
	ResumeApproval     *HITLResponse
	WorkspaceRoot      string
	Limits             RuntimeLimits
}

// CallResult is the normalized result returned by an Agent, Command, or
// Webhook executor.
type CallResult struct {
	Status          TurnStatus
	Reply           string
	CallTurnID      string
	AgentID         string
	Records         []SharedRecord
	Next            *Route
	Usage           TokenUsage
	Requests        []ModelRequestStats
	WorkspaceOps    []WorkspaceOperation
	ToolCalls       int
	Error           string
	CheckpointID    string
	Checkpoint      *AgentCheckpoint
	TaskID          string
	PendingApproval *AgentPendingApproval
	Approval        *HITLResponse
}

// ModelRequestStats is a privacy-preserving summary of one request sent to a
// model provider. It records structure, hashes, local estimates, and provider
// usage without persisting the full prompt by default.
type ModelRequestStats struct {
	Round                 int        `json:"round"`
	MessageCount          int        `json:"message_count"`
	MediaPartCount        int        `json:"media_part_count,omitempty"`
	SystemChars           int        `json:"system_chars"`
	UserChars             int        `json:"user_chars"`
	AssistantChars        int        `json:"assistant_chars"`
	ToolMessageChars      int        `json:"tool_message_chars"`
	ToolSchemaCount       int        `json:"tool_schema_count"`
	EstimatedPromptTokens int        `json:"estimated_prompt_tokens"`
	PromptHash            string     `json:"prompt_hash,omitempty"`
	StablePrefixHash      string     `json:"stable_prefix_hash,omitempty"`
	ToolSchemaHash        string     `json:"tool_schema_hash,omitempty"`
	Compacted             bool       `json:"compacted,omitempty"`
	Usage                 TokenUsage `json:"usage"`
}

// CallExecutorProvider executes exactly one V1 call type.
type CallExecutorProvider interface {
	Type() CallType
	Execute(ctx context.Context, req CallRequest) (CallResult, error)
}

// TeamTurnRequest is the input to TeamRuntime.
type TeamTurnRequest struct {
	FlowSession          FlowSession
	FlowTurn             FlowTurn
	TeamSession          TeamSession
	TeamTurn             TeamTurn
	Binding              FlowTeamBinding
	Team                 Team
	Input                string
	ContextBlocks        []ContextBlock
	Records              []SharedRecord
	WorkspaceRoot        string
	Limits               RuntimeLimits
	ResumeCallID         string
	ResumeCheckpointID   string
	ResumeInput          string
	ResumeCallTurnID     string
	ResumeTaskID         string
	ResumeApprovalID     string
	ResumeApproval       *HITLResponse
	ResumeCompletedCalls []string
	ResumeResults        map[string]CallResult
	ResumeCalls          map[string]TeamCallResume
}

// TeamCallResume identifies the checkpoint that must be resumed for one
// waiting Agent Call. It is intentionally a map entry rather than a single
// field because a Team can wait on several asynchronous Agent Tools.
type TeamCallResume struct {
	CallTurnID   string        `json:"call_turn_id,omitempty"`
	CheckpointID string        `json:"checkpoint_id,omitempty"`
	TaskID       string        `json:"task_id,omitempty"`
	ApprovalID   string        `json:"approval_id,omitempty"`
	Approval     *HITLResponse `json:"approval,omitempty"`
}

// TeamTurnResult is the normalized result of one TeamTurn.
type TeamTurnResult struct {
	Turn             TeamTurn
	Reply            string
	Records          []SharedRecord
	CallResults      map[string]CallResult
	PendingToolTasks []PendingToolTask
	PendingApprovals []AgentPendingApproval
	Usage            TokenUsage
	Next             *Route
	Error            string
}

// TeamRuntime executes a TeamTurn.
type TeamRuntime interface {
	Run(ctx context.Context, req TeamTurnRequest) (TeamTurnResult, error)
}

// StartFlowRequest starts or addresses a FlowSession.
type StartFlowRequest struct {
	FlowID        string
	Input         string
	ContextBlocks []ContextBlock
}

// FlowTurnResult contains the user-visible result of one FlowTurn and the
// TeamTurns it caused.
type FlowTurnResult struct {
	Session          FlowSession
	Turn             FlowTurn
	TeamResults      []TeamTurnResult
	PendingToolTasks []PendingToolTask
	PendingApprovals []AgentPendingApproval
	Records          []SharedRecord
	Reply            string
	Error            string
}

// RuntimeLimits bounds one external FlowTurn. These are safety defaults, not
// a new orchestration concept: the flow may still choose any valid static or
// dynamic route within these bounds.
type RuntimeLimits struct {
	MaxTeamTurns         int `json:"max_team_turns"`
	MaxCallsPerTeamTurn  int `json:"max_calls_per_team_turn"`
	MaxAgentRounds       int `json:"max_agent_rounds"`
	MaxParallelTeams     int `json:"max_parallel_teams"`
	MaxParallelCalls     int `json:"max_parallel_calls"`
	MaxCoordinateRetries int `json:"max_coordinate_retries"`
	MaxActivationRetries int `json:"max_activation_retries"`
	// Tool parallelism is inside one AgentTurn and is not a Flow/Team
	// orchestration level.
	MaxParallelTools int `json:"max_parallel_tools"`
}

func (l RuntimeLimits) WithDefaults() RuntimeLimits {
	if l.MaxTeamTurns <= 0 {
		l.MaxTeamTurns = 20
	}
	if l.MaxCallsPerTeamTurn <= 0 {
		l.MaxCallsPerTeamTurn = 20
	}
	if l.MaxAgentRounds <= 0 {
		l.MaxAgentRounds = 200
	}
	if l.MaxParallelTeams <= 0 {
		l.MaxParallelTeams = 20
	}
	if l.MaxParallelCalls <= 0 {
		l.MaxParallelCalls = 20
	}
	if l.MaxCoordinateRetries <= 0 {
		l.MaxCoordinateRetries = 1
	}
	if l.MaxActivationRetries <= 0 {
		l.MaxActivationRetries = 1
	}
	if l.MaxParallelTools <= 0 {
		l.MaxParallelTools = 20
	}
	return l
}

// UnmarshalJSON keeps old settings readable while the public vocabulary uses
// Flow → Team → Agent/Command/Webhook. The old names are migration aliases,
// not part of the current configuration contract.
func (l *RuntimeLimits) UnmarshalJSON(data []byte) error {
	type current RuntimeLimits
	var raw struct {
		current
		LegacyCallTurns     int `json:"max_call_turns"`
		LegacyToolCalls     int `json:"max_tool_calls"`
		LegacyParallelCalls int `json:"max_parallel_calls"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*l = RuntimeLimits(raw.current)
	if l.MaxCallsPerTeamTurn <= 0 {
		l.MaxCallsPerTeamTurn = raw.LegacyCallTurns
	}
	if l.MaxAgentRounds <= 0 {
		l.MaxAgentRounds = raw.LegacyToolCalls
	}
	if l.MaxParallelCalls <= 0 {
		l.MaxParallelCalls = raw.LegacyParallelCalls
	}
	return nil
}

// FlowRuntime owns FlowSession and FlowTurn lifecycle.
type FlowRuntime interface {
	Start(ctx context.Context, req StartFlowRequest) (FlowTurnResult, error)
	HandleInput(ctx context.Context, sessionID string, input string) (FlowTurnResult, error)
	Resume(ctx context.Context, sessionID string, input string) (FlowTurnResult, error)
	Cancel(ctx context.Context, sessionID string) error
	Status(ctx context.Context, sessionID string) (FlowSession, error)
}

// RichFlowRuntime is an optional transport extension for structured content.
// The compatibility-oriented FlowRuntime interface remains string-based.
type RichFlowRuntime interface {
	HandleInputWithContext(ctx context.Context, sessionID, input string, blocks []ContextBlock) (FlowTurnResult, error)
	ResumeWithContext(ctx context.Context, sessionID, input string, blocks []ContextBlock) (FlowTurnResult, error)
}

type ApprovalFlowRuntime interface {
	ResumeApproval(ctx context.Context, sessionID, approvalID string, approved bool, reason string) (FlowTurnResult, error)
}

// AuditableApprovalFlowRuntime accepts the complete approval decision,
// including approver identity and transport metadata. The older
// ApprovalFlowRuntime remains as a compatibility adapter.
type AuditableApprovalFlowRuntime interface {
	ResumeApprovalWithResponse(ctx context.Context, sessionID string, decision HITLResponse) (FlowTurnResult, error)
}
