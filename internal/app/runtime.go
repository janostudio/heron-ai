package app

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/memory"
	"github.com/heron-ai/heron-engine/internal/prompt"
	"github.com/heron-ai/heron-engine/internal/runtime/flow"
	"github.com/heron-ai/heron-engine/internal/runtime/member"
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
	toolRegistry.Register(tool.NewGrepTool(workspaceRoot))
	toolRegistry.Register(tool.NewGlobTool(workspaceRoot))
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

	executors := member.NewRegistry()
	if err := executors.Register(member.NewSubagentExecutor(turnLoop)); err != nil {
		return nil, err
	}
	if err := executors.Register(member.NewCommandExecutor(workspaceService)); err != nil {
		return nil, err
	}
	if err := executors.Register(member.NewWebhookExecutor(http.DefaultClient)); err != nil {
		return nil, err
	}

	teamRuntime := team.NewRuntime(executors, definitions.Agents)
	files := storage.NewFileStore(workspaceRoot)
	teamRuntime.SetMemoryStore(memory.NewStore(files, memory.Limits{}))
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

	return &RuntimeBundle{
		Flow:         flowRuntime,
		Definitions:  definitions,
		ToolExecutor: toolExecutor,
		Sessions:     sessionWriter,
	}, nil
}

type promptAdapter struct {
	renderer *prompt.PromptRenderer
}

func (a promptAdapter) Render(
	agentConfig types.AgentConfig,
	req types.SubagentRequest,
	renderContext agent.RenderContext,
) ([]types.Message, error) {
	return a.renderer.Render(
		agentConfig,
		req,
		prompt.RenderContext{
			Variables:      renderContext.Variables,
			TeamMemory:     renderContext.TeamMemory,
			SubagentMemory: renderContext.SubagentMemory,
			KnowledgeText:  renderContext.KnowledgeText,
			SkillText:      renderContext.SkillText,
			RuleText:       renderContext.RuleText,
			Records:        renderContext.Records,
		},
	)
}
