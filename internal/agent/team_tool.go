package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TeamToolName is the tool a model calls to hand work to another Team.
//
// Collaboration is an asynchronous tool, not a separate messaging mechanism:
// the call returns a task handle immediately, the caller's loop ends, the user
// can keep talking, and the Team's answer comes back as a tool result in the
// same conversation. Every existing async-tool property applies unchanged
// (durable task store, completion callback, recovery).
const TeamToolName = "Team"

// TeamRunToolName is the internal durable task that actually runs the Team. It
// is never registered in the tool registry, so a model cannot call it: only a
// task created by TeamTool reaches this name.
const TeamRunToolName = "TeamRun"

// TeamTool exposes Team calls to the model.
type TeamTool struct {
	runner types.TeamRunner
	tasks  *AsyncToolExecutor
}

func NewTeamTool(runner types.TeamRunner) *TeamTool {
	return &TeamTool{runner: runner}
}

// SetRunner wires the executor after the flow runtime exists. The Flow runtime
// is built after the tool registry, so this cannot be a constructor argument.
func (t *TeamTool) SetRunner(runner types.TeamRunner) {
	if t == nil {
		return
	}
	t.runner = runner
}

func (t *TeamTool) SetTaskRunner(runner *AsyncToolExecutor) {
	if t == nil {
		return
	}
	t.tasks = runner
}

func (t *TeamTool) Name() string { return TeamToolName }

func (t *TeamTool) Description() string {
	return "Hand work to another Team and return immediately with a task handle. " +
		"The Team runs in the background; its answer arrives later as the result of this call. " +
		"Use it for work that belongs to another Team's responsibility instead of doing it yourself."
}

func (t *TeamTool) Parameters() map[string]any {
	return map[string]any{
		"team_id": map[string]any{
			"type":        "string",
			"description": "Target Team id from the flow definition",
		},
		"item": map[string]any{
			"type":        "any",
			"description": "The task handed to that Team: what to find out or do, with the context it needs",
		},
	}
}

func (t *TeamTool) NeedsApproval() bool { return false }

// Execute never runs the Team inline. It records a durable task and returns the
// handle, so the caller's loop can finish and the user keeps control until the
// answer is ready.
func (t *TeamTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.runner == nil {
		return teamError("Team tool is not configured"), nil
	}
	teamID := strings.TrimSpace(stringParam(params, "team_id"))
	if teamID == "" {
		return teamError("Team requires a team_id"), nil
	}
	item, ok := params["item"]
	if !ok || item == nil {
		return teamError("Team requires an item describing the task"), nil
	}
	spec := teamCallSpecFromContext(ctx, teamID, item)

	if t.tasks == nil {
		// No durable task runner (unit tests, or an engine built without one):
		// run inline rather than pretending to accept work nobody will finish.
		outcome, err := t.runner.RunTeam(ctx, spec)
		if err != nil {
			return teamError(err.Error()), nil
		}
		return teamOutcomeResult(outcome), nil
	}

	task := types.ToolTask{
		ID:            teamTaskID(spec),
		FlowSessionID: spec.FlowSessionID,
		TeamID:        spec.CallerTeam,
		CallID:        spec.CallID,
		AgentTurnID:   spec.AgentTurnID,
		ToolName:      TeamRunToolName,
		Arguments:     teamRunArguments(spec),
		// A Team call has side effects; an interrupted execution is failed on
		// recovery rather than silently re-run.
		RestartSafe: false,
	}
	if err := t.tasks.Start(ctx, task); err != nil {
		return teamError(fmt.Sprintf("start Team call: %v", err)), nil
	}
	content, _ := json.Marshal(map[string]any{
		"status":  "accepted",
		"task_id": task.ID,
		"team_id": spec.TeamID,
	})
	return &types.ToolResult{
		Success: true,
		Content: string(content),
		Metadata: map[string]any{
			"team_id": spec.TeamID,
			"wait":    false,
		},
	}, nil
}

