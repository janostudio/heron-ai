package types

import "context"

// SubagentRequest is the explicit input to one SubagentTurn. It contains the
// member responsibility and the collaboration context visible to the
// Subagent. The business payload remains inside SharedRecord.Data.
type SubagentRequest struct {
	MemberID         string
	AgentID          string
	Responsibility   string
	Input            string
	Records          []SharedRecord
	TeamMemory       string
	SubagentMemory   string
	KnowledgeText    string
	Variables        map[string]string
	MaxToolCalls     int
	MaxParallelTools int
}

// SubagentResult is the result of one model member execution.
type SubagentResult struct {
	Reply        string
	Parsed       any
	Next         *Route
	Usage        TokenUsage
	WorkspaceOps []WorkspaceOperation
	ToolCalls    int
	Error        string
}

// MemberRequest is the normalized input passed to one Team member executor.
type MemberRequest struct {
	FlowSession     FlowSession
	FlowTurn        FlowTurn
	TeamSession     TeamSession
	TeamTurn        TeamTurn
	Member          Member
	AgentDefinition *AgentConfig
	Input           string
	Records         []SharedRecord
	TeamMemory      string
	SubagentMemory  string
	KnowledgeText   string
	Variables       map[string]string
	MemberTurnID    string
	WorkspaceRoot   string
	Limits          RuntimeLimits
}

// MemberResult is the normalized result returned by a Subagent, Command, or
// Webhook executor.
type MemberResult struct {
	Status       TurnStatus
	Reply        string
	Records      []SharedRecord
	Next         *Route
	Usage        TokenUsage
	WorkspaceOps []WorkspaceOperation
	ToolCalls    int
	Error        string
}

// MemberExecutorProvider executes exactly one V1 member type.
type MemberExecutorProvider interface {
	Type() MemberType
	Execute(ctx context.Context, req MemberRequest) (MemberResult, error)
}

// TeamTurnRequest is the input to TeamRuntime.
type TeamTurnRequest struct {
	FlowSession   FlowSession
	FlowTurn      FlowTurn
	TeamSession   TeamSession
	TeamTurn      TeamTurn
	Binding       FlowTeamBinding
	Team          Team
	Input         string
	Records       []SharedRecord
	WorkspaceRoot string
	Limits        RuntimeLimits
}

// TeamTurnResult is the normalized result of one TeamTurn.
type TeamTurnResult struct {
	Turn          TeamTurn
	Reply         string
	Records       []SharedRecord
	MemberResults map[string]MemberResult
	Usage         TokenUsage
	Next          *Route
	Error         string
}

// TeamRuntime executes a TeamTurn.
type TeamRuntime interface {
	Run(ctx context.Context, req TeamTurnRequest) (TeamTurnResult, error)
}

// StartFlowRequest starts or addresses a FlowSession.
type StartFlowRequest struct {
	FlowID string
	Input  string
}

// FlowTurnResult contains the user-visible result of one FlowTurn and the
// TeamTurns it caused.
type FlowTurnResult struct {
	Session     FlowSession
	Turn        FlowTurn
	TeamResults []TeamTurnResult
	Records     []SharedRecord
	Reply       string
	Error       string
}

// RuntimeLimits bounds one external FlowTurn. These are safety defaults, not
// a new orchestration concept: the flow may still choose any valid static or
// dynamic route within these bounds.
type RuntimeLimits struct {
	MaxTeamTurns       int `json:"max_team_turns"`
	MaxMemberTurns     int `json:"max_member_turns"`
	MaxToolCalls       int `json:"max_tool_calls"`
	MaxParallelMembers int `json:"max_parallel_members"`
	MaxParallelTools   int `json:"max_parallel_tools"`
}

func (l RuntimeLimits) WithDefaults() RuntimeLimits {
	if l.MaxTeamTurns <= 0 {
		l.MaxTeamTurns = 20
	}
	if l.MaxMemberTurns <= 0 {
		l.MaxMemberTurns = 20
	}
	if l.MaxToolCalls <= 0 {
		l.MaxToolCalls = 200
	}
	if l.MaxParallelMembers <= 0 {
		l.MaxParallelMembers = 20
	}
	if l.MaxParallelTools <= 0 {
		l.MaxParallelTools = 20
	}
	return l
}

// FlowRuntime owns FlowSession and FlowTurn lifecycle.
type FlowRuntime interface {
	Start(ctx context.Context, req StartFlowRequest) (FlowTurnResult, error)
	HandleInput(ctx context.Context, sessionID string, input string) (FlowTurnResult, error)
	Resume(ctx context.Context, sessionID string, input string) (FlowTurnResult, error)
	Cancel(ctx context.Context, sessionID string) error
	Status(ctx context.Context, sessionID string) (FlowSession, error)
}
