package types

import (
	"sort"
	"time"
)

// SessionStatus is the lifecycle state of a persistent session.
type SessionStatus string

const (
	SessionCreated SessionStatus = "created"
	SessionRunning SessionStatus = "running"
	// SessionIdle means the session has nothing in flight and will accept
	// another turn. It describes what the caller can do, not what the engine
	// is waiting for.
	SessionIdle            SessionStatus = "idle"
	SessionWaitingTool     SessionStatus = "waiting_tool"
	SessionWaitingApproval SessionStatus = "waiting_approval"
	SessionInterrupted     SessionStatus = "interrupted"
	SessionCompleted       SessionStatus = "completed"
	SessionFailed          SessionStatus = "failed"
	SessionCancelled       SessionStatus = "cancelled"
	// SessionClosed is a soft close: an isolated instance stops accepting work
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
// shared gives one session per Team inside a FlowSession, reused by every
// dispatch. isolated gives every dispatched task its own session so the same
// Team definition can run several tasks at once.
type TeamLifecycleMode string

const (
	// TeamLifecycleShared is the default: one session per Team per FlowSession.
	TeamLifecycleShared TeamLifecycleMode = "shared"
	// TeamLifecycleIsolated gives every dispatched task its own session.
	TeamLifecycleIsolated TeamLifecycleMode = "isolated"
)

// NormalizeTeamLifecycle maps an unset value to shared. Unknown values are
// passed through: load-time validation reports them with the offending field
// instead of letting a typo silently behave like shared.
func NormalizeTeamLifecycle(mode TeamLifecycleMode) TeamLifecycleMode {
	if mode == "" {
		return TeamLifecycleShared
	}
	return mode
}

// IsIsolated reports whether the session is task-scoped.
func (mode TeamLifecycleMode) IsIsolated() bool {
	return mode == TeamLifecycleIsolated
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

// AuthorizationGrant is the permission a task was created with. It is frozen
// when the task is created: continuing or reopening the task reuses it, so the
// scope cannot quietly widen later in the task's life.
type AuthorizationGrant struct {
	Scopes    []string  `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	Source    string    `yaml:"source,omitempty" json:"source,omitempty"`
	GrantedBy string    `yaml:"granted_by,omitempty" json:"granted_by,omitempty"`
	GrantedAt time.Time `yaml:"granted_at,omitempty" json:"granted_at,omitempty"`
}

// ScopesEqual reports whether two grants carry the same scope set, ignoring
// order. An empty scope set is not equal to a non-empty one: adding a scope is
// a change, and a change is what the freeze rule refuses.
func (g *AuthorizationGrant) ScopesEqual(other *AuthorizationGrant) bool {
	if g == nil && other == nil {
		return true
	}
	if g == nil || other == nil {
		return false
	}
	if len(g.Scopes) != len(other.Scopes) {
		return false
	}
	left := append([]string(nil), g.Scopes...)
	right := append([]string(nil), other.Scopes...)
	sort.Strings(left)
	sort.Strings(right)
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// TeamSession is the continuing context of a Team inside one FlowSession.
//
// A shared session uses the id "<flowSessionID>:<teamKey>" and reuses it for
// every dispatch. An isolated session appends ":" plus SessionSeq so two tasks
// of the same Team never share a session.
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
	// SessionSeq is the instance ordinal; 0 means the shared session.
	SessionSeq int `yaml:"session_seq,omitempty" json:"session_seq,omitempty"`
	// Lifecycle is this session's lifetime shape; empty means shared (see
	// EffectiveLifecycle).
	Lifecycle TeamLifecycleMode `yaml:"lifecycle,omitempty" json:"lifecycle,omitempty"`
	// DispatchedBy names the instance that created this one. Empty for the
	// entry Team, which nobody dispatched.
	DispatchedBy *TeamSessionRef `yaml:"dispatched_by,omitempty" json:"dispatched_by,omitempty"`
	// TaskKey is the business key of the task this instance serves. Required
	// for isolated sessions; it is what makes "continue the same task" find
	// the same instance after a restart.
	TaskKey string `yaml:"task_key,omitempty" json:"task_key,omitempty"`
	// TaskContextID groups a dispatch and all of its descendants. Records are
	// isolated by this value; it is empty for session-wide records.
	TaskContextID string `yaml:"task_context_id,omitempty" json:"task_context_id,omitempty"`
	// TaskDepth is the nesting depth inside one task context: a task dispatched
	// by a shared instance is 1, a task dispatched by an isolated instance is
	// its dispatcher's depth + 1.
	TaskDepth int `yaml:"task_depth,omitempty" json:"task_depth,omitempty"`
	// DispatchCount counts dispatched rounds (continue calls) on this instance.
	DispatchCount int `yaml:"dispatch_count,omitempty" json:"dispatch_count,omitempty"`
	// ReopenCount counts how often a closed instance was reopened. It is
	// reported, never used as a gate.
	ReopenCount int `yaml:"reopen_count,omitempty" json:"reopen_count,omitempty"`
	// Subscriptions are the sources (issue / PR / CI / thread ids) whose new
	// events are delivered straight to this instance.
	Subscriptions []string `yaml:"subscriptions,omitempty" json:"subscriptions,omitempty"`
	// Authorization is the permission this task was created with, frozen for
	// the life of the task.
	Authorization *AuthorizationGrant `yaml:"authorization,omitempty" json:"authorization,omitempty"`
	Status        SessionStatus       `yaml:"status" json:"status"`
	CreatedAt     time.Time           `yaml:"created_at" json:"created_at"`
	UpdatedAt     time.Time           `yaml:"updated_at" json:"updated_at"`
	ClosedAt      *time.Time          `yaml:"closed_at,omitempty" json:"closed_at,omitempty"`
}

// EffectiveLifecycle returns the lifecycle, defaulting to shared for data
// written before the field existed.
func (s TeamSession) EffectiveLifecycle() TeamLifecycleMode {
	return NormalizeTeamLifecycle(s.Lifecycle)
}

// IsIsolated reports whether this session is task-scoped.
func (s TeamSession) IsIsolated() bool {
	return s.EffectiveLifecycle().IsIsolated()
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
