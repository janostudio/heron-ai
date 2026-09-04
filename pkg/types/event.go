package types

import "time"

// EventHeader is the common envelope shared by all three layer event streams.
// The Seq is globally monotonic per FlowSession (allocated by the storage
// layer), so events from flow.jsonl / team.jsonl / agent.jsonl can be
// re-ordered into a single timeline when needed.
type EventHeader struct {
	Seq           int64     `json:"seq"`
	EventID       string    `json:"event_id"`
	Type          string    `json:"type"`
	FlowSessionID string    `json:"flow_session_id,omitempty"`
	FlowTurnID    string    `json:"flow_turn_id,omitempty"`
	TeamSessionID string    `json:"team_session_id,omitempty"`
	TeamTurnID    string    `json:"team_turn_id,omitempty"`
	TeamID        string    `json:"team_id,omitempty"`
	CallID        string    `json:"call_id,omitempty"`
	CallTurnID    string    `json:"call_turn_id,omitempty"`
	CallType      CallType  `json:"call_type,omitempty"`
	Attempt       int       `json:"attempt,omitempty"`
	RecoveryOf    string    `json:"recovery_of,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Flow event types (flow.jsonl).
const (
	EventFlowSessionCreated      = "flow_session.created"
	EventFlowSessionUpdated      = "flow_session.updated"
	EventFlowTurnStarted         = "flow_turn.started"
	EventFlowTurnCompleted       = "flow_turn.completed"
	EventFlowTurnWaitingInput    = "flow_turn.waiting_input"
	EventFlowTurnWaitingTool     = "flow_turn.waiting_tool"
	EventFlowTurnWaitingApproval = "flow_turn.waiting_approval"
	EventTeamTurnStarted         = "team_turn.started"
	EventTeamTurnCompleted       = "team_turn.completed"
	EventTeamTurnWaitingInput    = "team_turn.waiting_input"
	EventTeamTurnWaitingTool     = "team_turn.waiting_tool"
	EventTeamTurnWaitingApproval = "team_turn.waiting_approval"
	EventSharedRecordPublished   = "shared_record.published"
	EventRecoveryRequested       = "recovery.requested"
	EventRecoveryCompleted       = "recovery.completed"
)

// Team event types (team.jsonl).
const (
	EventTeamSessionCreated       = "team_session.created"
	EventTeamSessionUpdated       = "team_session.updated"
	EventAgentSessionCreated      = "agent_session.created"
	EventAgentSessionUpdated      = "agent_session.updated"
	EventAgentTurnStarted         = "agent_turn.started"
	EventAgentTurnCompleted       = "agent_turn.completed"
	EventAgentTurnWaitingInput    = "agent_turn.waiting_input"
	EventAgentTurnWaitingTool     = "agent_turn.waiting_tool"
	EventAgentTurnWaitingApproval = "agent_turn.waiting_approval"
	EventApprovalRequested        = "approval.requested"
	EventApprovalResolved         = "approval.resolved"
	EventCommandTurnStarted       = "command_turn.started"
	EventCommandTurnCompleted     = "command_turn.completed"
	EventWebhookTurnStarted       = "webhook_turn.started"
	EventWebhookTurnCompleted     = "webhook_turn.completed"
)

// Agent event types (agent.jsonl).
const (
	EventAgentModelResponse = "agent.model_response"
	EventAgentFeedback      = "agent.feedback"
	EventToolCallStarted    = "tool_call.started"
	EventToolCallCompleted  = "tool_call.completed"
	EventToolCallFailed     = "tool_call.failed"
	EventContextCompacted   = "context.compacted"
)
