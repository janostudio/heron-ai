package types

import "time"

// WorkspaceOperation is the audit fact for every real cwd read/write/test
// operation. It is not itself a cross-Team collaboration record.
type WorkspaceOperation struct {
	OperationID  string    `json:"operation_id"`
	TurnID       string    `json:"turn_id,omitempty"`
	Kind         string    `json:"kind"` // read | search | write | test | bash
	Path         string    `json:"path,omitempty"`
	Revision     string    `json:"revision,omitempty"`
	BaseRevision string    `json:"base_revision,omitempty"`
	Lines        []int     `json:"lines,omitempty"`
	Excerpt      string    `json:"excerpt,omitempty"`
	Summary      string    `json:"summary,omitempty"`
	Command      string    `json:"command,omitempty"`
	ExitCode     int       `json:"exit_code,omitempty"`
	Truncated    bool      `json:"truncated,omitempty"`
	StartedAt    time.Time `json:"started_at"`
	FinishedAt   time.Time `json:"finished_at"`
}
