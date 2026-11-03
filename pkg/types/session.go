package types

import "time"

// SessionStatus is the lifecycle state of a persistent session.
type SessionStatus string

const (
	SessionCreated         SessionStatus = "created"
	SessionRunning         SessionStatus = "running"
	SessionWaitingInput    SessionStatus = "waiting_input"
	SessionWaitingTool     SessionStatus = "waiting_tool"
	SessionWaitingApproval SessionStatus = "waiting_approval"
	SessionInterrupted     SessionStatus = "interrupted"
	SessionCompleted       SessionStatus = "completed"
	SessionFailed          SessionStatus = "failed"
	SessionCancelled       SessionStatus = "cancelled"
	// SessionClosed is a soft close: a per_task instance stops accepting work
	// but keeps its transcript and records, and may be reopened by dispatching
	// the same task_key again.
	SessionClosed SessionStatus = "closed"
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

// TeamLifecycleMode is the lifetime shape of a TeamSession.
//
// resident keeps the pre-existing behaviour: one session per Team inside a
// FlowSession, reused by every dispatch. per_task gives every dispatched task
// its own session so the same Team definition can run several tasks at once.
type TeamLifecycleMode string

const (
	// TeamLifecycleResident is the default: one session per Team per FlowSession.
	TeamLifecycleResident TeamLifecycleMode = "resident"
	// TeamLifecyclePerTask gives every dispatched task its own session.
	TeamLifecyclePerTask TeamLifecycleMode = "per_task"
)

// NormalizeTeamLifecycle maps an unset value to resident. Unknown values are
// passed through: load-time validation reports them with the offending field
// instead of letting a typo silently behave like resident.
func NormalizeTeamLifecycle(mode TeamLifecycleMode) TeamLifecycleMode {
	if mode == "" {
		return TeamLifecycleResident
	}
	return mode
}

// IsPerTask reports whether the session is task-scoped.
func (mode TeamLifecycleMode) IsPerTask() bool {
	return mode == TeamLifecyclePerTask
}

// TeamSessionRef identifies the instance that dispatched a task. It is the
// bookkeeping behind "who owns this task": the dispatcher cannot be
// transferred, so only this instance may continue, close or reopen it.
type TeamSessionRef struct {
	TeamSessionID string `yaml:"team_session_id,omitempty" json:"team_session_id,omitempty"`
	CallID        string `yaml:"call_id,omitempty" json:"call_id,omitempty"`
	AgentTurnID   string `yaml:"agent_turn_id,omitempty" json:"agent_turn_id,omitempty"`
	ToolCallID    string `yaml:"tool_call_id,omitempty" json:"tool_call_id,omitempty"`
}

// TeamSession is the continuing context of a Team inside one FlowSession.
//
// resident sessions keep the historical id "<flowSessionID>:<teamKey>" and
// reuse it for every dispatch. per_task sessions append ":" plus SessionSeq so
// two tasks of the same Team never share a session.
type TeamSession struct {
	ID string `yaml:"id" json:"id"`
	// FlowSessionID scopes the instance: a task instance never outlives its
	// FlowSession and is never shared across sessions.
	FlowSessionID string `yaml:"flow_session_id" json:"flow_session_id"`
	// TeamID is the flow-local binding name. The historical name is kept as
	// is: FlowTeamBinding.TeamID exists too and means the definition id, so
	// new code must read the definition from TeamDefinitionID below.
	TeamID string `yaml:"team_id" json:"team_id"`
	// TeamDefinitionID is the definition this instance runs (.agents/teams/<id>).
	TeamDefinitionID string `yaml:"team_definition_id,omitempty" json:"team_definition_id,omitempty"`
	// SessionSeq is the instance ordinal; 0 means the resident session.
	SessionSeq int `yaml:"session_seq,omitempty" json:"session_seq,omitempty"`
	// Lifecycle is empty for data written before the field existed, which
	// reads as resident (see EffectiveLifecycle).
	Lifecycle TeamLifecycleMode `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// DispatchedBy names the instance that created this one. Empty for the
	// entry Team, which nobody dispatched.
	DispatchedBy *TeamSessionRef `yaml:"dispatched_by,omitempty" json:"dispatched_by,omitempty"`
	// TaskKey is the business key of the task this instance serves. Required
	// for per_task sessions; it is what makes "continue the same task" find
	// the same instance after a restart.
	TaskKey string `yaml:"task_key,omitempty" json:"task_key,omitempty"`
	// TaskContextID groups a dispatch and all of its descendants. Records are
	// isolated by this value; it is empty for session-wide records.
	TaskContextID string `yaml:"task_context_id,omitempty" json:"task_context_id,omitempty"`
	// DispatchCount counts dispatched rounds (continue calls) on this instance.
	DispatchCount int `yaml:"dispatch_count,omitempty" json:"dispatch_count,omitempty"`
	// ReopenCount counts how often a closed instance was reopened. It is
	// reported, never used as a gate.
	ReopenCount int `yaml:"reopen_count,omitempty" json:"reopen_count,omitempty"`
	// Subscriptions are the sources (issue / PR / CI / thread ids) whose new
	// events are delivered straight to this instance.
	Subscriptions []string      `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
	Status        SessionStatus `yaml:"status" json:"status"`
	CreatedAt     time.Time     `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time     `yaml:"updated_at" json:"updated_at"`
	ClosedAt      *time.Time    `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
}

// EffectiveLifecycle returns the lifecycle, defaulting to resident for data
// written before the field existed.
func (s TeamSession) EffectiveLifecycle() TeamLifecycleMode {
	return NormalizeTeamLifecycle(s.Lifecycle)
}

// IsPerTask reports whether this session is task-scoped.
func (s TeamSession) IsPerTask() bool {
	return s.EffectiveLifecycle().IsPerTask()
}

// AgentSession is the private continuing context of an Agent Call.
// Command and Webhook Calls do not have an AgentSession.
type AgentSession struct {
	ID            string        `yaml:"id" json:"id"`
	TeamSessionID string        `yaml:"team_session_id" json:"team_session_id"`
	CallID        string        `yaml:"call_id" json:"call_id"`
	AgentID       string        `yaml:"agent_id" json:"agent_id"`
	Status        SessionStatus `yaml:"status" json:"status"`
	CreatedAt     time.Time     `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time     `yaml:"updated_at" json:"updated_at"`
}
