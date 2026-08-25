package types

import (
	"context"
	"encoding/json"
)

// SubagentRequest is the explicit input to one AgentTurn executed by a
// subagent member. It contains the member responsibility and the
// collaboration context visible to the Agent. The business payload remains
// inside SharedRecord.Data.
type SubagentRequest struct {
	MemberID       string
	AgentID        string
	Responsibility string
	Input          string
	Records        []SharedRecord
	TeamMemory     string
	SubagentMemory string
	KnowledgeText  string
	SkillText      string
	RuleText       string
	Variables      map[string]string
	// MaxAgentRounds is the maximum number of model/tool loop iterations
	// allowed inside this one AgentTurn.
	MaxAgentRounds   int
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
	SkillText       string
	RuleText        string
	Variables       map[string]string
	MemberTurnID    string
	Attempt         int
	RecoveryOf      string
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
	MaxTeamTurns        int `json:"max_team_turns"`
	MaxCallsPerTeamTurn int `json:"max_calls_per_team_turn"`
	MaxAgentRounds      int `json:"max_agent_rounds"`
	MaxParallelTeams    int `json:"max_parallel_teams"`
	MaxParallelCalls    int `json:"max_parallel_calls"`
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
		LegacyMemberTurns     int `json:"max_member_turns"`
		LegacyToolCalls       int `json:"max_tool_calls"`
		LegacyParallelMembers int `json:"max_parallel_members"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*l = RuntimeLimits(raw.current)
	if l.MaxCallsPerTeamTurn <= 0 {
		l.MaxCallsPerTeamTurn = raw.LegacyMemberTurns
	}
	if l.MaxAgentRounds <= 0 {
		l.MaxAgentRounds = raw.LegacyToolCalls
	}
	if l.MaxParallelCalls <= 0 {
		l.MaxParallelCalls = raw.LegacyParallelMembers
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
