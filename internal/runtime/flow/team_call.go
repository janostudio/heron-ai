package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

// ListTeams projects the caller's edges into a catalog the model can read.
//
// Configuration stays the source of truth: the entries are derived from the
// Flow definition on every call, so editing flows/default.yml changes what a
// model sees without touching a prompt.
func (r *Runtime) ListTeams(ctx context.Context, spec types.TeamCatalogSpec) ([]types.TeamCatalogEntry, error) {
	if spec.FlowSessionID == "" {
		return nil, fmt.Errorf("team catalog requires a flow session")
	}
	if _, err := r.loadSession(ctx, spec.FlowSessionID); err != nil {
		return nil, err
	}
	defs := r.definitions.Snapshot()
	binding, ok := defs.Flow.Teams[spec.CallerTeam]
	if !ok {
		return nil, fmt.Errorf("team %q is not configured in flow", spec.CallerTeam)
	}
	entries := make([]types.TeamCatalogEntry, 0, len(binding.CanActivate))
	for _, target := range binding.CanActivate {
		targetBinding, ok := defs.Flow.Teams[target]
		if !ok {
			continue
		}
		entry := types.TeamCatalogEntry{
			TeamID:   target,
			Receives: receivesOf(targetBinding.Inputs),
		}
		if team, found := defs.Teams[targetBinding.TeamID]; found {
			entry.Responsibility = teamResponsibility(team)
			entry.Produces = team.Output.Record
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// receivesOf lists what a binding lets the target read, using the same words the
// configuration uses so a caller can match it against what it just produced.
func receivesOf(inputs types.InputSpec) []string {
	receives := make([]string, 0, len(inputs.FlowRecords)+1)
	if inputs.UserMessage {
		receives = append(receives, "user_message")
	}
	receives = append(receives, inputs.FlowRecords...)
	return receives
}

// teamResponsibility returns the Team's own statement of what it does. Calls are
// a map, so the smallest id wins: the entry call is the one that describes the
// Team, and this keeps the answer stable across runs.
func teamResponsibility(team types.Team) string {
	if strings.TrimSpace(team.Goal) != "" {
		return strings.TrimSpace(team.Goal)
	}
	ids := make([]string, 0, len(team.Calls))
	for id := range team.Calls {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		call := team.Calls[id]
		if text := strings.TrimSpace(call.Responsibility); text != "" {
			return text
		}
	}
	return ""
}
