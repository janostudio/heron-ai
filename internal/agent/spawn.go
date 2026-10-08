package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Design docs 20/21: batch A added synchronous Spawn; batch B adds the
// asynchronous+parent combination (wait=false, deliver=parent). The Spawn tool
// is the single primitive for dynamic agent instances — it registers (or
// reuses) the target agent, runs child AgentTurns inline (wait=true) or as
// durable async tasks collected later with Collect (wait=false).

// SpawnChildToolName is the internal tool name of the durable task that runs
// one spawned child AgentTurn in the background. It is deliberately never
// registered in the Tool registry: models cannot invoke it, and only the
// AsyncToolExecutor's SpawnTaskDispatcher routes to it.
const SpawnChildToolName = "SpawnChild"

const (
	defaultSpawnMaxChildren = 8
	defaultSpawnMaxDepth    = 3
)

// spawnIdentity carries the currently executing AgentTurn into its Tool calls
// so Spawn can resolve its parent without widening the Tool interface.
type spawnIdentity struct {
	agent       types.AgentConfig
	req         types.AgentRequest
	instanceKey string
}

type spawnIdentityKey struct{}

func withSpawnIdentity(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) context.Context {
	return context.WithValue(ctx, spawnIdentityKey{}, &spawnIdentity{agent: agent, req: req})
}

// withSpawnInstanceKey decorates a spawn identity with the instance key of
// the current spawned child, so downstream tool calls (notably the state
// tool) can attribute entries to a specific "agent(instance)" actor. It
// copies the existing identity (never mutating the shared pointer carried in
// the parent context) so concurrent siblings each get their own instance key.
// If the context carries no spawn identity, a fresh one is created carrying
// only the instance key.
func withSpawnInstanceKey(ctx context.Context, instanceKey string) context.Context {
	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return context.WithValue(ctx, spawnIdentityKey{}, &spawnIdentity{instanceKey: instanceKey})
	}
	copied := *identity
	copied.instanceKey = instanceKey
	return context.WithValue(ctx, spawnIdentityKey{}, &copied)
}

func spawnIdentityFromContext(ctx context.Context) *spawnIdentity {
	if ctx == nil {
		return nil
	}
	identity, _ := ctx.Value(spawnIdentityKey{}).(*spawnIdentity)
	return identity
}

// Spawn depth lives in agentstore so the Team runtime's synthetic calls share
// the same nesting accounting with inline and durable children.
func withSpawnDepth(ctx context.Context, depth int) context.Context {
	return agentstore.WithSpawnDepth(ctx, depth)
}

func spawnDepthFromContext(ctx context.Context) int {
	return agentstore.SpawnDepthFromContext(ctx)
}

// spawnTurnSeq makes every spawned child AgentTurnID unique, because
// checkpoint and approval IDs derive from it.
var spawnTurnSeq atomic.Int64

// SpawnTool implements the builtin Spawn tool (design doc 20 §2). wait=true
// executes children inline; wait=false starts each child asynchronously —
// deliver=parent hands back durable task handles for a later Collect, while
// deliver=downstream registers the child as a synthetic call in the parent
// call's Team group through the insertion channel in ctx. The tool is
// registered in the tool registry and is only usable by Agents that declare
// it in tools.builtin.
type SpawnTool struct {
	runner      AgentRunner
	agents      map[string]types.AgentConfig
	registry    *agentstore.Registry
	states      *state.Store
	tasks       *AsyncToolExecutor
	sessions    storage.SessionWriter
	maxChildren int
	maxDepth    int
}

// SpawnOption configures a SpawnTool.
type SpawnOption func(*SpawnTool)

// WithSpawnMaxChildren bounds both the children of one Spawn call and their
// parallelism (decision D5/D6). Default 8.
func WithSpawnMaxChildren(max int) SpawnOption {
	return func(t *SpawnTool) {
		if max > 0 {
			t.maxChildren = max
		}
	}
}

