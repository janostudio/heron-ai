package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Design docs 20/21: batch A added synchronous Spawn; batch B adds the
// asynchronous+parent combination (wait=false, deliver=parent). The Spawn tool
// is the single primitive for dynamic agent entities — it registers (or
// reuses) the target entity, runs child AgentTurns inline (wait=true) or as
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
	agent types.AgentConfig
	req   types.AgentRequest
}

type spawnIdentityKey struct{}

func withSpawnIdentity(ctx context.Context, agent types.AgentConfig, req types.AgentRequest) context.Context {
	return context.WithValue(ctx, spawnIdentityKey{}, &spawnIdentity{agent: agent, req: req})
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
	memories    *memory.Store
	tasks       *AsyncToolExecutor
	sessions    storage.SessionWriter
	entityLocks *agentstore.EntityLocks
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
// agents resolves agent definitions by id; registry and memories persist
// dynamic entities and their memory.
func NewSpawnTool(
	runner AgentRunner,
	agents map[string]types.AgentConfig,
	registry *agentstore.Registry,
	memories *memory.Store,
	options ...SpawnOption,
) *SpawnTool {
	spawn := &SpawnTool{
		runner:      runner,
		agents:      agents,
		registry:    registry,
		memories:    memories,
		entityLocks: agentstore.NewEntityLocks(),
		maxChildren: defaultSpawnMaxChildren,
		maxDepth:    defaultSpawnMaxDepth,
	}
	for _, option := range options {
		option(spawn)
	}
	return spawn
}

// SetEntityLocks shares one entity lock set with the Team runtime so inline
// spawned children and synthetic Team calls of the same dynamic entity never
// run concurrently.
func (t *SpawnTool) SetEntityLocks(locks *agentstore.EntityLocks) {
	if locks != nil {
		t.entityLocks = locks
	}
}

// SetTaskRunner wires the durable async task executor used by wait=false
// spawns. It must be set before asynchronous Spawn can run.
func (t *SpawnTool) SetTaskRunner(runner *AsyncToolExecutor) {
	t.tasks = runner
}

// SetSessionWriter wires the optional session.jsonl writer used to emit
// agent-level events for spawned child turns, so child consumption
// (agent_turn.completed → requests[]) lands in the same fact source as
// ordinary Agent turns.
func (t *SpawnTool) SetSessionWriter(writer storage.SessionWriter) {
	t.sessions = writer
}

func (t *SpawnTool) Name() string { return "Spawn" }

func (t *SpawnTool) Description() string {
	return "Spawn dynamic agent entities and execute them. wait=true blocks until children finish; " +
		"wait=false returns handles immediately — deliver=parent children are collected later with Collect, " +
		"deliver=downstream children join your call's Team group and publish records for downstream calls. " +
		"Each item is delivered to its child as ## Your Item; entities keep persistent memory keyed by `key`."
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
			"description": "Single task item (any JSON value) delivered to the child entity",
		},
		"items": map[string]any{
			"type":        "array",
			"description": "Multiple task items; one child entity per item, executed in parallel",
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
			"description": "Entity key to reuse an existing entity (with its memory); only valid with a single item",
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
		return spawnError("Spawn key is only valid with a single item; items spawn one entity per item"), nil
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

// runChild ensures the entity exists, executes one child AgentTurn inline,
// and persists the entity memory afterwards. The child context carries the
// spawn depth (and inherits the record collector) for nested spawning. Child
// turns emit agent-level session events so their consumption lands in the
// same session.jsonl fact source as ordinary agent turns.
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

	entity, err := t.registry.EnsureEntity(childCtx, agentID, key)
	if err != nil {
		return &spawnOutcome{Key: key, Error: err.Error()}
	}

	itemJSON, err := json.Marshal(item)
	if err != nil {
		return &spawnOutcome{Key: entity.Key, Error: fmt.Sprintf("encode item: %v", err)}
	}

	// One entity is one agent: never two concurrent turns. TryLock (instead
	// of Lock) keeps a recursive self-spawn from deadlocking — the reentrant
	// child fails fast with "already executing" instead of blocking forever.
	unlock, busy := t.lockEntity(agentID, entity.Key)
	if busy {
		return &spawnOutcome{Key: entity.Key, Error: fmt.Sprintf("entity %q is already executing", entity.Key)}
	}
	defer unlock()

	childCall := childCallID(parent.CallID, entity.Key)
	childTurn := childTurnID(parent, entity.Key)

	blocks := []types.ContextBlock{{
		Kind:      "fanout_item",
		Text:      string(itemJSON),
		Source:    "spawn",
		Stability: "dynamic",
		Priority:  85,
	}}

	snapshot, err := t.memories.LoadEntity(childCtx, agentID, entity.Key)
	if err != nil {
		return &spawnOutcome{Key: entity.Key, Error: err.Error()}
	}
	memoryText := renderEntityMemory(snapshot)
	if memoryText != "" {
		blocks = append(blocks, types.ContextBlock{
			Kind:         "entity_memory",
			Text:         memoryText,
			Source:       "entity_memory",
			Stability:    "dynamic",
			Priority:     60,
			Compressible: true,
		})
	}

	t.emitChildEvent(childCtx, types.EventAgentTurnStarted, agentID, entity.Key, parent, childCall, childTurn, nil)
	var outcome *spawnOutcome
	defer func() {
		t.emitChildEvent(childCtx, types.EventAgentTurnCompleted, agentID, entity.Key, parent, childCall, childTurn, outcome)
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
		outcome = &spawnOutcome{Key: entity.Key, Status: types.TurnFailed, Error: err.Error()}
		return outcome
	}
	if result == nil {
		outcome = &spawnOutcome{Key: entity.Key, Status: types.TurnFailed, Error: "child agent returned a nil result"}
		return outcome
	}
	outcome = &spawnOutcome{
		Key:      entity.Key,
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
	if err := t.saveEntityMemory(childCtx, agentID, entity.Key, string(itemJSON), memoryText, result); err != nil {
		outcome.Error = fmt.Sprintf("save entity memory: %v", err)
	}
	return outcome
}

// lockEntity serializes turns of the same dynamic entity through the shared
// agentstore.EntityLocks (also used by the Team runtime's synthetic calls).
// It never blocks: when another turn of the entity is in flight the caller
// reports "busy".
func (t *SpawnTool) lockEntity(agentID, key string) (func(), bool) {
	unlock, acquired := t.entityLocks.TryLock(agentID, key)
	if !acquired {
		return nil, true
	}
	return unlock, false
}

// emitChildEvent appends one agent-level session event for a spawned child
// turn. The event reuses the ordinary agent_turn.* structure; the producer is
// distinguished by CallID "<parent-call>/<key>" and a payload.spawn block
// carrying the entity agent/key. Emission is skipped when no session writer
// is wired or the parent has no flow session to write into.
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
	event := types.SessionEvent{
		Type:          eventType,
		FlowSessionID: parent.FlowSessionID,
		TeamID:        parent.TeamID,
		TeamTurnID:    parent.TeamTurnID,
		CallID:        childCall,
		CallTurnID:    childTurn,
		CallType:      types.CallAgent,
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
	_, _ = t.sessions.Append(ctx, parent.FlowSessionID, event)
}

// saveEntityMemory applies the same deterministic update the Team runtime
// uses for per-call agent memory: first outcome is confirmed, later replies
// become next steps, workspace refs are tracked.
func (t *SpawnTool) saveEntityMemory(
	ctx context.Context,
	agentID, key, itemJSON, previousMemoryText string,
	result *types.AgentResult,
) error {
	snapshot, err := t.memories.LoadEntity(ctx, agentID, key)
	if err != nil {
		return err
	}
	if snapshot.Goal == "" {
		snapshot.Goal = itemJSON
	}
	reply := strings.TrimSpace(result.Reply)
	if reply != "" {
		if strings.TrimSpace(previousMemoryText) == "" {
			snapshot.Confirmed = append(snapshot.Confirmed, reply)
		} else {
			snapshot.NextSteps = append(snapshot.NextSteps, reply)
		}
	}
	for _, operation := range result.WorkspaceOps {
		if operation.Path == "" {
			continue
		}
		snapshot.Workspace = append(snapshot.Workspace, types.MemoryWorkspaceRef{
			Path:     operation.Path,
			Revision: operation.Revision,
		})
	}
	return t.memories.SaveEntity(ctx, agentID, key, snapshot)
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
// the tool returns handles (task id + entity key) immediately, and the parent
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
	// Resolve every entity before starting any task so a failing EnsureEntity
	// cannot leave partially started children behind.
	keys := make([]string, len(items))
	for i := range items {
		entity, err := t.registry.EnsureEntity(ctx, agentID, key)
		if err != nil {
			return spawnError(err.Error()), nil
		}
		keys[i] = entity.Key
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
// 21 §4.4, batch C): each child entity is registered as a synthetic call in
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
	// Resolve every entity before inserting any child so a failing
	// EnsureEntity cannot leave a partially joined group behind.
	keys := make([]string, len(items))
	for i := range items {
		entity, err := t.registry.EnsureEntity(ctx, agentID, key)
		if err != nil {
			return spawnError(err.Error()), nil
		}
		keys[i] = entity.Key
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
// entity memory persistence and agent-level session events.
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
// re-create the child turn after a process restart: target agent, entity key,
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

func renderEntityMemory(snapshot types.MemorySnapshot) string {
	var sections []string
	if snapshot.Goal != "" {
		sections = append(sections, "Goal: "+snapshot.Goal)
	}
	appendList := func(title string, values []string) {
		if len(values) == 0 {
			return
		}
		sections = append(sections, title+":\n- "+strings.Join(values, "\n- "))
	}
	appendList("Confirmed", snapshot.Confirmed)
	appendList("Open Questions", snapshot.OpenQuestions)
	appendList("Decisions", snapshot.Decisions)
	appendList("Next Steps", snapshot.NextSteps)
	return strings.Join(sections, "\n\n")
}
