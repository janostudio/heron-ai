package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/agentstore"
	"github.com/heron-ai/heron-engine/internal/config"
	definitionwriter "github.com/heron-ai/heron-engine/internal/definitions"
	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/mcp"
	"github.com/heron-ai/heron-engine/internal/media"
	"github.com/heron-ai/heron-engine/internal/prompt"
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/runtime/flow"
	"github.com/heron-ai/heron-engine/internal/runtime/team"
	"github.com/heron-ai/heron-engine/internal/skill"
	"github.com/heron-ai/heron-engine/internal/state"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// RuntimeBundle wires the new FlowRuntime without exposing its internal
// executors to CLI, HTTP, or TUI callers.
type RuntimeBundle struct {
	Flow         types.FlowRuntime
	Definitions  *types.Definitions
	ToolExecutor *tool.ToolExecutor
	Sessions     storage.SessionWriter
	Tasks        types.ToolTaskStore
	TaskControl  types.ToolTaskCanceller

	// DefinitionStore is the live store the runtimes resolve against, and the
	// source of the reload path. Callers that want the tree to act on must
	// read DefinitionStore.Snapshot(); Definitions above is the startup tree
	// and goes stale as soon as a definition is published.
	DefinitionStore *types.DefinitionStore

	// ReloadDefinitions re-reads .agents/ and publishes the result, so a
	// definition created mid-conversation becomes effective on the next turn.
	// It is built here rather than by the caller so the loader and the store
	// cannot disagree about which config root they describe.
	ReloadDefinitions types.DefinitionsReloadFunc

	// MCP holds the live MCP connections. It is exposed so the caller can
	// Close them: a stdio MCP server is a child process, and only the caller
	// knows when the process is about to exit.
	MCP *mcp.MCPAdapter
}

