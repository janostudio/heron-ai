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
const TeamRunToolName = types.ToolNameTeamRun

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
	return "Hand work to another Team. Runs in the background and returns a task handle immediately; " +
		"the Team's answer arrives later as the result of this call. " +
		"Use it for work that belongs to another Team's responsibility instead of doing it yourself."
}

func (t *TeamTool) Parameters() map[string]any {
	return map[string]any{
		"description": map[string]any{
			"type":        "string",
			"description": "A short (3-5 word) description of the task, shown to the user while it runs",
		},
		"prompt": map[string]any{
			"type":        "string",
			"description": "The task for the Team to perform: goal, evidence, scope, acceptance criteria, authorization",
		},
		"subagent_type": map[string]any{
			"type":        "string",
			"description": "The Team to hand the work to",
		},
		"task_key": map[string]any{
			"type":        "string",
			"description": "Business key of this task (e.g. repoA-143). Required when the target Team uses lifecycle per_task: the same key continues the same Team instance later.",
		},
		"run_in_background": map[string]any{
			"type":        "boolean",
			"description": "Run the Team without blocking this turn (default true)",
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
	teamID := strings.TrimSpace(stringParam(params, "subagent_type"))
	if teamID == "" {
		return teamError("Team requires a subagent_type (the Team to hand the work to)"), nil
	}
	promptText := strings.TrimSpace(stringParam(params, "prompt"))
	if promptText == "" {
		return teamError("Team requires a prompt describing the task"), nil
	}
	summary := strings.TrimSpace(stringParam(params, "description"))
	item := any(promptText)
	// Reach is the Flow's decision, not the model's: a Team may hand work only
	// to the targets its binding declares. The schema narrows the choice; this
	// check is the enforcement behind it, because a model can still write a
	// name the schema never offered.
	if identity := spawnIdentityFromContext(ctx); identity != nil && len(identity.req.AllowedTeams) > 0 {
		if !containsTeam(identity.req.AllowedTeams, teamID) {
			return teamError(fmt.Sprintf(
				"team %q is not reachable from %q; this Team may hand work to: %s",
				teamID, identity.req.TeamID, strings.Join(identity.req.AllowedTeams, ", "))), nil
		}
	}
	spec := teamCallSpecFromContext(ctx, teamID, item)
	spec.Summary = summary
	spec.TaskKey = strings.TrimSpace(stringParam(params, "task_key"))

	if t.tasks == nil {
		// No durable task runner (unit tests, or an engine built without one):
		// run inline rather than pretending to accept work nobody will finish.
		outcome, err := t.runner.RunTeam(ctx, spec)
		if err != nil {
			return teamError(err.Error()), nil
		}
		return teamOutcomeResult(spec.TeamID, outcome), nil
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
	// Say what happens next in the same terms mature subagent tools use: the
	// caller is notified, it must not wait. "Its answer will arrive" read as
	// "this call is still open", and the caller sat in `sleep` loops waiting
	// for it instead of ending the turn.
	content := fmt.Sprintf(
		"Handed to %s. It runs in the background; you will be notified when it finishes. "+
			"Do not wait for it and do not re-dispatch the same task.",
		spec.TeamID,
	)
	if spec.TaskKey != "" {
		content += fmt.Sprintf("\n\n(handle: task_key=%s — keep it to continue the same task)", spec.TaskKey)
	}
	metadata := map[string]any{
		"team_id": spec.TeamID,
		"wait":    false,
	}
	if spec.TaskKey != "" {
		metadata["task_key"] = spec.TaskKey
	}
	return &types.ToolResult{
		Success:  true,
		Content:  string(content),
		Metadata: metadata,
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
	rawDispatchedBy, _ := args["dispatched_by"].(map[string]any)
	spec := types.TeamCallSpec{
		FlowSessionID: stringParam(args, "flow_session_id"),
		CallerTeam:    stringParam(args, "caller_team"),
		TeamID:        stringParam(args, "team_id"),
		CallID:        stringParam(args, "call_id"),
		AgentTurnID:   stringParam(args, "agent_turn_id"),
		TaskKey:       stringParam(args, "task_key"),
		DispatchedBy:  dispatchedByFromArguments(rawDispatchedBy),
		Item:          args["item"],
	}
	if spec.TeamID == "" {
		return teamError("Team call is missing team_id"), nil
	}
	outcome, err := t.runner.RunTeam(ctx, spec)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	return teamOutcomeResult(spec.TeamID, outcome), nil
}

// teamOutcomeResult returns the Team's answer as text.
//
// A subagent tool's result is the subagent's final message, not a status
// envelope. Wrapping it in {"status":...,"reply":...} made the model read a
// finished answer as a task state and keep dispatching ("wait for the result",
// "get the final conclusion") — six calls for one question in one session.
func teamOutcomeResult(teamID string, outcome types.TeamCallOutcome) *types.ToolResult {
	content := strings.TrimSpace(outcome.Reply)
	switch {
	case outcome.Error != "":
		content = fmt.Sprintf("%s could not complete the task: %s", teamID, outcome.Error)
	case content == "":
		content = fmt.Sprintf("%s finished without a written answer.", teamID)
	}
	// Record names stay visible as a short footer: a caller quoting its evidence
	// reads them, while the body remains an answer.
	if len(outcome.Records) > 0 {
		content += fmt.Sprintf("\n\n(records: %s)", strings.Join(outcome.Records, ", "))
	}
	// The handle is the one thing a caller must keep to continue the same task
	// later: the tool result is the only place it is handed over.
	if outcome.TaskKey != "" || outcome.TeamSessionID != "" {
		content += fmt.Sprintf("\n\n(handle: task_key=%s team_session_id=%s", outcome.TaskKey, outcome.TeamSessionID)
		if outcome.Reopened {
			content += " reopened=true"
		}
		content += ")"
	}
	metadata := map[string]any{"status": outcome.Status, "team_id": teamID}
	if outcome.TaskKey != "" {
		metadata["task_key"] = outcome.TaskKey
	}
	if outcome.TeamSessionID != "" {
		metadata["team_session_id"] = outcome.TeamSessionID
	}
	if outcome.Reopened {
		metadata["reopened"] = true
	}
	return &types.ToolResult{
		Success:  outcome.Error == "",
		Content:  content,
		Error:    outcome.Error,
		Metadata: metadata,
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
		if identity.req.TeamSessionID != "" {
			spec.DispatchedBy = &types.TeamSessionRef{
				TeamSessionID: identity.req.TeamSessionID,
				CallID:        identity.req.CallID,
				AgentTurnID:   identity.req.AgentTurnID,
			}
		}
	}
	return spec
}

func teamRunArguments(spec types.TeamCallSpec) map[string]any {
	arguments := map[string]any{
		"flow_session_id": spec.FlowSessionID,
		"caller_team":     spec.CallerTeam,
		"team_id":         spec.TeamID,
		"call_id":         spec.CallID,
		"agent_turn_id":   spec.AgentTurnID,
		"task_key":        spec.TaskKey,
		"item":            spec.Item,
	}
	if spec.DispatchedBy != nil {
		arguments["dispatched_by"] = map[string]any{
			"team_session_id": spec.DispatchedBy.TeamSessionID,
			"call_id":         spec.DispatchedBy.CallID,
			"agent_turn_id":   spec.DispatchedBy.AgentTurnID,
			"tool_call_id":    spec.DispatchedBy.ToolCallID,
		}
	}
	return arguments
}

// dispatchedByFromArguments rebuilds the caller reference persisted with a
// durable TeamRun task. Values arrive as float64/string after the JSON round
// trip through the task store.
func dispatchedByFromArguments(raw map[string]any) *types.TeamSessionRef {
	if raw == nil {
		return nil
	}
	ref := &types.TeamSessionRef{
		TeamSessionID: stringFromArgs(raw, "team_session_id"),
		CallID:        stringFromArgs(raw, "call_id"),
		AgentTurnID:   stringFromArgs(raw, "agent_turn_id"),
		ToolCallID:    stringFromArgs(raw, "tool_call_id"),
	}
	if ref.TeamSessionID == "" {
		return nil
	}
	return ref
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

func containsTeam(teams []string, target string) bool {
	for _, team := range teams {
		if team == target {
			return true
		}
	}
	return false
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

// TeamSessionToolName is the tool a model calls to read the tasks it dispatched.
//
// It is the read side of the routing table: the model keeps a task_key (not a
// session id), and the engine answers what happened to that task. Keeping the
// lookup here is what lets a restarted or compacted conversation continue a
// task without replaying its own history.
const TeamSessionToolName = "TeamSession"

// TeamSessionTool answers status/list/close requests about dispatched tasks.
type TeamSessionTool struct {
	catalog types.TaskCatalog
	closer  types.TaskCloser
}

func NewTeamSessionTool(catalog types.TaskCatalog) *TeamSessionTool {
	return &TeamSessionTool{catalog: catalog}
}

// SetCatalog wires the flow runtime after it exists.
func (t *TeamSessionTool) SetCatalog(catalog types.TaskCatalog) {
	if t == nil {
		return
	}
	t.catalog = catalog
}

// SetCloser wires the soft-close path (same runtime, separate interface: the
// read side stays usable without it).
func (t *TeamSessionTool) SetCloser(closer types.TaskCloser) {
	if t == nil {
		return
	}
	t.closer = closer
}

func (t *TeamSessionTool) Name() string { return TeamSessionToolName }

func (t *TeamSessionTool) Description() string {
	return "Read or close the tasks you dispatched: status of one task by task_key, the list of " +
		"all tasks this Team instance dispatched, or a soft close (records and transcript are kept; " +
		"dispatching the same task_key again reopens it)."
}

func (t *TeamSessionTool) Parameters() map[string]any {
	return map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"status", "list", "tree", "close"},
			"description": "status: one task by task_key; list: every task this instance dispatched; tree: the same tasks grouped by task context; close: soft-close a task and its sub-tasks",
		},
		"task_key": map[string]any{
			"type":        "string",
			"description": "Business key of the task (required for action=status|close)",
		},
	}
}

func (t *TeamSessionTool) NeedsApproval() bool { return false }

func (t *TeamSessionTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.catalog == nil {
		return teamError("TeamSession is not configured"), nil
	}
	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return teamError("TeamSession is only available inside an Agent execution context"), nil
	}
	spec := types.TaskCatalogSpec{
		FlowSessionID:       identity.req.FlowSessionID,
		CallerTeamSessionID: identity.req.TeamSessionID,
	}
	if spec.CallerTeamSessionID == "" {
		return teamError("TeamSession requires the calling Team instance"), nil
	}

	switch action := strings.TrimSpace(stringParam(params, "action")); action {
	case "status":
		taskKey := strings.TrimSpace(stringParam(params, "task_key"))
		if taskKey == "" {
			return teamError("TeamSession(action=status) requires task_key"), nil
		}
		info, err := t.catalog.GetTask(ctx, spec, taskKey)
		if err != nil {
			return teamError(err.Error()), nil
		}
		return taskJSON(info)
	case "list":
		infos, err := t.catalog.ListTasks(ctx, spec)
		if err != nil {
			return teamError(err.Error()), nil
		}
		if infos == nil {
			infos = []types.TaskInfo{}
		}
		return taskJSON(infos)
	case "tree":
		trees, err := t.catalog.ListTaskTrees(ctx, spec)
		if err != nil {
			return teamError(err.Error()), nil
		}
		if trees == nil {
			trees = []types.TaskTree{}
		}
		return taskJSON(trees)
	case "close":
		if t.closer == nil {
			return teamError("TeamSession(close) is not configured"), nil
		}
		taskKey := strings.TrimSpace(stringParam(params, "task_key"))
		if taskKey == "" {
			return teamError("TeamSession(action=close) requires task_key"), nil
		}
		info, err := t.closer.CloseTask(ctx, spec, taskKey)
		if err != nil {
			return teamError(err.Error()), nil
		}
		return taskJSON(info)
	case "":
		return teamError("TeamSession requires action=status|list|close"), nil
	default:
		return teamError(fmt.Sprintf("unknown TeamSession action %q (use status, list or close)", action)), nil
	}
}

func taskJSON(value any) (*types.ToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return teamError(fmt.Sprintf("encode tasks: %v", err)), nil
	}
	return &types.ToolResult{Success: true, Content: string(data)}, nil
}

// ListTeamsToolName is the tool a model calls to discover which Teams it may
// hand work to.
//
// It is the "tool search" half of collaboration: the reach is decided by the
// Flow, and this exposes it as a catalog the model can read — what each Team is
// for, what it can read, what it produces — instead of a list baked into a
// prompt that goes stale as soon as the Flow changes.
const ListTeamsToolName = "ListTeams"

// ListTeamsTool reads the caller's view of the Flow graph.
type ListTeamsTool struct {
	catalog types.TeamCatalog
}

func NewListTeamsTool(catalog types.TeamCatalog) *ListTeamsTool {
	return &ListTeamsTool{catalog: catalog}
}

// SetCatalog wires the flow runtime after it exists.
func (t *ListTeamsTool) SetCatalog(catalog types.TeamCatalog) {
	if t == nil {
		return
	}
	t.catalog = catalog
}

func (t *ListTeamsTool) Name() string { return ListTeamsToolName }

func (t *ListTeamsTool) Description() string {
	return "List the Teams you may hand work to, with what each is for, what it can read " +
		"and what record it produces. Call this before choosing a team_id for Team."
}

func (t *ListTeamsTool) Parameters() map[string]any {
	return map[string]any{}
}

func (t *ListTeamsTool) NeedsApproval() bool { return false }

func (t *ListTeamsTool) Execute(ctx context.Context, _ map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.catalog == nil {
		return teamError("ListTeams is not configured"), nil
	}
	spec := types.TeamCatalogSpec{}
	if identity := spawnIdentityFromContext(ctx); identity != nil {
		spec.FlowSessionID = identity.req.FlowSessionID
		spec.CallerTeam = identity.req.TeamID
	}
	entries, err := t.catalog.ListTeams(ctx, spec)
	if err != nil {
		return teamError(err.Error()), nil
	}
	content, _ := json.Marshal(entries)
	return &types.ToolResult{
		Success:  true,
		Content:  string(content),
		Metadata: map[string]any{"teams": len(entries)},
	}, nil
}
