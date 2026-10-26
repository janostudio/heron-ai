package types

import "context"

// ToolNameTeamRun is the internal tool name of the durable task that runs one
// Team call. It is never registered for models: only a task created by the Team
// tool reaches it. Shared so the flow can recognise such a task without
// importing the agent package.
const ToolNameTeamRun = "TeamRun"

// TeamCallSpec is one Agent's request to another Team.
//
// Collaboration is modelled as an asynchronous tool call: the caller hands over
// a task and gets a handle, the callee runs on its own, and the answer returns
// as the tool result. This struct is the tool call's arguments, widened with the
// caller's coordinates so the answer can route back.
type TeamCallSpec struct {
	// FlowSessionID scopes the call: the callee is part of the same session, not
	// a detached job.
	FlowSessionID string
	// CallerTeam is the Team that made the call.
	CallerTeam string
	// TeamID is the callee.
	TeamID string
	// CallID and AgentTurnID name the calling Agent, for provenance in events.
	CallID      string
	AgentTurnID string
	// Item is the task handed to the callee.
	Item any
	// Summary is a short description of the task, for display while it runs.
	Summary string
}

// TeamCallOutcome is what the caller receives as the tool result.
type TeamCallOutcome struct {
	// Status mirrors the callee's own route (done / blocked / failed), so the
	// caller can tell "answered" from "could not answer" without reading prose.
	Status string
	// Reply is the callee's answer, self-contained.
	Reply string
	// Records are the names of the records the callee published, for the
	// caller's provenance display.
	Records []string
	// Error is set when the call itself failed (budget, timeout, crash).
	Error string
}

// TeamRunner executes one Team call. The flow runtime implements it: only the
// flow knows the session, the Team bindings and how to persist a Team turn.
type TeamRunner interface {
	RunTeam(ctx context.Context, spec TeamCallSpec) (TeamCallOutcome, error)
}

// TeamCatalogEntry is one Team a caller may hand work to, as the caller needs to
// see it: what it is for, what it can read, what it produces.
//
// This is the projection of the Flow's graph into something a model can choose
// from. Configuration stays the source of truth; the catalog is a read-only
// view of it, scoped to one caller.
type TeamCatalogEntry struct {
	TeamID string `json:"subagent_type"`
	// Responsibility is the Team's own statement of what it does, taken from its
	// entry call. It is what the model reasons about when choosing.
	Responsibility string `json:"responsibility,omitempty"`
	// Receives lists the inputs the target can read: "user_message" and the
	// record names it subscribes to.
	Receives []string `json:"receives,omitempty"`
	// Produces is the record the target publishes when it finishes, so a caller
	// knows what to read next.
	Produces string `json:"produces,omitempty"`
}

// TeamCatalogSpec identifies whose view is being read.
type TeamCatalogSpec struct {
	FlowSessionID string
	// CallerTeam is the Team asking: the answer is its can_activate, not the
	// whole graph.
	CallerTeam string
}

// TeamCatalog lists the Teams one caller may hand work to.
type TeamCatalog interface {
	ListTeams(ctx context.Context, spec TeamCatalogSpec) ([]TeamCatalogEntry, error)
}
