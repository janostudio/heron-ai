package types

import (
	"context"
	"encoding/json"
)

// AgentRequest is the explicit input to one AgentTurn executed by an Agent
// Call. It contains the call responsibility and the
// collaboration context visible to the Agent. The business payload remains
// inside SharedRecord.Data.
//
// No wire contract: this is an input struct, passed in-process from the call
// executor to TurnLoop.Run. Nothing serializes it and nothing embeds it, so
// these field names are free to change. If it ever starts being persisted,
// it becomes part of the published contract and needs the same treatment as
// CallResult below.
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
//
// No wire contract: AgentResult never reaches the event stream. It is an
// in-process handoff from TurnLoop to its caller, and every caller copies
// the fields it needs into CallResult (see internal/runtime/call/agent.go
// and internal/agent/spawn.go) or into a checkpoint. Nothing marshals this
// struct as a whole, so these field names are free to change.
//
// If you ever do persist it, it becomes part of the published contract and
// needs the same treatment as CallResult below.
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
//
// No wire contract: same as AgentRequest, this is an in-process input
// struct. Only selected fields are copied into event payloads, never the
// struct as a whole, so these field names are free to change. If it ever
// starts being persisted, it needs the same treatment as CallResult below.
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
	Limits             RuntimeLimits
}

// CallResult is the normalized result returned by an Agent, Command, or
// Webhook executor.
//
// Wire contract: CallResult is persisted verbatim as the `call_result`
// payload of `agent_turn.completed` / `command_turn.completed` /
// `webhook_turn.completed` events in team.jsonl, and nested inside the
// `team_result` payload's `CallResults` map. That event stream is the fact
// source external consumers read token usage from, so the JSON names below
// are a published contract, not an accident of Go naming.
//
// They deliberately stay in Go field name form (PascalCase). Two reasons:
//   - Every session jsonl already on disk uses these names. Renaming them
//     would split the stream into two dialects that external parsers must
//     both handle.
//   - encoding/json only falls back to case-insensitive matching. Names
//     that differ by more than case (`CallTurnID` vs `call_turn_id`) are
//     silently dropped on read, with no error.
//
// Do not "clean up" these tags into snake_case without a migration. The
// nested leaf types (TokenUsage, ModelRequestStats, SharedRecord) do use
// snake_case; that mixed shape is intentional and load-bearing.
type CallResult struct {
	Status          TurnStatus            `json:"Status"`
	Reply           string                `json:"Reply"`
	CallTurnID      string                `json:"CallTurnID"`
	AgentID         string                `json:"AgentID"`
	Records         []SharedRecord        `json:"Records"`
	Next            *Route                `json:"Next"`
	Usage           TokenUsage            `json:"Usage"`
	Requests        []ModelRequestStats   `json:"Requests"`
	WorkspaceOps    []WorkspaceOperation  `json:"WorkspaceOps"`
	ToolCalls       int                   `json:"ToolCalls"`
	Error           string                `json:"Error"`
	CheckpointID    string                `json:"CheckpointID"`
	Checkpoint      *AgentCheckpoint      `json:"Checkpoint"`
	TaskID          string                `json:"TaskID"`
	PendingApproval *AgentPendingApproval `json:"PendingApproval"`
	Approval        *HITLResponse         `json:"Approval"`
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
	// Model is the actual model name that produced this request, which may
	// differ from the configured primary when fallback was triggered.
	Model string `json:"model,omitempty"`
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
//
// Wire contract, same shape and same rules as CallResult: TeamTurnResult is
// persisted verbatim as the `team_result` payload of the team waiting
// events, and it embeds CallResult values in CallResults. The JSON names
// below stay in Go field name form (PascalCase) because every session jsonl
// on disk already uses them, and because encoding/json only falls back to
// case-insensitive matching — renaming `CallResults` or `PendingToolTasks`
// to snake_case would silently drop them on read, with no error, and break
// resume of interrupted Team turns.
//
// The nested leaf types (TeamTurn, SharedRecord, PendingToolTask,
// AgentPendingApproval, TokenUsage) do use snake_case. That mixed shape is
// intentional and load-bearing; see docs/context-management.md §6.4.
type TeamTurnResult struct {
	Turn             TeamTurn               `json:"Turn"`
	Reply            string                 `json:"Reply"`
	Records          []SharedRecord         `json:"Records"`
	CallResults      map[string]CallResult  `json:"CallResults"`
	PendingToolTasks []PendingToolTask      `json:"PendingToolTasks"`
	PendingApprovals []AgentPendingApproval `json:"PendingApprovals"`
	Usage            TokenUsage             `json:"Usage"`
	Next             *Route                 `json:"Next"`
	Error            string                 `json:"Error"`
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
//
// Two different wire surfaces, and it is important not to confuse them:
//
//   - Not part of the event stream contract. Unlike CallResult and
//     TeamTurnResult, this struct is never written to flow.jsonl / team.jsonl.
//     The flow layer emits its own payload fields, so these names carry no
//     jsonl compatibility obligation.
//   - But it IS an HTTP response contract. internal/view/handler.go encodes
//     it directly with json.NewEncoder in the start / handle / resume /
//     status / approval / recovery endpoints, so clients of the HTTP view
//     API do see these PascalCase names. Renaming a field is a breaking
//     change for those HTTP clients even though it is harmless for jsonl.
//
// The json tags below therefore freeze the HTTP response shape. They match
// the current Go field names exactly, so the bytes on the wire are
// unchanged; they exist to make the contract explicit rather than an
// accident of Go naming.
//
// Forward-looking hazard: FlowTurnResult currently has no Usage field. Flow
// level token usage is computed on the consumer side by summing
// TeamTurnResult.Usage, which does not include consumption from spawned
// child tasks. If a future change makes the flow layer aggregate usage from
// the event stream instead and puts it here, this struct stops being
// jsonl-invisible and its field names become part of the published event
// contract too. At that point re-check the frozen set: a new Usage field
// needs a tag before that change ships, not after.
type FlowTurnResult struct {
	Session          FlowSession            `json:"Session"`
	Turn             FlowTurn               `json:"Turn"`
	TeamResults      []TeamTurnResult       `json:"TeamResults"`
	PendingToolTasks []PendingToolTask      `json:"PendingToolTasks"`
	PendingApprovals []AgentPendingApproval `json:"PendingApprovals"`
	Records          []SharedRecord         `json:"Records"`
	Reply            string                 `json:"Reply"`
	Error            string                 `json:"Error"`
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
