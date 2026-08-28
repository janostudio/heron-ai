package types

import "time"

// SessionEvent is the append-only event envelope stored in session.jsonl.
// Payload is deliberately open because Session replay is responsible for
// interpreting event types, while the storage layer only guarantees ordering
// and durability.
type SessionEvent struct {
	Seq           int64          `json:"seq"`
	EventID       string         `json:"event_id"`
	Type          string         `json:"type"`
	FlowSessionID string         `json:"flow_session_id,omitempty"`
	FlowTurnID    string         `json:"flow_turn_id,omitempty"`
	TeamSessionID string         `json:"team_session_id,omitempty"`
	TeamTurnID    string         `json:"team_turn_id,omitempty"`
	TeamID        string         `json:"team_id,omitempty"`
	CallID        string         `json:"call_id,omitempty"`
	CallTurnID    string         `json:"call_turn_id,omitempty"`
	CallType      CallType       `json:"call_type,omitempty"`
	Attempt       int            `json:"attempt,omitempty"`
	RecoveryOf    string         `json:"recovery_of,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	Payload       map[string]any `json:"payload,omitempty"`
}

const (
	EventFlowSessionCreated       = "flow_session.created"
	EventFlowSessionUpdated       = "flow_session.updated"
	EventFlowTurnStarted          = "flow_turn.started"
	EventFlowTurnCompleted        = "flow_turn.completed"
	EventFlowTurnWaitingInput     = "flow_turn.waiting_input"
	EventFlowTurnWaitingTool      = "flow_turn.waiting_tool"
	EventFlowTurnWaitingApproval  = "flow_turn.waiting_approval"
	EventTeamSessionCreated       = "team_session.created"
	EventTeamSessionUpdated       = "team_session.updated"
	EventTeamTurnStarted          = "team_turn.started"
	EventTeamTurnCompleted        = "team_turn.completed"
	EventTeamTurnWaitingInput     = "team_turn.waiting_input"
	EventTeamTurnWaitingTool      = "team_turn.waiting_tool"
	EventTeamTurnWaitingApproval  = "team_turn.waiting_approval"
	EventAgentSessionCreated      = "agent_session.created"
	EventAgentSessionUpdated      = "agent_session.updated"
	EventAgentTurnStarted         = "agent_turn.started"
	EventAgentTurnCompleted       = "agent_turn.completed"
	EventAgentTurnWaitingInput    = "agent_turn.waiting_input"
	EventAgentTurnWaitingTool     = "agent_turn.waiting_tool"
	EventAgentTurnWaitingApproval = "agent_turn.waiting_approval"
	EventApprovalRequested        = "approval.requested"
	EventApprovalResolved         = "approval.resolved"
	EventAgentCheckpointSaved     = "agent_checkpoint.saved"
	EventAgentCheckpointDeleted   = "agent_checkpoint.deleted"
	EventCommandTurnStarted       = "command_turn.started"
	EventCommandTurnCompleted     = "command_turn.completed"
	EventWebhookTurnStarted       = "webhook_turn.started"
	EventWebhookTurnCompleted     = "webhook_turn.completed"
	EventToolCallQueued           = "tool_call.queued"
	EventToolCallStarted          = "tool_call.started"
	EventToolCallCompleted        = "tool_call.completed"
	EventToolCallFailed           = "tool_call.failed"
	EventWorkspaceOperation       = "workspace.operation"
	EventRouteResolved            = "route.resolved"
	EventSharedRecordPublished    = "shared_record.published"
	EventRecoveryRequested        = "recovery.requested"
	EventRecoveryCompleted        = "recovery.completed"
	EventMemoryUpdated            = "memory.updated"
)