// WithSpawnMaxDepth bounds recursive spawning depth. Default 3.
func WithSpawnMaxDepth(max int) SpawnOption {
	return func(t *SpawnTool) {
		if max > 0 {
			t.maxDepth = max
		}
	}
}

// NewSpawnTool creates the Spawn tool. runner is the Agent execution path used
// for child turns (typically the same TurnLoop that executes the parent);
// agents resolves agent definitions by id; registry and states persist
// dynamic entities and their state.
func NewSpawnTool(
	runner AgentRunner,
	agents map[string]types.AgentConfig,
	registry *agentstore.Registry,
	states *state.Store,
	options ...SpawnOption,
) *SpawnTool {
	spawn := &SpawnTool{
		runner:      runner,
		agents:      agents,
		registry:    registry,
		states:      states,
		maxChildren: defaultSpawnMaxChildren,
		maxDepth:    defaultSpawnMaxDepth,
	}
	for _, option := range options {
		option(spawn)
	}
	return spawn
}

// SetAgentStateLocks is retained for API compatibility. Since design doc 26
// the turn lock has been removed: same-agent instances run concurrently and
// their shared state is protected by the agent-level CRUD lock inside the
// state Store.
func (t *SpawnTool) SetAgentStateLocks(locks *agentstore.AgentStateLocks) {}

// SetTaskRunner wires the durable async task executor used by wait=false
// spawns. It must be set before asynchronous Spawn can run.
func (t *SpawnTool) SetTaskRunner(runner *AsyncToolExecutor) {
	t.tasks = runner
}

// SetSessionWriter wires the optional per-layer session writer used to emit
// agent-level events for spawned child turns (they land in team.jsonl), so
// child consumption (agent_turn.completed → payload.call_result.Requests[])
// is recorded in the same fact source as ordinary Agent turns.
func (t *SpawnTool) SetSessionWriter(writer storage.SessionWriter) {
	t.sessions = writer
}

func (t *SpawnTool) Name() string { return "Spawn" }

func (t *SpawnTool) Description() string {
	return "Spawn dynamic agent instances and execute them. wait=true blocks until children finish; " +
		"wait=false returns handles immediately — deliver=parent children are collected later with Collect, " +
		"deliver=downstream children join your call's Team group and publish records for downstream calls. " +
		"Each item is delivered to its child as ## Your Item; all instances of an agent share one cross-session state."
}

func (t *SpawnTool) NeedsApproval() bool { return false }

func (t *SpawnTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}

func (t *SpawnTool) Parameters() map[string]any {
	return map[string]any{
		"agent": map[string]any{
			"type":        "string",
			"description": "Target agent id; defaults to the spawning agent itself",
		},
		"item": map[string]any{
			"type":        "any",
			"description": "Single task item (any JSON value) delivered to the child instance",
		},
		"items": map[string]any{
			"type":        "array",
			"description": "Multiple task items; one child instance per item, executed in parallel",
		},
		"wait": map[string]any{
			"type":        "boolean",
			"description": "true: block until children finish; false: return handles immediately (deliver=parent collects later with Collect; deliver=downstream children run in the Team DAG)",
		},
		"deliver": map[string]any{
			"type":        "string",
			"enum":        []string{"parent", "downstream"},
			"description": "parent: results return to you; downstream: results are published as records of your call (downstream calls wait for you and all your spawned children)",
		},
		"key": map[string]any{
			"type":        "string",
			"description": "Instance key to reuse a given instance number; only valid with a single item",
		},
	}
}

// spawnOutcome is the aggregated result of one spawned child.
type spawnOutcome struct {
	Key    string
	Status types.TurnStatus
	Reply  string
	Error  string
	Usage  types.TokenUsage
	// Requests carries the child's model request stats so child consumption
	// can be emitted as agent-level session events (the fact source for the
	// consumption model).
	Requests []types.ModelRequestStats
}

