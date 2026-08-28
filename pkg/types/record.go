package types

import "time"

// RecordScope controls the default visibility of a SharedRecord.
type RecordScope string

const (
	RecordScopeTeam RecordScope = "team"
	RecordScopeFlow RecordScope = "flow"
)

// RecordStatus describes whether a record is currently usable.
type RecordStatus string

const (
	RecordActive      RecordStatus = "active"
	RecordSuperseded  RecordStatus = "superseded"
	RecordInvalidated RecordStatus = "invalidated"
)

// ProducerRef identifies the execution that produced a SharedRecord.
type ProducerRef struct {
	FlowSessionID string `yaml:"flow_session_id,omitempty" json:"flow_session_id,omitempty"`
	FlowTurnID    string `yaml:"flow_turn_id,omitempty" json:"flow_turn_id,omitempty"`
	TeamID        string `yaml:"team_id,omitempty" json:"team_id,omitempty"`
	TeamTurnID    string `yaml:"team_turn_id,omitempty" json:"team_turn_id,omitempty"`
	CallID        string `yaml:"call_id,omitempty" json:"call_id,omitempty"`
	CallTurnID    string `yaml:"call_turn_id,omitempty" json:"call_turn_id,omitempty"`
}

// BasisRef points to evidence supporting a record. A workspace basis can be
// reused by a downstream call while its revision is still current.
type BasisRef struct {
	Kind             string `yaml:"kind" json:"kind"`
	Path             string `yaml:"path,omitempty" json:"path,omitempty"`
	Revision         string `yaml:"revision,omitempty" json:"revision,omitempty"`
	Lines            []int  `yaml:"lines,omitempty" json:"lines,omitempty"`
	Excerpt          string `yaml:"excerpt,omitempty" json:"excerpt,omitempty"`
	SourceTurnID     string `yaml:"source_turn_id,omitempty" json:"source_turn_id,omitempty"`
	SourceToolCallID string `yaml:"source_tool_call_id,omitempty" json:"source_tool_call_id,omitempty"`
}

// RecordLink connects a record to another record or an external object.
type RecordLink struct {
	Relation string `yaml:"relation" json:"relation"`
	TargetID string `yaml:"target_id" json:"target_id"`
}

// SharedRecord is the only generic cross-Team / cross-call collaboration
// object. Data remains business-defined; the core only owns this envelope.
type SharedRecord struct {
	RecordID  string         `yaml:"record_id" json:"record_id"`
	Kind      string         `yaml:"kind" json:"kind"`
	Name      string         `yaml:"name" json:"name"`
	Scope     RecordScope    `yaml:"scope" json:"scope"`
	Producer  ProducerRef    `yaml:"producer" json:"producer"`
	Summary   string         `yaml:"summary" json:"summary"`
	Data      map[string]any `yaml:"data,omitempty" json:"data,omitempty"`
	Basis     []BasisRef     `yaml:"basis,omitempty" json:"basis,omitempty"`
	Status    RecordStatus   `yaml:"status" json:"status"`
	Revision  int            `yaml:"revision" json:"revision"`
	Links     []RecordLink   `yaml:"links,omitempty" json:"links,omitempty"`
	CreatedAt time.Time      `yaml:"created_at" json:"created_at"`
}
