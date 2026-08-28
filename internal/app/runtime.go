package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/consolidation"
	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/media"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/prompt"
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/runtime/flow"
	"github.com/heron-ai/heron-engine/internal/runtime/team"
	"github.com/heron-ai/heron-engine/internal/skill"
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
}

func BuildRuntime(ctx context.Context, definitions *types.Definitions, provider types.ModelProvider, workspaceRoot string) (*RuntimeBundle, error) {
	if definitions == nil {
		return nil, errors.New("definitions are required")
	}
	if provider == nil {
		return nil, errors.New("model provider is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	workspaceService, err := workspace.New(workspaceRoot)
	if err != nil {
		return nil, err
	}
	toolRegistry := tool.NewToolRegistry()
	toolRegistry.Register(tool.NewReadTool(workspaceRoot))
	toolRegistry.Register(tool.NewWriteTool(workspaceRoot))
	toolRegistry.Register(tool.NewBashTool(workspaceRoot))
	toolRegistry.Register(tool.NewGrepTool(workspaceRoot))
	toolRegistry.Register(tool.NewGlobTool(workspaceRoot))
	toolRegistry.Register(tool.NewWebSearchTool(http.DefaultClient, tool.WebSearchConfig{}))
	toolRegistry.Register(tool.NewWebFetchTool(http.DefaultClient, tool.WebFetchConfig{}))
	toolRegistry.Register(tool.NewCodeNavTool(workspaceRoot, "codels"))
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

	teamRuntime := team.NewRuntime(executors, definitions.Agents)
	files := storage.NewFileStore(workspaceRoot)
	mediaStore := media.NewFileStore(files, media.Limits{})
	if setter, ok := provider.(types.MediaResolverSetter); ok {
		setter.SetMediaResolver(mediaStore)
	}
	teamRuntime.SetMemoryStore(memory.NewStore(files, memory.Limits{}))
	dreamWorker := consolidation.NewWorker(
		consolidation.NewFileJobStore(files),
		memory.NewStore(files, memory.Limits{}),
	)
	if err := dreamWorker.Start(ctx); err != nil {
		return nil, err
	}
	teamRuntime.SetConsolidationEnqueuer(dreamWorker)
	skillRegistry := skill.NewSkillRegistry()
	for _, definition := range definitions.Skills {
		if err := skillRegistry.Register(definition); err != nil {
			return nil, err
		}
	}
	teamRuntime.SetSkillInjector(skill.NewSkillInjector(skillRegistry))
	teamRuntime.SetRuleDefinitions(definitions.Rules)
	knowledgeStore := knowledge.NewMarkdownStore(files, ".agents/knowledge")
	if entries, loadErr := knowledgeStore.Load(ctx); loadErr == nil {
		index := knowledge.NewKnowledgeIndex()
		for _, entry := range entries {
			index.Add(entry)
		}
		for agentID := range definitions.Agents {
			privateStore := knowledge.NewMarkdownStore(
				files,
				filepath.Join(".agents", "agents", agentID, "knowledge"),
			)
			if privateEntries, privateErr := privateStore.Load(ctx); privateErr == nil {
				for _, entry := range privateEntries {
					index.Add(entry)
				}
			}
		}
		teamRuntime.SetKnowledgeInjector(knowledge.NewKnowledgeInjector(index))
	}
	sessionWriter := storage.NewJSONLSessionWriter(files)
	checkpointStore := agent.NewFileCheckpointStore(files)
	taskStore := agent.NewFileToolTaskStore(files)
	taskRunner := agent.NewAsyncToolExecutor(taskStore, toolExecutor)
	turnLoop.SetCheckpointStore(checkpointStore)
	turnLoop.SetTaskRunner(taskRunner)
	if err := taskRunner.Recover(ctx); err != nil {
		return nil, err
	}
	if _, err := agent.RecoverCheckpoints(ctx, checkpointStore, taskStore); err != nil {
		return nil, err
	}
	teamRuntime.SetSessionWriter(sessionWriter)
	evidenceStore := storage.NewJSONLEvidenceStore(files)
	flowRuntime := flow.NewRuntime(
		definitions,
		teamRuntime,
		sessionWriter,
		evidenceStore,
		workspaceRoot,
	)
	flowRuntime.SetLimits(definitions.Limits)
	flowRuntime.SetTaskStore(taskStore)
	flowRuntime.SetMediaStore(mediaStore)
	onTaskDone := func(doneCtx context.Context, task types.ToolTask) {
		if task.FlowSessionID == "" {
			return
		}
		// The task can finish before the Agent checkpoint and the waiting
		// Team event are flushed. Retry only while the Flow is waiting for a
		// Tool; once resumed/completed, this callback is a no-op.
		for attempt := 0; attempt < 100; attempt++ {
			session, statusErr := flowRuntime.Status(doneCtx, task.FlowSessionID)
			if statusErr == nil && session.Status == types.SessionWaitingTool {
				if resumed, resumeErr := flowRuntime.Resume(doneCtx, task.FlowSessionID, ""); resumeErr == nil {
					if resumed.Session.Status != types.SessionWaitingTool {
						return
					}
					// Another task in the same Team is still running. Its
					// completion callback will perform the final resume.
					return
				}
			} else if statusErr == nil &&
				session.Status != types.SessionCreated &&
				session.Status != types.SessionRunning {
				// Waiting for user input, or already terminal, is not a
				// durable Tool wake-up that this callback should consume.
				return
			}
			select {
			case <-doneCtx.Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
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
		Flow:         flowRuntime,
		Definitions:  definitions,
		ToolExecutor: toolExecutor,
		Sessions:     sessionWriter,
		Tasks:        taskStore,
		TaskControl:  taskRunner,
	}, nil
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
