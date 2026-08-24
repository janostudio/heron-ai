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
	MemberID      string         `json:"member_id,omitempty"`
	MemberTurnID  string         `json:"member_turn_id,omitempty"`
	MemberType    MemberType     `json:"member_type,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	Payload       map[string]any `json:"payload,omitempty"`
}

const (
	EventFlowSessionCreated     = "flow_session.created"
	EventFlowSessionUpdated     = "flow_session.updated"
	EventFlowTurnStarted        = "flow_turn.started"
	EventFlowTurnCompleted      = "flow_turn.completed"
	EventTeamSessionCreated     = "team_session.created"
	EventTeamSessionUpdated     = "team_session.updated"
	EventTeamTurnStarted        = "team_turn.started"
	EventTeamTurnCompleted      = "team_turn.completed"
	EventSubagentSessionCreated = "subagent_session.created"
	EventSubagentSessionUpdated = "subagent_session.updated"
	EventSubagentTurnStarted    = "subagent_turn.started"
	EventSubagentTurnCompleted  = "subagent_turn.completed"
	EventCommandTurnStarted     = "command_turn.started"
	EventCommandTurnCompleted   = "command_turn.completed"
	EventWebhookTurnStarted     = "webhook_turn.started"
	EventWebhookTurnCompleted   = "webhook_turn.completed"
	EventToolCallQueued         = "tool_call.queued"
	EventToolCallStarted        = "tool_call.started"
	EventToolCallCompleted      = "tool_call.completed"
	EventToolCallFailed         = "tool_call.failed"
	EventWorkspaceOperation     = "workspace.operation"
	EventRouteResolved          = "route.resolved"
	EventSharedRecordPublished  = "shared_record.published"
	EventMemoryUpdated          = "memory.updated"
)