// executeRunTask is the body of the durable task: it runs the Team and turns
// its outcome into the tool result the caller will see.
func (t *TeamTool) executeRunTask(ctx context.Context, args map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.runner == nil {
		return teamError("Team tool is not configured"), nil
	}
	spec := types.TeamCallSpec{
		FlowSessionID: stringParam(args, "flow_session_id"),
		CallerTeam:    stringParam(args, "caller_team"),
		TeamID:        stringParam(args, "team_id"),
		CallID:        stringParam(args, "call_id"),
		AgentTurnID:   stringParam(args, "agent_turn_id"),
		Item:          args["item"],
	}
	if spec.TeamID == "" {
		return teamError("Team call is missing team_id"), nil
	}
	outcome, err := t.runner.RunTeam(ctx, spec)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return teamOutcomeResult(outcome), nil
}

func teamOutcomeResult(outcome types.TeamCallOutcome) *types.ToolResult {
	payload := map[string]any{"status": outcome.Status, "reply": outcome.Reply}
	if len(outcome.Records) > 0 {
		payload["records"] = outcome.Records
	}
	if outcome.Error != "" {
		payload["error"] = outcome.Error
	}
	content, _ := json.Marshal(payload)
	return &types.ToolResult{
		Success:  outcome.Error == "",
		Content:  string(content),
		Error:    outcome.Error,
		Metadata: map[string]any{"team_id": outcome.Status},
	}
}

// teamCallSpecFromContext fills the caller coordinates from the turn's identity.
func teamCallSpecFromContext(ctx context.Context, teamID string, item any) types.TeamCallSpec {
	spec := types.TeamCallSpec{TeamID: teamID, Item: item}
	if identity := spawnIdentityFromContext(ctx); identity != nil {
		spec.FlowSessionID = identity.req.FlowSessionID
		spec.CallerTeam = identity.req.TeamID
		spec.CallID = identity.req.CallID
		spec.AgentTurnID = identity.req.AgentTurnID
	}
	return spec
}

func teamRunArguments(spec types.TeamCallSpec) map[string]any {
	return map[string]any{
		"flow_session_id": spec.FlowSessionID,
		"caller_team":     spec.CallerTeam,
		"team_id":         spec.TeamID,
		"call_id":         spec.CallID,
		"agent_turn_id":   spec.AgentTurnID,
		"item":            spec.Item,
	}
}

func teamTaskID(spec types.TeamCallSpec) string {
	base := spec.AgentTurnID
	if base == "" {
		base = spec.FlowSessionID
	}
	if base == "" {
		base = "team"
	}
	return fmt.Sprintf("%s:team:%s:%d", base, spec.TeamID, spawnTurnSeq.Add(1))
}

func teamError(message string) *types.ToolResult {
	return &types.ToolResult{Success: false, Error: message}
}

// TeamTaskDispatcher routes the internal TeamRun task name to the Team tool and
// forwards everything else unchanged.
//
// TeamRun is never registered in the Tool registry, so models cannot invoke it;
// only a durable task created by TeamTool reaches this path.
type TeamTaskDispatcher struct {
	team *TeamTool
	base ToolExecutor
}

func NewTeamTaskDispatcher(team *TeamTool, base ToolExecutor) *TeamTaskDispatcher {
	return &TeamTaskDispatcher{team: team, base: base}
}

func (d *TeamTaskDispatcher) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if d != nil && name == TeamRunToolName {
		if d.team == nil {
			return teamError("Team tool is not configured"), nil
		}
		return d.team.executeRunTask(ctx, args)
	}
	if d == nil || d.base == nil {
		return &types.ToolResult{Success: false, Error: "tool executor is not configured"}, nil
	}
	return d.base.Execute(ctx, name, args)
}

// ExecutionSpec keeps the async executor's restart classification working for
// tasks routed through the dispatcher.
func (d *TeamTaskDispatcher) ExecutionSpec(name string) types.ToolExecutionSpec {
	if name == TeamRunToolName {
		return types.ToolExecutionSpec{Class: types.ToolSerial, RestartSafe: false}
	}
	if d != nil && d.base != nil {
		if inspector, ok := d.base.(toolExecutionInspector); ok {
			return inspector.ExecutionSpec(name)
		}
	}
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}
