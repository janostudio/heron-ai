package types

import (
	"context"
	"time"
)

// RecoveryAction is explicit because a crashed call must never be
// re-executed implicitly during ordinary session replay.
type RecoveryAction string

const (
	RecoveryInspect    RecoveryAction = "inspect"
	RecoveryWait       RecoveryAction = "wait"
	RecoveryRetry      RecoveryAction = "retry"
	RecoveryCoordinate RecoveryAction = "coordinate"
)

// RecoveryRequest is submitted after inspecting interrupted executions.
//
// TargetTurnID may be a TeamTurnID or CallTurnID. V1 retries the containing
// Team as a unit, never an individual side-effecting call.
type RecoveryRequest struct {
	Action                RecoveryAction `json:"action"`
	TargetTurnID          string         `json:"target_turn_id,omitempty"`
	Input                 string         `json:"input,omitempty"`
	Reason                string         `json:"reason,omitempty"`
	AllowSideEffectReplay bool           `json:"allow_side_effect_replay,omitempty"`
}

// InterruptedExecution describes a started execution that has no matching
// completed event in session.jsonl. It is a recovery signal, not a checkpoint.
type InterruptedExecution struct {
	Kind         string    `json:"kind"` // flow_turn | team_turn | call_turn
	FlowTurnID   string    `json:"flow_turn_id,omitempty"`
	TeamID       string    `json:"team_id,omitempty"`
	TeamTurnID   string    `json:"team_turn_id,omitempty"`
	CallID       string    `json:"call_id,omitempty"`
	CallTurnID   string    `json:"call_turn_id,omitempty"`
	CallType     CallType  `json:"call_type,omitempty"`
	Attempt      int       `json:"attempt"`
	RecoveryOf   string    `json:"recovery_of,omitempty"`
	StartedSeq   int64     `json:"started_seq,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	Input        string    `json:"input,omitempty"`
	CallerTeam   string    `json:"caller_team,omitempty"`
	StartedEvent string    `json:"started_event"`
	SafeToRetry  bool      `json:"safe_to_retry"`
	RetryReason  string    `json:"retry_reason,omitempty"`
}

type RecoveryStatus struct {
	Session         FlowSession            `json:"session"`
	Interrupted     []InterruptedExecution `json:"interrupted,omitempty"`
	RecoveryHistory []RecoveryEvent        `json:"recovery_history,omitempty"`
}

type RecoveryEvent struct {
	Seq          int64          `json:"seq"`
	EventID      string         `json:"event_id"`
	Status       string         `json:"status"` // requested | completed
	Action       RecoveryAction `json:"action"`
	TargetTurnID string         `json:"target_turn_id,omitempty"`
	Attempt      int            `json:"attempt,omitempty"`
	Reason       string         `json:"reason,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// RecoveryRuntime is optional on top of FlowRuntime. It keeps normal chat
// APIs small while exposing explicit crash recovery to operators and UIs.
type RecoveryRuntime interface {
	RecoveryStatus(ctx context.Context, sessionID string) (RecoveryStatus, error)
	Recover(ctx context.Context, sessionID string, req RecoveryRequest) (FlowTurnResult, error)
}
