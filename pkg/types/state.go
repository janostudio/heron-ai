package types

import "time"

// StateObservation is an optional learning input produced by a State
// extension. It is separate from the bounded StateSnapshot persisted by the
// core runtime.
type StateObservation struct {
	Content    string `json:"content"`
	Importance string `json:"importance"` // low | medium | high | critical
	Source     string `json:"source"`
	Round      int    `json:"round"`
	Timestamp  string `json:"timestamp"`
}

// StateScope identifies the two V1 short-term state layers.
type StateScope string

const (
	StateScopeTeam  StateScope = "team"
	StateScopeAgent StateScope = "agent"
	// StateScopeEntity is the persistent state of one dynamic Agent entity
	// (design doc 20). Unlike the session-scoped layers, it survives across
	// sessions and is keyed by entity, not by call.
	StateScopeEntity StateScope = "entity"
)

// StateWorkspaceRef keeps only a small pointer to workspace state. Complete
// read/write history remains in session.jsonl and WorkspaceOperation events.
type StateWorkspaceRef struct {
	Path     string `yaml:"path" json:"path"`
	Revision string `yaml:"revision,omitempty" json:"revision,omitempty"`
}

// StateSnapshot is the fixed-format short-term work snapshot stored as
// state.md. It is intentionally bounded and does not replace the session
// timeline or SharedRecord evidence chain.
type StateSnapshot struct {
	Scope         StateScope          `yaml:"scope" json:"scope"`
	SessionID     string              `yaml:"session_id" json:"session_id"`
	TeamID        string              `yaml:"team_id" json:"team_id"`
	CallID        string              `yaml:"call_id,omitempty" json:"call_id,omitempty"`
	Revision      int                 `yaml:"revision" json:"revision"`
	Goal          string              `yaml:"goal,omitempty" json:"goal,omitempty"`
	Confirmed     []string            `yaml:"confirmed,omitempty" json:"confirmed,omitempty"`
	OpenQuestions []string            `yaml:"open_questions,omitempty" json:"open_questions,omitempty"`
	Decisions     []string            `yaml:"decisions,omitempty" json:"decisions,omitempty"`
	NextSteps     []string            `yaml:"next_steps,omitempty" json:"next_steps,omitempty"`
	Workspace     []StateWorkspaceRef `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	RecordIDs     []string            `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	UpdatedAt     time.Time           `yaml:"updated_at" json:"updated_at"`
}
