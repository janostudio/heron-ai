package types

import "context"

// FlowTopology is the configuration graph of one Flow together with the
// execution state of one session.
//
// It answers a question the event stream cannot: which Teams and Calls exist
// before anything has run. A board built only from events can show what has
// already happened; it cannot show the shape of the graph, so Teams that never
// ran are invisible and the reader has to remember the config to notice.
type FlowTopology struct {
	FlowID string         `json:"flow_id"`
	Entry  string         `json:"entry"`
	Teams  []TopologyTeam `json:"teams"`
}

// TopologyTeam is one Flow-local Team binding: what it is made of, and whether
// it is running in the session being asked about.
type TopologyTeam struct {
	// Name is the Flow-local binding name, the same identifier the event
	// stream carries in team_session.team_id.
	Name string `json:"name"`
	// TeamID is the reusable definition under .agents/teams/.
	TeamID      string         `json:"team_id"`
	Goal        string         `json:"goal,omitempty"`
	Entry       bool           `json:"entry"`
	Lifecycle   string         `json:"lifecycle"`
	CanActivate []string       `json:"can_activate,omitempty"`
	Calls       []TopologyCall `json:"calls"`

	// Status is running, idle, or never:
	//   running — at least one instance is executing a turn right now
	//   idle    — it has run in this session and none is executing
	//   never   — it has not run in this session
	Status string `json:"status"`
	// CallStatus maps a Call name to running or idle for Calls that have run.
	// A Call absent from the map has never run in this session.
	CallStatus map[string]string `json:"call_status,omitempty"`

	// Instances is how many Team instances this session has created for the
	// binding (1 for a resident Team, N for per_task); RunningInstances is how
	// many of them are executing right now. A per_task Team can run several
	// tasks at once, and a board that shows only "running" would hide that.
	Instances        int `json:"instances,omitempty"`
	RunningInstances int `json:"running_instances,omitempty"`
}

// TopologyCall is one execution item of a Team.
type TopologyCall struct {
	Name string `json:"name"`
	// Type is agent, command, or webhook.
	Type string `json:"type"`
	// Agent is the agent definition a type=agent Call runs ("" otherwise).
	Agent string `json:"agent,omitempty"`
}

// TopologyRuntime is an optional runtime extension. A transport that wants the
// configuration graph asks for it; a runtime that does not implement it can
// still serve every other endpoint.
type TopologyRuntime interface {
	// Topology returns the Flow graph and, when sessionID is non-empty, the
	// execution state of that session.
	Topology(ctx context.Context, sessionID string) (FlowTopology, error)
}
