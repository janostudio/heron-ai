package types

import (
	"context"
	"time"
)

// WakeupRecord is one self-scheduled wake-up: a note a model left for itself,
// with the time it wants to be given back.
//
// It is deliberately not a ToolTask. A tool task is work the engine performs on
// the caller's behalf and whose result becomes a tool message. A wake-up
// performs nothing: it only re-enters the conversation later, so it has no
// tool call to answer. Keeping them apart is what lets a wake-up fire while the
// session is idle, with no caller waiting on it.
type WakeupRecord struct {
	ID            string `json:"id"`
	FlowSessionID string `json:"flow_session_id"`
	// TeamID is the Team that asked to be woken. The wake-up starts its turn
	// at that Team rather than at the Flow entry, so the model that left the
	// note is the one that reads it back.
	TeamID string `json:"team_id"`
	CallID string `json:"call_id,omitempty"`
	// FireAt is absolute and UTC. A relative "in 6h" is resolved against
	// CreatedAt when the record is created, so later clock changes cannot
	// move a scheduled wake-up.
	FireAt time.Time `json:"fire_at"`
	// Note is what the model wrote for itself: what to check, and what to do
	// depending on what it finds. It is handed back verbatim.
	Note string `json:"note"`
	// LastSeq is the session event sequence the previous wake-up had already
	// seen. The facts block reads only events after it, so a session that has
	// been running for a day does not replay its whole jsonl on every wake-up.
	LastSeq int64 `json:"last_seq,omitempty"`
	// Status is one of the Wakeup* constants below.
	Status string `json:"status"`
	// Fires counts how many wake-ups this session has fired, for the daily
	// quota. It travels on the record so recovery can resume the count.
	Fires int `json:"fires,omitempty"`
	// Chain counts consecutive wake-ups derived from one another (a wake-up
	// turn that schedules the next one). It is what bounds a self-maintaining
	// chain that never produces anything.
	Chain     int        `json:"chain,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	FiredAt   *time.Time `json:"fired_at,omitempty"`
}

// Wake-up lifecycle states.
const (
	WakeupScheduled = "scheduled"
	WakeupFired     = "fired"
	WakeupSkipped   = "skipped"
	WakeupCancelled = "cancelled"
	WakeupExpired   = "expired"
)

// WakeupRequest is what a Tool hands to the flow runtime. The Tool resolves the
// caller identity from its context and the fire time from the model's argument;
// everything else about where the record lives is the runtime's business.
type WakeupRequest struct {
	FlowSessionID string
	TeamID        string
	CallID        string
	FireAt        time.Time
	Note          string
}

// WakeupLimits bounds self-scheduled wake-ups. They are resource limits, not
// judgement: the engine never decides whether a wake-up is worth its cost, it
// only refuses to start more than the session is allowed.
//
// Zero values fall back to the defaults below via WithDefaults, so a runtime
// built without configuration still behaves safely.
type WakeupLimits struct {
	// MinAfter rejects a wake-up scheduled closer than this. It exists to stop
	// a turn from re-arming itself in a tight loop.
	MinAfter time.Duration
	// MaxPending caps how many wake-ups one session may have outstanding.
	MaxPending int
	// MaxFiresPerDay caps how many wake-ups one session may fire per UTC day.
	MaxFiresPerDay int
	// MaxChain caps consecutive wake-ups derived from one another. A model
	// that wakes up, decides nothing has changed, and re-arms for the same
	// reason would otherwise continue forever.
	MaxChain int
	// MaxAge caps how far in the future one wake-up may be scheduled.
	MaxAge time.Duration
	// QuietAfterUserTurn delays a due wake-up when the user spoke this
	// recently, so a background check never talks over a live conversation.
	QuietAfterUserTurn time.Duration
}

// DefaultWakeupLimits are the values used when the config leaves a field zero.
func DefaultWakeupLimits() WakeupLimits {
	return WakeupLimits{
		MinAfter:           time.Minute,
		MaxPending:         8,
		MaxFiresPerDay:     20,
		MaxChain:           20,
		MaxAge:             7 * 24 * time.Hour,
		QuietAfterUserTurn: 5 * time.Minute,
	}
}

// WithDefaults fills every unset field from DefaultWakeupLimits.
func (l WakeupLimits) WithDefaults() WakeupLimits {
	d := DefaultWakeupLimits()
	if l.MinAfter <= 0 {
		l.MinAfter = d.MinAfter
	}
	if l.MaxPending <= 0 {
		l.MaxPending = d.MaxPending
	}
	if l.MaxFiresPerDay <= 0 {
		l.MaxFiresPerDay = d.MaxFiresPerDay
	}
	if l.MaxChain <= 0 {
		l.MaxChain = d.MaxChain
	}
	if l.MaxAge <= 0 {
		l.MaxAge = d.MaxAge
	}
	if l.QuietAfterUserTurn <= 0 {
		l.QuietAfterUserTurn = d.QuietAfterUserTurn
	}
	return l
}

// WakeupScheduler is the narrow capability the ScheduleWakeup tool depends on.
// It is an interface rather than a *flow.Runtime because the Tool lives in
// internal/agent and the runtime is built after the tool registry.
type WakeupScheduler interface {
	ScheduleWakeup(ctx context.Context, req WakeupRequest) (WakeupRecord, error)
	CancelWakeup(ctx context.Context, sessionID, wakeupID string) error
}