func BuildRuntime(ctx context.Context, store *types.DefinitionStore, provider types.ModelProvider, workspaceRoot string, logLevelOverride string) (*RuntimeBundle, error) {
	if store == nil {
		return nil, errors.New("definition store is required")
	}
	// The one-time wiring below (workspace backend, skill registry, knowledge
	// index, rule definitions, limits) is build-time state: it describes the
	// process, not a turn, so it reads the startup tree once. Per-turn
	// consumers hold the store and take their own snapshots.
	definitions := store.Snapshot()
	if definitions == nil {
		return nil, errors.New("definition store has no definitions")
	}
	if provider == nil {
		return nil, errors.New("model provider is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// The workspace tool backend follows the resolved runtime workspace config
	// (agent > team > flow, default local). Engine state (storage/logging)
	// below stays on the local workspaceRoot regardless.
	var workspaceService workspace.Workspace
	var err error
	if definitions.Workspace != nil && definitions.Workspace.Type != "" && definitions.Workspace.Type != "local" {
		workspaceService, err = workspace.New(*definitions.Workspace, workspaceRoot)
	} else {
		workspaceService, err = workspace.NewLocal(workspaceRoot)
	}
	if err != nil {
		return nil, err
	}
	toolRegistry := tool.NewToolRegistry()
	toolRegistry.Register(tool.NewReadTool(workspaceService))
	toolRegistry.Register(tool.NewWriteTool(workspaceService))
	toolRegistry.Register(tool.NewBashTool(workspaceService))
	toolRegistry.Register(tool.NewGrepTool(workspaceService))
	toolRegistry.Register(tool.NewGlobTool(workspaceService))
	toolRegistry.Register(tool.NewWebSearchTool(http.DefaultClient, tool.WebSearchConfig{}))
	toolRegistry.Register(tool.NewWebFetchTool(http.DefaultClient, tool.WebFetchConfig{}))
	toolRegistry.Register(tool.NewCodeNavTool(workspaceService, "codels"))
	toolRegistry.Register(tool.NewAskUserQuestionTool())
	toolRegistry.Register(tool.NewTodoWriteTool())
	toolRegistry.Register(tool.NewTodoReadTool())
	toolExecutor := tool.NewToolExecutor(toolRegistry)

	promptRenderer := promptAdapter{renderer: prompt.NewPromptRenderer(nil)}
	turnLoop := agent.NewTurnLoop(
		provider,
		toolExecutor,
		nil,
		agent.NewRouteParser(),
		agent.NewHITLGate(0),
		agent.NewHookExecutor(),
		promptRenderer,
	)

	executors := call.NewRegistry()
	if err := executors.Register(call.NewAgentExecutor(turnLoop)); err != nil {
		return nil, err
	}
	if err := executors.Register(call.NewCommandExecutor(workspaceService)); err != nil {
		return nil, err
	}
	if err := executors.Register(call.NewWebhookExecutor(http.DefaultClient)); err != nil {
		return nil, err
	}

	teamRuntime := team.NewRuntime(executors, store)
	files := storage.NewFileStore(workspaceRoot)
	// The Bash gate (internal/agent/bash_gate.go) needs the file store to ask
	// whether an agent has a private knowledge tree, and the knowledge trees
	// are read through it below. Wiring it here — where the workspace root is
	// known — is what makes the gate able to answer at all; a TurnLoop without
	// it grants Bash unconditionally.
	turnLoop.SetFileStore(files)

	// Global execution logger: rotating file logs under .agents/data/logs,
	// configured via .agents/settings.json (logging section), with an optional
	// command-line level override. The handle is kept: the Tool wake-up below
	// logs through it explicitly so its diagnostics cannot depend on whoever
	// last called logging.SetDefault.
	execLogger := buildLogger(workspaceRoot, logLevelOverride)
	logging.SetDefault(execLogger)
	// One agent is one agent: the Spawn tool's inline children, durable
	// SpawnChild tasks, and the Team scheduler's synthetic calls (batch C)
	// share one agent-state lock set so the same dynamic agent never runs two
	// concurrent turns across paths.
	agentStateLocks := agentstore.NewAgentStateLocks()
	teamRuntime.SetAgentStateLocks(agentStateLocks)
	// One shared state.Store backs both the Spawn tool's cross-session agent
	// state and the Team runtime's team/agent state, plus the builtin State
	// tool (design doc 26). The store's agent-level write locks guard CRUD
	// atomicity; the shared lock set is reused across paths.
	stateStore := state.NewStore(files, state.Limits{})
	stateStore.SetLocks(agentStateLocks)
	// Spawn (design 20/21, batch A): the tool executes child turns through the
	// same TurnLoop and persists cross-session agent state under the workspace
	// data dir. Agents must declare Spawn in tools.builtin to see it; everyone
	// else is unaffected. Batch B wires the async task runner and session
	// writer below so wait=false spawns run as durable SpawnChild tasks;
	// batch C wires the shared agent-state locks for Team DAG insertions.
	spawnTool := agent.NewSpawnTool(
		turnLoop,
		store,
		agentstore.NewRegistry(files),
		stateStore,
	)
	spawnTool.SetAgentStateLocks(agentStateLocks)
	toolRegistry.Register(spawnTool)
	// State (design doc 26): the builtin tool lets an Agent CRUD its own
	// cross-session todo state from inside the TurnLoop.
	toolRegistry.Register(agent.NewStateTool(stateStore))
	// Define: the write surface of internal/definitions (design doc 22). The
	// writer is built from the STORE rather than the startup `definitions`
	// snapshot above, because the store is what carries the absolute config
	// root and flow path an apply must write back to — and it is the thing an
	// apply reloads. Registering here, before any turn can run, is only half
	// the wiring: a tool the model cannot see is dead code with no symptom.
	// internal/agent's builtinSchemas + buildToolSchemas must also know the
	// name, and the agent's tools.builtin must list it. See the sync note on
	// builtinSchemas in internal/agent/runtime.go and
	// TestBuildToolSchemasIncludesDefine, which fails if either half is
	// missing.
	toolRegistry.Register(agent.NewDefineTool(definitionwriter.NewWriter(store), store))
	mediaStore := media.NewFileStore(files, media.Limits{})
	if setter, ok := provider.(types.MediaResolverSetter); ok {
		setter.SetMediaResolver(mediaStore)
	}
	teamRuntime.SetStateStore(stateStore)
	skillRegistry := skill.NewSkillRegistry()
	for _, definition := range definitions.Skills {
		if err := skillRegistry.Register(definition); err != nil {
			return nil, err
		}
	}
	teamRuntime.SetSkillInjector(skill.NewSkillInjector(skillRegistry))
	teamRuntime.SetRuleDefinitions(definitions.Rules)
	// 规则正文的解析只在 config 包里有一份定义（config.LoadRuleBody）；
	// 这个闭包只负责把它接到 files 这个 workspace FileStore 上。ctx 未使用
	// 是因为签名由 team runtime 规定，而读取是同步的本地 IO。
	teamRuntime.SetRuleLoader(func(ctx context.Context, path string) (string, error) {
		return config.LoadRuleBody(files, path)
	})
	// Knowledge is validated per location, then handed to the Team runtime as
	// a pointer rather than an index.
	//
	// # Why the load still happens even though nothing is indexed
	//
	// Agentic search means the model finds knowledge with Grep rather than by
	// querying an in-memory index, so there is no index to build. The load is
	// kept for a different reason: MarkdownStore.load is where a knowledge
	// file whose frontmatter scope contradicts where it lives is *rejected*
	// (see reconcileScope), and a rejection is returned, not swallowed.
	//
	// That is the deliberate change from the previous `if entries, loadErr :=
	// ...; loadErr == nil` shape, which treated *any* load failure as "this
	// tree has no knowledge". For an absent directory that is right: every
	// workspace without a knowledge base would otherwise refuse to start. For a
	// validation failure it is exactly backwards — the tree is malformed, and
	// the engine would come up looking healthy while the model, grepping that
	// same tree, would read files whose stated scope is a lie. Absence is
	// detected by asking whether the directory exists; everything else is
	// fatal.
	//
	// So this is a startup validation pass. Its result is discarded on
	// purpose; the pointer re-derives what it needs from the same Store.Load.
	knowledgeStore := knowledge.NewMarkdownStore(files, ".agents/knowledge")
	if _, err := knowledgeStore.Load(ctx); err != nil {
		return nil, fmt.Errorf("load knowledge from .agents/knowledge: %w", err)
	}
	for agentID := range definitions.Agents {
		privateStore := knowledge.NewMarkdownStore(
			files,
			filepath.Join(".agents", "agents", agentID, "knowledge"),
		)
		if !privateStore.Exists() {
			// Most agents have no private knowledge directory. That is not an
			// error and must not be: only a directory that exists and then
			// fails to load is.
			continue
		}
		if _, err := privateStore.Load(ctx); err != nil {
			return nil, fmt.Errorf("load knowledge for agent %q: %w", agentID, err)
		}
	}
	// The pointer names the knowledge directories an agent may grep. It reads
	// the same two roots the validation above does, so what it advertises and
	// what was validated cannot diverge.
	teamRuntime.SetKnowledgePointer(knowledge.NewKnowledgePointer(files, ".agents/knowledge", ".agents"))
	sessionWriter := storage.NewJSONLSessionWriter(files)
	checkpointStore := agent.NewFileCheckpointStore(files)
	taskStore := agent.NewFileToolTaskStore(files)
	turnLoop.SetSessionWriter(sessionWriter)
	// Durable SpawnChild tasks (asynchronous Spawn, batch B) route through the
	// dispatcher into the Spawn tool; every other async tool keeps its normal
	// execution path. The shared executor gives spawned children the same
	// persistence, progress and recovery as ordinary async tool tasks.
	taskRunner := agent.NewAsyncToolExecutor(taskStore, agent.NewSpawnTaskDispatcher(spawnTool, toolExecutor))
	spawnTool.SetTaskRunner(taskRunner)
	spawnTool.SetSessionWriter(sessionWriter)
	toolRegistry.Register(agent.NewCollectTool(taskRunner))
	turnLoop.SetCheckpointStore(checkpointStore)
	turnLoop.SetTaskRunner(taskRunner)
	// MCP (design 04E): servers are declared in .agents/settings.json under
	// "mcp" and connected once at startup. Three separate wirings are needed
	// before a model can actually call one, and missing any of them is
	// silent — the server connects, discovery succeeds, and the tool simply
	// never appears:
	//
	//   1. toolExecutor.SetMCPDispatcher — makes the name EXECUTABLE.
	//   2. turnLoop.SetMCPTools          — makes it VISIBLE in the schema.
	//   3. the agent's own tools.mcp     — declares it USES the tool.
	//
	// A server that fails to connect is reported and skipped rather than
	// aborting startup: a broken MCP server should not make the engine
	// unusable, but it must not be silent either, so every failure is logged
	// with the server name and the reason.
	mcpAdapter := mcp.NewMCPAdapter(mcp.Options{})
	mcpConfigs, mcpConfigErr := config.NewConfigLoader(workspaceRoot).LoadMCPServers()
	switch {
	case mcpConfigErr != nil:
		logging.Warn("mcp: config is unreadable, no MCP tools will be available",
			map[string]any{"error": mcpConfigErr.Error()})
	case len(mcpConfigs) > 0:
		if err := mcpAdapter.ConnectConfigs(ctx, mcpConfigs); err != nil {
			logging.Warn("mcp: some servers failed to connect", map[string]any{"error": err.Error()})
		}
		for _, issue := range mcpAdapter.Issues() {
			logging.Warn("mcp: "+issue, nil)
		}
		logging.Info("mcp: connected", map[string]any{
			"servers": strings.Join(mcpAdapter.ListServers(), ","),
			"tools":   len(mcpAdapter.Tools()),
		})
	}
	toolExecutor.SetMCPDispatcher(mcpAdapter)
	turnLoop.SetMCPTools(mcpAdapter)
	if err := taskRunner.Recover(ctx); err != nil {
		return nil, err
	}
	if _, err := agent.RecoverCheckpoints(ctx, checkpointStore, taskStore); err != nil {
		return nil, err
	}
	teamRuntime.SetSessionWriter(sessionWriter)
	evidenceStore := storage.NewJSONLEvidenceStore(files)
	flowRuntime := flow.NewRuntime(
		store,
		teamRuntime,
		sessionWriter,
		evidenceStore,
	)
	flowRuntime.SetLimits(definitions.Limits)
	flowRuntime.SetTaskStore(taskStore)
	flowRuntime.SetMediaStore(mediaStore)
	onTaskDone := func(doneCtx context.Context, task types.ToolTask) {
		if task.ToolName == agent.SpawnChildToolName {
			// Spawned children deliver their results through Collect handles
			// (durable task store), not through the Flow waiting-tool wake-up:
			// the parent keeps running or has already finished its turn.
			return
		}
		if task.FlowSessionID == "" {
			return
		}
		// The callback signature carries no error, so the wake-up logs its
		// own failure; the returned error exists for tests and for callers
		// that can act on it.
		_ = wakeFlowOnToolTaskDone(doneCtx, flowRuntime, execLogger, task)
	}
	taskRunner.SetCompletionHandler(onTaskDone)
	// A previous process may have completed a durable task before this
	// runtime installed its callback. Re-scan terminal tasks on startup.
	if tasks, listErr := taskStore.List(ctx); listErr == nil {
		for _, task := range tasks {
			if task.Status == types.ToolTaskCompleted ||
				task.Status == types.ToolTaskFailed ||
				task.Status == types.ToolTaskCancelled {
				go onTaskDone(context.Background(), task)
			}
		}
	}

	return &RuntimeBundle{
		Flow:              flowRuntime,
		Definitions:       definitions,
		ToolExecutor:      toolExecutor,
		Sessions:          sessionWriter,
		Tasks:             taskStore,
		TaskControl:       taskRunner,
		DefinitionStore:   store,
		ReloadDefinitions: reloadDefinitions(store),
		MCP:               mcpAdapter,
	}, nil
}

// Close releases everything the bundle owns that outlives a turn. Today that
// is only the MCP servers, and it matters: a stdio MCP server is a child
// process, and a heron run that exits without closing them leaves the servers
// (and whatever they forked) running.
func (b *RuntimeBundle) Close() error {
	if b == nil || b.MCP == nil {
		return nil
	}
	return b.MCP.Close()
}

// Poll parameters for the Tool wake-up below. The first poll is immediate, so
// a turn that has already flushed waiting_tool is resumed in microseconds;
// the delay then doubles so a turn that is still writing its checkpoint is
// re-read tens of times per minute instead of thousands.
const (
	toolWakeupMinDelay = 20 * time.Millisecond
	toolWakeupMaxDelay = time.Second
)

// toolWakeupWaitLimit is how long one completion callback waits for its Flow
// session to reach waiting_tool before reporting the wake-up as lost. A turn
// can legitimately need a long time to flush: the Agent checkpoint, the Team
// waiting-tool event and the session event are separate writes, and a slow
// disk or a slow model stream stretches all of them. Two seconds (the old
// fixed retry window) is not a safe bound; one minute is, and it costs nothing
// because the callback already runs on its own goroutine. It is a var so tests
// can shorten it.
var toolWakeupWaitLimit = 60 * time.Second

// wakeFlowOnToolTaskDone resumes the Flow session that is parked in
// waiting_tool for a durable async tool task that has just finished.
//
// # Why it has to wait
//
// The task completes on its own goroutine, so it can finish before the Agent
// checkpoint and the Team waiting-tool event are flushed. The wake-up must
// therefore wait for the session to actually reach waiting_tool: resuming too
// early fails, and giving up leaves the session parked in waiting_tool with no
// further callback scheduled — the session stops responding and nothing in the
// logs says why.
//
// # What it waits for, and when it stops early
//
// It consumes the wake-up only in waiting_tool, which is the existing
// contract. waiting_input, waiting_approval and the terminal states return
// immediately: none of them is a durable Tool wake-up, and resuming any of
// them would invent a turn the user did not ask for. While the session is
// still created/running (or not yet readable) the wait continues, bounded by
// toolWakeupWaitLimit and by the caller's context.
//
// # Why the bound is loud
//
// If the bound is reached the wake-up is still not delivered, so the function
// logs a warning carrying the session, the task and the last status observed,
// and returns an error describing the same. The task stays terminal in the
// durable store, so the wake-up is re-driven by the startup re-scan on the
// next start rather than being lost for good — but an operator has to be able
// to see that it happened, which is the whole point of the log.
func wakeFlowOnToolTaskDone(
	ctx context.Context,
	flowRuntime types.FlowRuntime,
	logger logging.Logger,
	task types.ToolTask,
) error {
	waitCtx, cancel := context.WithTimeout(ctx, toolWakeupWaitLimit)
	defer cancel()

	var (
		attempts   int
		lastStatus types.SessionStatus
		lastErr    error
	)
	delay := toolWakeupMinDelay
	for {
		attempts++
		session, statusErr := flowRuntime.Status(waitCtx, task.FlowSessionID)
		switch {
		case statusErr == nil && session.Status == types.SessionWaitingTool:
			lastStatus = session.Status
			// A Resume that leaves the session in waiting_tool means a
			// sibling task in the same Team is still running; that task's
			// own completion callback performs the final resume. Either way
			// this callback is consumed exactly once.
			_, resumeErr := flowRuntime.Resume(waitCtx, task.FlowSessionID, "")
			if resumeErr == nil {
				return nil
			}
			lastErr = resumeErr
		case statusErr == nil &&
			session.Status != types.SessionCreated &&
			session.Status != types.SessionRunning:
			return nil
		case statusErr == nil:
			lastStatus = session.Status
		default:
			lastErr = statusErr
		}

		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				// The caller's context went away: shutdown, or its own
				// deadline. That is not a stuck session, so stay quiet.
				return ctx.Err()
			}
			return lostToolWakeup(logger, task, attempts, lastStatus, lastErr)
		case <-time.After(delay):
		}
		if delay < toolWakeupMaxDelay {
			delay *= 2
			if delay > toolWakeupMaxDelay {
				delay = toolWakeupMaxDelay
			}
		}
	}
}