func spawnError(message string) *types.ToolResult {
	return &types.ToolResult{Success: false, Error: message, Content: message}
}

func (t *SpawnTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if t == nil || t.runner == nil || t.registry == nil {
		return spawnError("Spawn tool is not configured"), nil
	}

	agentID, _ := params["agent"].(string)
	item, hasItem := params["item"]
	rawItems, hasItems := params["items"]
	wait := true
	if value, ok := params["wait"].(bool); ok {
		wait = value
	}
	deliver := "parent"
	if value, ok := params["deliver"].(string); ok && strings.TrimSpace(value) != "" {
		deliver = strings.TrimSpace(value)
	}
	key, _ := params["key"].(string)

	var items []any
	switch {
	case hasItem && hasItems:
		return spawnError("Spawn accepts either item or items, not both"), nil
	case hasItem:
		items = []any{item}
	case hasItems:
		array, ok := rawItems.([]any)
		if !ok {
			return spawnError("Spawn items must be an array"), nil
		}
		if len(array) == 0 {
			return spawnError("Spawn items must not be empty; an empty spawn hides business bugs"), nil
		}
		items = array
	default:
		return spawnError("Spawn requires item or items"), nil
	}
	if deliver != "parent" && deliver != "downstream" {
		return spawnError(`Spawn deliver must be "parent" or "downstream"`), nil
	}
	if strings.TrimSpace(key) != "" && len(items) > 1 {
		return spawnError("Spawn key is only valid with a single item; items spawn one instance per item"), nil
	}

	identity := spawnIdentityFromContext(ctx)
	if identity == nil {
		return spawnError("Spawn is not available outside an Agent execution context"), nil
	}
	parent := identity.req

	targetAgentID := strings.TrimSpace(agentID)
	if targetAgentID == "" {
		targetAgentID = parent.AgentID
	}
	if targetAgentID == "" {
		targetAgentID = identity.agent.Name
	}
	targetDef, defined := t.agents[targetAgentID]
	if !defined {
		return spawnError(fmt.Sprintf("Spawn agent %q is not defined", targetAgentID)), nil
	}

	if len(items) > t.maxChildren {
		return spawnError(fmt.Sprintf("Spawn of %d children exceeds the limit of %d", len(items), t.maxChildren)), nil
	}
	depth := spawnDepthFromContext(ctx)
	childDepth := depth + 1
	if childDepth > t.maxDepth {
		return spawnError(fmt.Sprintf("Spawn depth %d exceeds the limit of %d", childDepth, t.maxDepth)), nil
	}

	if !wait {
		// wait=false + deliver=downstream (dynamic DAG insertion, design 21
		// §4.4) joins the parent call's Team group; deliver=parent runs the
		// child as a durable task collected later.
		if deliver != "parent" {
			return t.executeAsyncDownstream(ctx, targetAgentID, parent, items, key, childDepth)
		}
		return t.executeAsync(ctx, targetAgentID, parent, items, key, childDepth)
	}

	var collector *agentstore.RecordCollector
	if deliver == "downstream" {
		collector = agentstore.RecordCollectorFromContext(ctx)
		if collector == nil {
			return spawnError("Spawn deliver=downstream requires a call record collector"), nil
		}
		if !collector.Enabled() {
			return spawnError("Spawn deliver=downstream requires the parent call to configure output.record"), nil
		}
	}

	outcomes := make([]*spawnOutcome, len(items))
	semaphore := make(chan struct{}, t.maxChildren)
	var wg sync.WaitGroup
	for i := range items {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			outcomes[index] = t.runChild(ctx, targetAgentID, targetDef, parent, items[index], key, childDepth)
		}(i)
	}
	wg.Wait()

	failed := 0
	firstError := ""
	usage := types.TokenUsage{}
	for _, outcome := range outcomes {
		if outcome == nil || outcome.Error != "" {
			failed++
			if firstError == "" && outcome != nil {
				firstError = outcome.Error
			}
			continue
		}
		usage.PromptTokens += outcome.Usage.PromptTokens
		usage.CompletionTokens += outcome.Usage.CompletionTokens
		usage.ReasoningTokens += outcome.Usage.ReasoningTokens
		usage.TotalTokens += outcome.Usage.TotalTokens
	}

	var content []byte
	if deliver == "parent" {
		entries := make([]map[string]any, len(outcomes))
		for i, outcome := range outcomes {
			entries[i] = spawnOutcomeEntry(outcome, true)
		}
		content, _ = json.Marshal(entries)
	} else {
		// downstream: full results go into records; the parent only sees a
		// compact completion summary (design 20 §2.1 — order without content).
		for i, outcome := range outcomes {
			data := spawnOutcomeEntry(outcome, true)
			data["agent"] = targetAgentID
			data["item"] = items[i]
			summary := ""
			if outcome != nil {
				summary = outcome.Reply
			}
			collector.Add("spawn_result", summary, data)
		}
		entries := make([]map[string]any, len(outcomes))
		for i, outcome := range outcomes {
			entries[i] = spawnOutcomeEntry(outcome, false)
		}
		content, _ = json.Marshal(entries)
	}

	result := &types.ToolResult{
		Success: failed == 0,
		Content: string(content),
		Metadata: map[string]any{
			"agent":    targetAgentID,
			"deliver":  deliver,
			"children": len(outcomes),
			"depth":    childDepth,
			"usage": map[string]any{
				"prompt_tokens":     usage.PromptTokens,
				"completion_tokens": usage.CompletionTokens,
				"total_tokens":      usage.TotalTokens,
			},
		},
	}
	if failed > 0 {
		result.Error = fmt.Sprintf("%d of %d spawned children failed", failed, len(outcomes))
		if firstError != "" {
			result.Error += ": " + firstError
		}
	}
	return result, nil
}

