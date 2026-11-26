package types

import "time"

// EventHeader is the common envelope shared by all three layer event streams.
// The Seq is globally monotonic per FlowSession (allocated by the storage
// layer), so events from flow.jsonl / team.jsonl / agent.jsonl can be
// re-ordered into a single timeline when needed.
//
// The id fields keep their internal names (flow_session_id / flow_turn_id):
// they identify layer objects whose Go types are FlowSession / FlowTurn, and
// renaming the fields would touch every producer and consumer for no wire
// benefit. The event `type` values, by contrast, are the public vocabulary and
// live in the `session.` namespace below.
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
//
// Every event lives in the `session.` namespace; the second segment names the
// object and the last one the action. Actions are past tense: an event records
// something that already happened, not a state to be reached.
const (
	EventFlowSessionCreated = "session.created"
	EventFlowSessionUpdated = "session.updated"
	EventFlowTurnStarted    = "session.turn.started"
	// EventFlowTurnEnded is the single turn-terminal event: how the turn ended
	// (stop_reason) and who it is waiting for (awaiting) are separate fields in
	// the payload, so a consumer never has to map event name to status.
	EventFlowTurnEnded   = "session.turn.ended"
	EventTeamTurnStarted = "session.team_turn.started"
	// EventTeamTurnEnded mirrors EventFlowTurnEnded one level down.
	EventTeamTurnEnded         = "session.team_turn.ended"
	EventSharedRecordPublished = "session.record.published"
	// EventSharedRecordConsumed records that a Team is being handed an existing
	// record. It exists so a display layer can say which Team uses which
	// production ("B → A") without duplicating the content: the production is
	// shown once, at its producer, and every use is an edge.
	EventSharedRecordConsumed = "session.record.consumed"
	// EventUserInputInjected records that a message the user sent while a turn
	// was running was handed to that turn instead of waiting for the next one.
	// The mailbox's own delivered event follows it; this one carries the text so
	// a reader can see what the model was told, and when.
	EventUserInputInjected = "session.user_input.injected"
	EventRecoveryRequested = "session.recovery.requested"
	EventRecoveryCompleted = "session.recovery.completed"

	// Mailbox events (also recorded in flow.jsonl).
	EventMailboxMessageEnqueued  = "session.mailbox.message.enqueued"
	EventMailboxMessageClaimed   = "session.mailbox.message.claimed"
	EventMailboxMessageDelivered = "session.mailbox.message.delivered"
	EventMailboxMessageDead      = "session.mailbox.message.dead"
	EventMailboxBatchDispatched  = "session.mailbox.batch.dispatched"

	// Self-scheduled wake-ups (also recorded in flow.jsonl). The payload
	// carries the whole WakeupRecord, so replay can rebuild the pending set
	// without a second store. A wake-up performs no work, so these are the
	// only states it has.
	EventWakeupCreated   = "session.wakeup.created"
	EventWakeupFired     = "session.wakeup.fired"
	EventWakeupSkipped   = "session.wakeup.skipped"
	EventWakeupCancelled = "session.wakeup.cancelled"
	EventWakeupExpired   = "session.wakeup.expired"
)

// Team event types (team.jsonl).
const (
	EventTeamSessionCreated       = "session.team_session.created"
	EventTeamSessionUpdated       = "session.team_session.updated"
	EventAgentSessionCreated      = "session.agent_session.created"
	EventAgentSessionUpdated      = "session.agent_session.updated"
	EventAgentTurnStarted         = "session.agent_turn.started"
	EventAgentTurnCompleted       = "session.agent_turn.completed"
	EventAgentTurnWaitingInput    = "session.agent_turn.idle"
	EventAgentTurnWaitingTool     = "session.agent_turn.waiting_tool"
	EventAgentTurnWaitingApproval = "session.agent_turn.waiting_approval"
	EventApprovalRequested        = "session.approval.requested"
	EventApprovalResolved         = "session.approval.resolved"
	EventCommandTurnStarted       = "session.command_turn.started"
	EventCommandTurnCompleted     = "session.command_turn.completed"
	EventWebhookTurnStarted       = "session.webhook_turn.started"
	EventWebhookTurnCompleted     = "session.webhook_turn.completed"
)

// Agent event types (agent.jsonl).
const (
	EventAgentModelResponse = "session.agent.model_response"
	EventAgentFeedback      = "session.agent.feedback"
	EventToolCallStarted    = "session.tool_call.started"
	EventToolCallCompleted  = "session.tool_call.completed"
	EventToolCallFailed     = "session.tool_call.failed"
	EventContextCompacted   = "session.context.compacted"
	// EventRuleInjected records that a conditional rule entered this turn's
	// context because a tool touched a matching path. The rule body is not in the
	// payload: it is already in agent.jsonl's request, and repeating it here
	// would double the size of the event for a reader who has both files.
	EventRuleInjected = "session.rule.injected"
)