// lostToolWakeup reports a wake-up that could not be delivered within
// toolWakeupWaitLimit, and returns the same as an error.
func lostToolWakeup(
	logger logging.Logger,
	task types.ToolTask,
	attempts int,
	lastStatus types.SessionStatus,
	lastErr error,
) error {
	observed := string(lastStatus)
	if observed == "" {
		observed = "unknown"
	}
	fields := map[string]any{
		"flow_session_id":     task.FlowSessionID,
		"task_id":             task.ID,
		"task_tool":           task.ToolName,
		"task_status":         string(task.Status),
		"last_session_status": observed,
		"attempts":            attempts,
		"wait_limit":          toolWakeupWaitLimit.String(),
		// The task stays terminal in the durable store, so the wake-up is
		// re-driven by the startup re-scan; an operator can also drive
		// Recover/Resume by hand. Neither is automatic, which is why this is
		// logged rather than swallowed.
		"recovery_hint": "durable task stays terminal; wake-up is retried on next start",
	}
	detail := fmt.Sprintf(
		"tool task %q (%s) finished but flow session %q never reached waiting_tool within %s (last status %q after %d attempts)",
		task.ID, task.ToolName, task.FlowSessionID, toolWakeupWaitLimit, observed, attempts,
	)
	if lastErr != nil {
		fields["last_error"] = lastErr.Error()
		detail += ": " + lastErr.Error()
	}
	logger.Warn("async tool task wake-up was not delivered: "+detail, fields)
	return errors.New(detail)
}