func spawnOutcomeEntry(outcome *spawnOutcome, includeReply bool) map[string]any {
	entry := map[string]any{}
	if outcome == nil {
		entry["error"] = "child produced no outcome"
		return entry
	}
	entry["key"] = outcome.Key
	entry["status"] = string(outcome.Status)
	if includeReply && outcome.Reply != "" {
		entry["reply"] = outcome.Reply
	}
	if outcome.Error != "" {
		entry["error"] = outcome.Error
	}
	return entry
}

// runChild ensures the instance exists, executes one child AgentTurn inline,
// and persists the agent state afterwards. The child context carries the
// spawn depth (and inherits the record collector) for nested spawning. Child
// turns emit agent-level session events so their consumption lands in the
// same team.jsonl fact source as ordinary agent turns.
func (t *SpawnTool) runChild(
	ctx context.Context,
	agentID string,
	def types.AgentConfig,
	parent types.AgentRequest,
	item any,
	key string,
	depth int,
) *spawnOutcome {
	childCtx := withSpawnDepth(ctx, depth)

	instance, err := t.registry.NextInstanceKey(childCtx, agentID, key)
	if err != nil {
		return &spawnOutcome{Key: key, Error: err.Error()}
	}

	// Attribute subsequent tool calls (state CRUD) to this specific instance
	// so the shared whiteboard can record who added/handled each entry.
	childCtx = withSpawnInstanceKey(childCtx, instance.Key)

	itemJSON, err := json.Marshal(item)
	if err != nil {
		return &spawnOutcome{Key: instance.Key, Error: fmt.Sprintf("encode item: %v", err)}
	}

	childCall := childCallID(parent.CallID, instance.Key)
	childTurn := childTurnID(parent, instance.Key)

	blocks := []types.ContextBlock{{
		Kind:      "fanout_item",
		Text:      string(itemJSON),
		Source:    "spawn",
		Stability: "dynamic",
		Priority:  85,
	}}

	snapshot, err := t.states.LoadAgentState(childCtx, agentID)
	if err != nil {
		return &spawnOutcome{Key: instance.Key, Error: err.Error()}
	}
	stateText := renderAgentState(snapshot)
	if stateText != "" {
		blocks = append(blocks, types.ContextBlock{
			Kind:         "agent_state",
			Text:         stateText,
			Source:       "agent_state",
			Stability:    "dynamic",
			Priority:     60,
			Compressible: true,
		})
	}

	t.emitChildEvent(childCtx, types.EventAgentTurnStarted, agentID, instance.Key, parent, childCall, childTurn, nil)
	var outcome *spawnOutcome
	defer func() {
		t.emitChildEvent(childCtx, types.EventAgentTurnCompleted, agentID, instance.Key, parent, childCall, childTurn, outcome)
	}()

	result, err := t.runner.Run(childCtx, def, types.AgentRequest{
		FlowSessionID:    parent.FlowSessionID,
		TeamID:           parent.TeamID,
		TeamTurnID:       parent.TeamTurnID,
		CallID:           childCall,
		CallTurnID:       parent.CallTurnID,
		AgentID:          agentID,
		AgentTurnID:      childTurn,
		ContextBlocks:    blocks,
		MaxAgentRounds:   parent.MaxAgentRounds,
		MaxParallelTools: parent.MaxParallelTools,
	})
	if err != nil {
		outcome = &spawnOutcome{Key: instance.Key, Status: types.TurnFailed, Error: err.Error()}
		return outcome
	}
	if result == nil {
		outcome = &spawnOutcome{Key: instance.Key, Status: types.TurnFailed, Error: "child agent returned a nil result"}
		return outcome
	}
	outcome = &spawnOutcome{
		Key:      instance.Key,
		Status:   result.Status,
		Reply:    result.Reply,
		Usage:    result.Usage,
		Requests: result.Requests,
	}
	if result.Error != "" {
		outcome.Error = result.Error
		return outcome
	}
	if result.Status != types.TurnCompleted {
		outcome.Error = fmt.Sprintf("child ended with status %s", result.Status)
		return outcome
	}
	if err := t.saveAgentState(childCtx, agentID, string(itemJSON), stateText, result); err != nil {
		outcome.Error = fmt.Sprintf("save agent state: %v", err)
	}
	return outcome
}

