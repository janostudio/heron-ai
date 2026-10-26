package flow

import (
	"context"

	"encoding/json"
	"fmt"
	"github.com/heron-ai/heron-engine/internal/logging"
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
	// A caller wants the callee's answer, not its process. When the callee hands
	// work further down, its first turn ends with "I dispatched it" — returning
	// that reads as a result and invites the caller to dispatch again. Wait for
	// the callee to settle, then report what it actually concluded.
	if teamCallStatus(execution) == "dispatched" {
		if settled := r.awaitTeamSettlement(ctx, session.ID, spec.TeamID); settled != nil {
			execution.result.Reply = settled.Reply
			execution.result.Error = settled.Error

			execution.next = nil
		}
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

// teamSettleLimit bounds how long a call waits for its callee to conclude after
// the callee has itself handed work further down.
const teamSettleLimit = 5 * time.Minute

// awaitTeamSettlement waits until the callee stops dispatching and returns what
// it finally concluded.
//
// The callee's follow-up turns are started by the result callback, not by this
// goroutine, so there is nothing to join on: poll the callee's own pending calls
// and read its last conclusion from the event stream when they clear.
func (r *Runtime) awaitTeamSettlement(ctx context.Context, sessionID, teamID string) *types.TeamCallOutcome {
	deadline := time.Now().Add(teamSettleLimit)
	for {
		if !r.hasPendingTeamCallFrom(ctx, sessionID, teamID) {
			if reply, ok := r.lastTeamReply(ctx, sessionID, teamID); ok {
				return &types.TeamCallOutcome{Status: "done", Reply: reply}
			}
			return nil
		}
		if time.Now().After(deadline) {
			logging.Info("team call settled by timeout", map[string]any{
				"flow_session_id": sessionID,
				"team_id":         teamID,
			})
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// hasPendingTeamCallFrom reports whether this Team still has a call in flight.
func (r *Runtime) hasPendingTeamCallFrom(ctx context.Context, sessionID, callerTeam string) bool {
	if r.tasks == nil || sessionID == "" {
		return false
	}
	tasks, err := r.tasks.List(ctx)
	if err != nil {
		return false
	}
	for _, task := range tasks {
		if task.FlowSessionID != sessionID || task.ToolName != types.ToolNameTeamRun {
			continue
		}
		if task.Status != types.ToolTaskQueued && task.Status != types.ToolTaskRunning {
			continue
		}
		if caller, _ := task.Arguments["caller_team"].(string); caller == callerTeam {
			return true
		}
	}
	return false
}

// lastTeamReply reads the Team's most recent conclusion from the event stream.
func (r *Runtime) lastTeamReply(ctx context.Context, sessionID, teamID string) (string, bool) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return "", false
	}
	reply := ""
	for _, event := range events {
		switch event.Type {
		case types.EventFlowTurnWaitingInput, types.EventFlowTurnCompleted:
			if event.TeamID != "" && event.TeamID != teamID {
				continue
			}
			if text := payloadString(event.Payload, "reply"); strings.TrimSpace(text) != "" {
				reply = text
			}
		}
	}
	return reply, reply != ""
}
