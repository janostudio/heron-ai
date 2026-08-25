package types

import "time"

// SessionStatus is the lifecycle state of a persistent session.
type SessionStatus string

const (
	SessionCreated      SessionStatus = "created"
	SessionRunning      SessionStatus = "running"
	SessionWaitingInput SessionStatus = "waiting_input"
	SessionInterrupted  SessionStatus = "interrupted"
	SessionCompleted    SessionStatus = "completed"
	SessionFailed       SessionStatus = "failed"
	SessionCancelled    SessionStatus = "cancelled"
)

// FlowSession is the persistent conversation and orchestration context for a
// Flow. It is recoverable from session.jsonl.
type FlowSession struct {
	ID        string        `yaml:"id" json:"id"`
	FlowID    string        `yaml:"flow_id" json:"flow_id"`
	Status    SessionStatus `yaml:"status" json:"status"`
	CreatedAt time.Time     `yaml:"created_at" json:"created_at"`
	UpdatedAt time.Time     `yaml:"updated_at" json:"updated_at"`
}

// TeamSession is the continuing context of a Team inside one FlowSession.
type TeamSession struct {
	ID            string        `yaml:"id" json:"id"`
	FlowSessionID string        `yaml:"flow_session_id" json:"flow_session_id"`
	TeamID        string        `yaml:"team_id" json:"team_id"`
	Status        SessionStatus `yaml:"status" json:"status"`
	CreatedAt     time.Time     `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time     `yaml:"updated_at" json:"updated_at"`
}

// SubagentSession is the private continuing context of a subagent member.
// Command and Webhook members do not have a SubagentSession.
type SubagentSession struct {
	ID            string        `yaml:"id" json:"id"`
	TeamSessionID string        `yaml:"team_session_id" json:"team_session_id"`
	MemberID      string        `yaml:"member_id" json:"member_id"`
	AgentID       string        `yaml:"agent_id" json:"agent_id"`
	Status        SessionStatus `yaml:"status" json:"status"`
	CreatedAt     time.Time     `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time     `yaml:"updated_at" json:"updated_at"`
}