// emitChildEvent appends one agent-level session event for a spawned child
// turn. The event reuses the ordinary agent_turn.* structure; the producer is
// distinguished by CallID "<parent-call>/<key>" and a payload.spawn block
// carrying the instance agent/key. Emission is skipped when no session writer
// is wired or the parent has no flow session to write into.
//
// This event — not the parent's CallResult — is where a child's consumption is
// recorded. A child is its own agent turn, so its tokens are published under
// its own CallID and the parent's CallResult.Usage keeps covering only the
// parent's own model rounds. Do not "fix" this by summing child usage into the
// parent: the child's event already carries it, so the event stream (the
// declared fact source) would count it twice, and a wait=false child may not
// even have finished by the time the parent's result is sealed.
func (t *SpawnTool) emitChildEvent(
	ctx context.Context,
	eventType string,
	agentID, key string,
	parent types.AgentRequest,
	childCall, childTurn string,
	outcome *spawnOutcome,
) {
	if t == nil || t.sessions == nil || strings.TrimSpace(parent.FlowSessionID) == "" {
		return
	}
	event := storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          eventType,
			FlowSessionID: parent.FlowSessionID,
			TeamID:        parent.TeamID,
			TeamTurnID:    parent.TeamTurnID,
			CallID:        childCall,
			CallTurnID:    childTurn,
			CallType:      types.CallAgent,
		},
		Payload: map[string]any{
			"spawn": map[string]any{
				"agent":          agentID,
				"key":            key,
				"parent_call_id": parent.CallID,
			},
		},
	}
	if eventType == types.EventAgentTurnCompleted {
		callResult := types.CallResult{
			Status:     types.TurnFailed,
			CallTurnID: childTurn,
			AgentID:    agentID,
		}
		if outcome != nil {
			callResult.Status = outcome.Status
			callResult.Reply = outcome.Reply
			callResult.Error = outcome.Error
			callResult.Usage = outcome.Usage
			callResult.Requests = outcome.Requests
			if callResult.Status == "" {
				callResult.Status = types.TurnFailed
			}
		}
		event.Payload["call_result"] = callResult
	}
	_, _ = t.sessions.Append(ctx, parent.FlowSessionID, storage.LayerTeam, event)
}

