package types

import "context"

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
