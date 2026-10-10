package types

import "time"

// MailboxAddress identifies a mailbox recipient or sender. It is a leaf
// object, so its fields use snake_case json tags (published contract).
type MailboxAddress struct {
	TeamID  string `json:"team_id,omitempty"`
	AgentID string `json:"agent_id,omitempty"` // empty = team-level inbox
}

// MailboxMessageStatus is the lifecycle state of a mailbox message.
type MailboxMessageStatus string

const (
	MailboxPending   MailboxMessageStatus = "pending"   // enqueued, not yet claimed
	MailboxClaimed   MailboxMessageStatus = "claimed"   // claimed by a turn, executing
	MailboxDelivered MailboxMessageStatus = "delivered" // consumed and produced a result
	MailboxDead      MailboxMessageStatus = "dead"      // failed, retry limit exceeded
)

// MailboxMessage is a single mailbox message. It is a leaf object, so its
// fields use snake_case json tags (published contract). Refs reuses the
// existing ProducerRef (record.go) as the provenance anchor.
type MailboxMessage struct {
	MessageID       string               `json:"message_id"`
	FlowSessionID   string               `json:"flow_session_id"`
	From            MailboxAddress       `json:"from"`
	To              MailboxAddress       `json:"to"`
	Kind            string               `json:"kind"` // "work" | "result"
	Payload         map[string]any       `json:"payload,omitempty"`
	CauseFlowTurnID string               `json:"cause_flow_turn_id,omitempty"` // flow turn that dispatched the work
	Refs            ProducerRef          `json:"refs,omitempty"`
	Status          MailboxMessageStatus `json:"status"`
	Revision        int                  `json:"revision"`
	CreatedAt       time.Time            `json:"created_at"`
	UpdatedAt       time.Time            `json:"updated_at"`
}