// saveAgentState applies the same deterministic update the Team runtime
// uses for per-call agent state: first outcome is confirmed, later replies
// become next steps, workspace refs are tracked.
func (t *SpawnTool) saveAgentState(
	ctx context.Context,
	agentID, itemJSON, previousStateText string,
	result *types.AgentResult,
) error {
	snapshot, err := t.states.LoadAgentState(ctx, agentID)
	if err != nil {
		return err
	}
	if snapshot.Goal == "" {
		snapshot.Goal = itemJSON
	}
	reply := strings.TrimSpace(result.Reply)
	if reply != "" {
		item := types.StateItem{ID: t.states.NextItemID(), Text: reply}
		if strings.TrimSpace(previousStateText) == "" {
			snapshot.Confirmed = append(snapshot.Confirmed, item)
		} else {
			snapshot.NextSteps = append(snapshot.NextSteps, item)
		}
	}
	for _, operation := range result.WorkspaceOps {
		if operation.Path == "" {
			continue
		}
		snapshot.Workspace = append(snapshot.Workspace, types.StateWorkspaceRef{
			Path:     operation.Path,
			Revision: operation.Revision,
		})
	}
	return t.states.SaveAgentState(ctx, agentID, snapshot)
}

func childCallID(parentCallID, key string) string {
	return agentstore.ChildCallID(parentCallID, key)
}

func childTurnID(parent types.AgentRequest, key string) string {
	base := parent.AgentTurnID
	if base == "" {
		base = parent.CallID
	}
	if base == "" {
		base = "agent"
	}
	return fmt.Sprintf("%s/%s#%d", base, key, spawnTurnSeq.Add(1))
}

// executeAsync implements wait=false + deliver=parent (design 21 §4.3): each
// child becomes a durable SpawnChild task on the shared AsyncToolExecutor,
// the tool returns handles (task id + instance key) immediately, and the parent
// collects results later with the Collect tool. Children run concurrently —
// one task per item, bounded by the same maxChildren limit as the sync path.
func (t *SpawnTool) executeAsync(
	ctx context.Context,
	agentID string,
	parent types.AgentRequest,
	items []any,
	key string,
	depth int,
) (*types.ToolResult, error) {
	if t.tasks == nil {
		return spawnError("asynchronous Spawn requires the async tool task runner; it is not configured"), nil
	}
	// Resolve every instance before starting any task so a failing
	// NextInstanceKey cannot leave partially started children behind.
	keys := make([]string, len(items))
	for i := range items {
		instance, err := t.registry.NextInstanceKey(ctx, agentID, key)
		if err != nil {
			return spawnError(err.Error()), nil
		}
		keys[i] = instance.Key
	}
	entries := make([]map[string]any, len(items))
	for i := range items {
		task := types.ToolTask{
			ID:            spawnTaskID(parent, keys[i]),
			FlowSessionID: parent.FlowSessionID,
			TeamID:        parent.TeamID,
			TeamTurnID:    parent.TeamTurnID,
			CallTurnID:    parent.CallTurnID,
			AgentTurnID:   parent.AgentTurnID,
			CallID:        parent.CallID,
			ToolName:      SpawnChildToolName,
			Arguments:     spawnChildArguments(agentID, parent, items[i], keys[i], depth),
			// A child turn may have side effects; an interrupted execution is
			// failed on recovery, never silently re-run.
			RestartSafe: false,
		}
		if err := t.tasks.Start(ctx, task); err != nil {
			return spawnError(fmt.Sprintf("start spawned child %q: %v", keys[i], err)), nil
		}
		entries[i] = map[string]any{"task_id": task.ID, "key": keys[i]}
	}
	content, _ := json.Marshal(entries)
	return &types.ToolResult{
		Success: true,
		Content: string(content),
		Metadata: map[string]any{
			"agent":    agentID,
			"deliver":  "parent",
			"wait":     false,
			"children": len(entries),
			"depth":    depth,
		},
	}, nil
}

