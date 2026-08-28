package types

import "time"

// MemoryObservation is an optional learning input produced by a Memory
// extension. It is separate from the bounded MemorySnapshot persisted by the
// core runtime.
type MemoryObservation struct {
	Content    string `json:"content"`
	Importance string `json:"importance"` // low | medium | high | critical
	Source     string `json:"source"`
	Round      int    `json:"round"`
	Timestamp  string `json:"timestamp"`
}

// MemoryScope identifies the two V1 short-term memory layers.
type MemoryScope string

const (
	MemoryScopeTeam  MemoryScope = "team"
	MemoryScopeAgent MemoryScope = "agent"
)

// MemoryWorkspaceRef keeps only a small pointer to workspace state. Complete
// read/write history remains in session.jsonl and WorkspaceOperation events.
type MemoryWorkspaceRef struct {
	Path     string `yaml:"path" json:"path"`
	Revision string `yaml:"revision,omitempty" json:"revision,omitempty"`
}

// MemorySnapshot is the fixed-format short-term work snapshot stored as
// memory.md. It is intentionally bounded and does not replace the session
// timeline or SharedRecord evidence chain.
type MemorySnapshot struct {
	Scope         MemoryScope          `yaml:"scope" json:"scope"`
	SessionID     string               `yaml:"session_id" json:"session_id"`
	TeamID        string               `yaml:"team_id" json:"team_id"`
	CallID        string               `yaml:"call_id,omitempty" json:"call_id,omitempty"`
	Revision      int                  `yaml:"revision" json:"revision"`
	Goal          string               `yaml:"goal,omitempty" json:"goal,omitempty"`
	Confirmed     []string             `yaml:"confirmed,omitempty" json:"confirmed,omitempty"`
	OpenQuestions []string             `yaml:"open_questions,omitempty" json:"open_questions,omitempty"`
	Decisions     []string             `yaml:"decisions,omitempty" json:"decisions,omitempty"`
	NextSteps     []string             `yaml:"next_steps,omitempty" json:"next_steps,omitempty"`
	Workspace     []MemoryWorkspaceRef `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	RecordIDs     []string             `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	UpdatedAt     time.Time            `yaml:"updated_at" json:"updated_at"`
}
