package types

import (
	"time"

	"gopkg.in/yaml.v3"
)

// TurnStatus is the lifecycle state of one execution.
type TurnStatus string

const (
	TurnCreated         TurnStatus = "created"
	TurnRunning         TurnStatus = "running"
	TurnWaitingInput    TurnStatus = "waiting_input"
	TurnWaitingApproval TurnStatus = "waiting_approval"
	TurnCompleted       TurnStatus = "completed"
	TurnFailed          TurnStatus = "failed"
	TurnCancelled       TurnStatus = "cancelled"
)

// NextAction is the small routing protocol shared by FlowRuntime and
// TeamRuntime.
type NextAction string

const (
	NextProceed    NextAction = "proceed"
	NextReturn     NextAction = "return"
	NextCoordinate NextAction = "coordinate"
	NextWaitInput  NextAction = "wait_input"
	NextComplete   NextAction = "complete"
	NextFail       NextAction = "fail"
	NextActivate   NextAction = "activate"
)

// Route is a Team or Flow routing decision. Teams is used by activate;
// CallerTeam is populated by runtime when a result returns to its caller.
type Route struct {
	Action     NextAction `yaml:"action" json:"action"`
	Teams      []string   `yaml:"teams,omitempty" json:"teams,omitempty"`
	CallerTeam string     `yaml:"caller_team,omitempty" json:"caller_team,omitempty"`
	Reason     string     `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// UnmarshalYAML accepts both:
//
//	on_proceed: [research, fix]
//
// and:
//
//	on_proceed:
//	  action: proceed
//	  teams: [research, fix]
//
// The short list form means "proceed and activate these fixed teams".
func (r *Route) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.SequenceNode {
		var teams []string
		if err := node.Decode(&teams); err != nil {
			return err
		}
		r.Action = NextProceed
		r.Teams = teams
		return nil
	}

	type routeAlias Route
	var decoded routeAlias
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*r = Route(decoded)
	return nil
}

// FlowTurn represents one user input or external trigger handled by a
// FlowSession.
type FlowTurn struct {
	ID            string     `yaml:"id" json:"id"`
	FlowSessionID string     `yaml:"flow_session_id" json:"flow_session_id"`
	Attempt       int        `yaml:"attempt" json:"attempt"`
	RecoveryOf    string     `yaml:"recovery_of,omitempty" json:"recovery_of,omitempty"`
	Input         string     `yaml:"input" json:"input"`
	Status        TurnStatus `yaml:"status" json:"status"`
	Next          *Route     `yaml:"next,omitempty" json:"next,omitempty"`
	RecordIDs     []string   `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	StartedAt     time.Time  `yaml:"started_at" json:"started_at"`
	FinishedAt    *time.Time `yaml:"finished_at,omitempty" json:"finished_at,omitempty"`
}

// TeamTurn represents one invocation of a TeamSession.
type TeamTurn struct {
	ID            string     `yaml:"id" json:"id"`
	FlowTurnID    string     `yaml:"flow_turn_id" json:"flow_turn_id"`
	TeamSessionID string     `yaml:"team_session_id" json:"team_session_id"`
	TeamID        string     `yaml:"team_id" json:"team_id"`
	Attempt       int        `yaml:"attempt" json:"attempt"`
	RecoveryOf    string     `yaml:"recovery_of,omitempty" json:"recovery_of,omitempty"`
	CallerTeam    string     `yaml:"caller_team,omitempty" json:"caller_team,omitempty"`
	Status        TurnStatus `yaml:"status" json:"status"`
	Next          *Route     `yaml:"next,omitempty" json:"next,omitempty"`
	RecordIDs     []string   `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	StartedAt     time.Time  `yaml:"started_at" json:"started_at"`
	FinishedAt    *time.Time `yaml:"finished_at,omitempty" json:"finished_at,omitempty"`
}

// SubagentTurn represents one request/response execution by a Subagent
// member. Model and Tool calls remain implementation details inside it.
type SubagentTurn struct {
	ID                string     `yaml:"id" json:"id"`
	TeamTurnID        string     `yaml:"team_turn_id" json:"team_turn_id"`
	SubagentSessionID string     `yaml:"subagent_session_id" json:"subagent_session_id"`
	MemberID          string     `yaml:"member_id" json:"member_id"`
	Attempt           int        `yaml:"attempt" json:"attempt"`
	RecoveryOf        string     `yaml:"recovery_of,omitempty" json:"recovery_of,omitempty"`
	Status            TurnStatus `yaml:"status" json:"status"`
	RecordIDs         []string   `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	StartedAt         time.Time  `yaml:"started_at" json:"started_at"`
	FinishedAt        *time.Time `yaml:"finished_at,omitempty" json:"finished_at,omitempty"`
}

// CommandTurn represents one direct command execution by a Team member.
type CommandTurn struct {
	ID         string     `yaml:"id" json:"id"`
	TeamTurnID string     `yaml:"team_turn_id" json:"team_turn_id"`
	MemberID   string     `yaml:"member_id" json:"member_id"`
	Attempt    int        `yaml:"attempt" json:"attempt"`
	RecoveryOf string     `yaml:"recovery_of,omitempty" json:"recovery_of,omitempty"`
	Status     TurnStatus `yaml:"status" json:"status"`
	RecordIDs  []string   `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	StartedAt  time.Time  `yaml:"started_at" json:"started_at"`
	FinishedAt *time.Time `yaml:"finished_at,omitempty" json:"finished_at,omitempty"`
}

// WebhookTurn represents one direct HTTP execution by a Team member.
type WebhookTurn struct {
	ID         string     `yaml:"id" json:"id"`
	TeamTurnID string     `yaml:"team_turn_id" json:"team_turn_id"`
	MemberID   string     `yaml:"member_id" json:"member_id"`
	Attempt    int        `yaml:"attempt" json:"attempt"`
	RecoveryOf string     `yaml:"recovery_of,omitempty" json:"recovery_of,omitempty"`
	Status     TurnStatus `yaml:"status" json:"status"`
	RecordIDs  []string   `yaml:"record_ids,omitempty" json:"record_ids,omitempty"`
	StartedAt  time.Time  `yaml:"started_at" json:"started_at"`
	FinishedAt *time.Time `yaml:"finished_at,omitempty" json:"finished_at,omitempty"`
}