// executeAsyncDownstream implements wait=false + deliver=downstream (design
// 21 §4.4, batch C): each child instance is registered as a synthetic call in
// the spawning parent call's Team group through the insertion channel in ctx,
// and the tool returns handles immediately. The children execute later in the
// Team scheduler's normal batch path and publish records under the parent
// call's output.record name; downstream calls that depend on the parent wait
// for the whole group.
func (t *SpawnTool) executeAsyncDownstream(
	ctx context.Context,
	agentID string,
	parent types.AgentRequest,
	items []any,
	key string,
	depth int,
) (*types.ToolResult, error) {
	// Batch B leftover #4: a durable SpawnChild task (wait=false +
	// deliver=parent) runs on the AsyncToolExecutor, whose execution context
	// is detached from the Team Run (task.go run() starts it with
	// context.Background()). Such a child is not a Team-scheduled call: it has
	// no call channel to join, so there is nothing a downstream spawn could
	// attach to. The insertion channel only exists while the parent call is
	// executed by the Team scheduler.
	inserter := agentstore.ChildInserterFromContext(ctx)
	if inserter == nil {
		return spawnError("asynchronous Spawn with deliver=downstream requires the parent call to be scheduled by a Team; spawned background children (deliver=parent) have no Team call channel to join"), nil
	}
	// Resolve every instance before inserting any child so a failing
	// NextInstanceKey cannot leave a partially joined group behind.
	keys := make([]string, len(items))
	for i := range items {
		instance, err := t.registry.NextInstanceKey(ctx, agentID, key)
		if err != nil {
			return spawnError(err.Error()), nil
		}
		keys[i] = instance.Key
	}
	entries := make([]map[string]any, len(items))
	for i := range items {
		spec := agentstore.SpawnedCallSpec{
			AgentID: agentID,
			Key:     keys[i],
			Item:    items[i],
			Depth:   depth,
		}
		if err := inserter.InsertSpawnedCall(ctx, parent.CallID, spec); err != nil {
			return spawnError(err.Error()), nil
		}
		entries[i] = map[string]any{
			"key":     keys[i],
			"call_id": agentstore.ChildCallID(parent.CallID, keys[i]),
		}
	}
	content, _ := json.Marshal(entries)
	return &types.ToolResult{
		Success: true,
		Content: string(content),
		Metadata: map[string]any{
			"agent":    agentID,
			"deliver":  "downstream",
			"wait":     false,
			"children": len(entries),
			"depth":    depth,
		},
	}, nil
}

// executeChildTask runs one durable SpawnChild task (the dispatcher entry
// point). Arguments are the persisted spawnChildArguments payload; the child
// executes through the same runChild path as synchronous spawns, including
// agent state persistence and agent-level session events.
func (t *SpawnTool) executeChildTask(ctx context.Context, args map[string]any) (*types.ToolResult, error) {
	if t == nil || t.runner == nil || t.registry == nil {
		return spawnError("Spawn tool is not configured"), nil
	}
	agentID, _ := args["agent"].(string)
	key, _ := args["key"].(string)
	depth := 0
	switch value := args["depth"].(type) {
	case int:
		depth = value
	case float64:
		depth = int(value)
	}
	item := args["item"]
	parentRaw, _ := args["parent"].(map[string]any)
	parent := spawnParentFromArguments(parentRaw)

	def, defined := t.agents[agentID]
	if !defined {
		return spawnError(fmt.Sprintf("Spawn agent %q is not defined", agentID)), nil
	}

	outcome := t.runChild(ctx, agentID, def, parent, item, key, depth)
	entry := spawnOutcomeEntry(outcome, true)
	content, _ := json.Marshal(entry)
	result := &types.ToolResult{Success: outcome.Error == "", Content: string(content)}
	if outcome.Error != "" {
		result.Error = outcome.Error
	}
	return result, nil
}