// reloadDefinitions builds the store's reload path from the root and flow path
// the store itself was built for.
//
// The loader is created here, from store.ConfigRoot(), rather than by the
// caller, because the two must agree: a reload through a loader rooted
// somewhere else would publish a tree describing different files than the
// store claims, and nothing downstream could tell. The root is absolute (the
// caller resolves it), so a reload does not depend on the process cwd.
//
// The loader's base directory is the config root's PARENT, not the config root.
// ConfigLoader has two families of reads with different anchors:
//
//   - definition reads (teams/, agents/, skills/, rules/) resolve against the
//     config root, which LoadDefinitions derives from the flow path itself;
//   - settings reads (LoadRuntimeLimits, LoadKnowledgeSettings,
//     LoadLoggingSettings) resolve the literal ".agents/settings.json" against
//     the loader's base directory.
//
// Rooting at the config root would make the settings reads look for
// "<configRoot>/.agents/settings.json", which does not exist, and the loader
// would silently fall back to RuntimeLimits defaults. A reload that changed
// nothing would then quietly reset the runtime limits — measured, not
// hypothetical: a fixture with max_team_turns=7 and max_agent_rounds=3
// reloaded as 20 and 200. Using the parent makes ".agents/settings.json"
// resolve to "<configRoot>/settings.json", which is exactly the file the
// cwd-relative startup load read.
func reloadDefinitions(store *types.DefinitionStore) types.DefinitionsReloadFunc {
	root := store.ConfigRoot()
	flow := store.FlowPath()
	if root == "" || flow == "" {
		// A store built without a root/flow (tests, embedded flows) has no
		// source to re-read. Return nil so callers can tell "reload is not
		// wired" from "reload returned nothing".
		return nil
	}
	loader := config.NewConfigLoader(filepath.Dir(root))
	return func(ctx context.Context) (*types.Definitions, error) {
		return loader.LoadDefinitions(ctx, config.DefinitionsLoadRequest{FlowPath: flow})
	}
}

// buildLogger constructs the global execution logger from .agents/settings.json
// (logging section). It falls back to default settings when the config is
// absent or invalid. logLevelOverride, when non-empty, wins over the config.
func buildLogger(workspaceRoot, logLevelOverride string) *logging.RotatingLogger {
	loader := config.NewConfigLoader(workspaceRoot)
	cfg := loader.LoadLoggingSettings()
	if strings.TrimSpace(logLevelOverride) != "" {
		cfg.Level = logLevelOverride
	}
	return logging.NewRotatingLogger(workspaceRoot, logging.Config{
		Level:         cfg.Level,
		Dir:           cfg.Dir,
		MaxFileSize:   cfg.MaxFileSize,
		MaxBackups:    cfg.MaxBackups,
		RetentionDays: cfg.RetentionDays,
	})
}

type promptAdapter struct {
	renderer *prompt.PromptRenderer
}

func (a promptAdapter) Render(
	agentConfig types.AgentConfig,
	req types.AgentRequest,
	renderContext agent.RenderContext,
) ([]types.Message, error) {
	return a.renderer.Render(
		agentConfig,
		req,
		prompt.RenderContext{
			Variables:     renderContext.Variables,
			ContextBlocks: renderContext.ContextBlocks,
		},
	)
}
