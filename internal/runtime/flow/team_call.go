package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// turnOriginTool marks a turn started by an Agent's tool call rather than by a
// user message or a mailbox wake-up.
const turnOriginTool = "tool"

// RunTeam executes one Team on behalf of an Agent's Team tool call.
//
// It is the tool-shaped entry point for collaboration: the caller has already
// handed a task handle back to the model, and this runs the callee. The outcome
// becomes the tool result, so no mailbox round trip is involved and the caller's
// conversation keeps its own message list.
//
// Two calls for the same Team in one session cannot run at once: a Team is a
// single execution unit, so the second call is refused rather than interleaved.
func (r *Runtime) RunTeam(ctx context.Context, spec types.TeamCallSpec) (types.TeamCallOutcome, error) {
	if spec.FlowSessionID == "" {
		return types.TeamCallOutcome{}, fmt.Errorf("team call requires a flow session")
	}
	if spec.TeamID == "" {
		return types.TeamCallOutcome{}, fmt.Errorf("team call requires a team id")
	}
	session, err := r.loadSession(ctx, spec.FlowSessionID)
	if err != nil {
		return types.TeamCallOutcome{}, err
	}
	defs := r.definitions.Snapshot()
	if _, ok := defs.Flow.Teams[spec.TeamID]; !ok {
		return types.TeamCallOutcome{
			Status: "failed",
			Error:  fmt.Sprintf("team %q is not configured in flow", spec.TeamID),
		}, nil
	}
	if !r.tryMarkTeamBusy(session.ID, spec.TeamID) {
		return types.TeamCallOutcome{
			Status: "failed",
			Error:  fmt.Sprintf("team %q is already running in this session", spec.TeamID),
		}, nil
	}
	defer r.clearTeamBusy(session.ID, spec.TeamID)

	now := time.Now().UTC()
	input := teamCallInputText(spec.Item)
	// The call runs as its own Flow turn: the Team's events, budget and
	// recovery stay attributable, and the caller's turn is not extended by the
	// callee's duration.
	flowTurn := types.FlowTurn{
		ID:            newID("ft"),
		FlowSessionID: session.ID,
		Input:         input,
		Status:        types.TurnRunning,
		Origin:        turnOriginTool,
		StartedAt:     now,
	}
	execution, runErr := r.executeTeamTurn(ctx, defs, session, flowTurn,
		activation{teamID: spec.TeamID, callerTeam: spec.CallerTeam},
		input, nil, now, nil)
	if runErr != nil {
		return types.TeamCallOutcome{Status: "failed", Error: runErr.Error()}, nil
	}
	outcome := types.TeamCallOutcome{
		Status: teamCallStatus(execution),
		Reply:  execution.result.Reply,
		Error:  execution.result.Error,
	}
	for _, record := range execution.result.Records {
		if record.Name != "" {
			outcome.Records = append(outcome.Records, record.Name)
		}
	}
	return outcome, nil
}

// teamCallStatus maps the callee's route to the status the caller sees.
func teamCallStatus(execution teamExecution) string {
	if execution.result.Error != "" {
		return "failed"
	}
	if execution.next == nil {
		return "done"
	}
	switch execution.next.Action {
	case types.NextFail:
		return "failed"
	case types.NextActivate, types.NextCoordinate:
		// The callee handed work further down. This is an answer from the
		// caller's point of view only when it comes back; report the fact so
		// the caller does not read it as a conclusion.
		return "dispatched"
	}
	return "done"
}

// teamCallInputText renders the tool call's item as the Team's task text.
func teamCallInputText(item any) string {
	switch value := item.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(value)
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(data)
	}
}