// spawnChildArguments captures everything a durable SpawnChild task needs to
// re-create the child turn after a process restart: target agent, instance key,
// spawn depth, the fanout item, and the parent request identity.
func spawnChildArguments(
	agentID string,
	parent types.AgentRequest,
	item any,
	key string,
	depth int,
) map[string]any {
	return map[string]any{
		"agent": agentID,
		"key":   key,
		"depth": depth,
		"item":  item,
		"parent": map[string]any{
			"flow_session_id":    parent.FlowSessionID,
			"team_id":            parent.TeamID,
			"team_turn_id":       parent.TeamTurnID,
			"call_id":            parent.CallID,
			"call_turn_id":       parent.CallTurnID,
			"agent_id":           parent.AgentID,
			"agent_turn_id":      parent.AgentTurnID,
			"max_agent_rounds":   parent.MaxAgentRounds,
			"max_parallel_tools": parent.MaxParallelTools,
		},
	}
}

// spawnParentFromArguments rebuilds the parent AgentRequest recorded in a
// durable SpawnChild task. Numeric fields arrive as float64 after the JSON
// round-trip through the task store.
func spawnParentFromArguments(raw map[string]any) types.AgentRequest {
	if raw == nil {
		return types.AgentRequest{}
	}
	return types.AgentRequest{
		FlowSessionID:    stringFromArgs(raw, "flow_session_id"),
		TeamID:           stringFromArgs(raw, "team_id"),
		TeamTurnID:       stringFromArgs(raw, "team_turn_id"),
		CallID:           stringFromArgs(raw, "call_id"),
		CallTurnID:       stringFromArgs(raw, "call_turn_id"),
		AgentID:          stringFromArgs(raw, "agent_id"),
		AgentTurnID:      stringFromArgs(raw, "agent_turn_id"),
		MaxAgentRounds:   intFromArgs(raw, "max_agent_rounds"),
		MaxParallelTools: intFromArgs(raw, "max_parallel_tools"),
	}
}

func stringFromArgs(raw map[string]any, key string) string {
	value, _ := raw[key].(string)
	return value
}

func intFromArgs(raw map[string]any, key string) int {
	switch value := raw[key].(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return 0
	}
}

func spawnTaskID(parent types.AgentRequest, key string) string {
	base := parent.AgentTurnID
	if base == "" {
		base = parent.CallID
	}
	if base == "" {
		base = "agent"
	}
	return fmt.Sprintf("%s:spawn:%s:%d", base, key, spawnTurnSeq.Add(1))
}

func renderAgentState(snapshot types.StateSnapshot) string {
	var sections []string
	if snapshot.Goal != "" {
		sections = append(sections, "Goal: "+snapshot.Goal)
	}
	appendList := func(title string, values []types.StateItem) {
		if len(values) == 0 {
			return
		}
		texts := make([]string, len(values))
		for i, item := range values {
			texts[i] = item.Text
		}
		sections = append(sections, title+":\n- "+strings.Join(texts, "\n- "))
	}
	appendList("Confirmed", snapshot.Confirmed)
	appendList("Open Questions", snapshot.OpenQuestions)
	appendList("Decisions", snapshot.Decisions)
	appendList("Next Steps", snapshot.NextSteps)
	return strings.Join(sections, "\n\n")
}
