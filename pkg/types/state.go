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

// StateScope identifies the two V1 state layers (team and agent). Both
// scopes persist across sessions: team state lives under the session's team
// directory, while agent state has a session-scoped per-call form and a
// cross-session form keyed only by agent id (design doc 26).
type StateScope string

const (
	StateScopeTeam  StateScope = "team"
	StateScopeAgent StateScope = "agent"
)

// StateWorkspaceRef keeps only a small pointer to workspace state. Complete
// read/write history remains in session.jsonl and WorkspaceOperation events.
type StateWorkspaceRef struct {
	Path     string `yaml:"path" json:"path"`
	Revision string `yaml:"revision,omitempty" json:"revision,omitempty"`
}

// StateItem is one todo entry in a state list. The ID is engine-generated and
// unique within the list; Text is the entry content. AddedBy and HandledBy
// record the actor (as "agent(instance)" identifiers) who created and last
// updated the entry, so a shared per-agent todo whiteboard stays traceable
// while every concurrent instance may still read or mutate any entry.
type StateItem struct {
	ID        string `yaml:"id" json:"id"`
	Text      string `yaml:"text" json:"text"`
	AddedBy   string `yaml:"added_by,omitempty" json:"added_by,omitempty"`
	HandledBy string `yaml:"handled_by,omitempty" json:"handled_by,omitempty"`
}

// StateSnapshot is the fixed-format shared todo whiteboard stored as state.md.
// Every instance of an agent reads and writes the same snapshot, so entries
// carry AddedBy/HandledBy attribution. It is intentionally bounded and does
// not replace the session timeline or SharedRecord evidence chain. The list
// fields are todo lists of StateItem (id + text + attribution), not plain
// strings.
type StateSnapshot struct {
	Scope         StateScope          `yaml:"scope" json:"scope"`
	SessionID     string              `yaml:"session_id" json:"session_id"`
	TeamID        string              `yaml:"team_id" json:"team_id"`
	CallID        string              `yaml:"call_id,omitempty" json:"call_id,omitempty"`
	Revision      int                 `yaml:"revision" json:"revision"`
	Goal          string              `yaml:"goal,omitempty" json:"goal,omitempty"`
	Confirmed     []StateItem         `yaml:"confirmed,omitempty" json:"confirmed,omitempty"`
	OpenQuestions []StateItem         `yaml:"open_questions,omitempty" json:"open_questions,omitempty"`
	Decisions     []StateItem         `yaml:"decisions,omitempty" json:"decisions,omitempty"`
	NextSteps     []StateItem         `yaml:"next_steps,omitempty" json:"next_steps,omitempty"`
	Workspace     []StateWorkspaceRef `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	RecordIDs     []string            `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	UpdatedAt     time.Time           `yaml:"updated_at" json:"updated_at"`
}
