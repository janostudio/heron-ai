package agent

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// SubagentRunner executes one V1 Subagent member. TeamRuntime owns routing
// and record publication; this package only runs the model/tool loop.
type SubagentRunner interface {
	Run(ctx context.Context, agent types.AgentConfig, req types.SubagentRequest) (*types.SubagentResult, error)
}

// TurnLoop implements SubagentRunner with a bounded tool-use loop.
type TurnLoop struct {
	model          types.ModelProvider
	toolExecutor   ToolExecutor
	guardrail      *GuardrailChecker
	routeParser    *RouteParser
	hitl           *HITLGate
	hooks          *HookExecutor
	promptRenderer PromptRenderer
}

// ToolExecutor is kept small so the agent package does not depend on the
// concrete tool registry.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
}

// PromptRenderer renders one Subagent prompt.
type PromptRenderer interface {
	Render(agent types.AgentConfig, req types.SubagentRequest, rctx RenderContext) ([]types.Message, error)
}

// RenderContext holds the collaboration context visible to a Subagent.
type RenderContext struct {
	Variables      map[string]string
	TeamMemory     string
	SubagentMemory string
	KnowledgeText  string
	Records        []types.SharedRecord
}

func NewTurnLoop(
	model types.ModelProvider,
	toolExecutor ToolExecutor,
	guardrail *GuardrailChecker,
	routeParser *RouteParser,
	hitl *HITLGate,
	hooks *HookExecutor,
	promptRenderer PromptRenderer,
) *TurnLoop {
	return &TurnLoop{
		model:          model,
		toolExecutor:   toolExecutor,
		guardrail:      guardrail,
		routeParser:    routeParser,
		hitl:           hitl,
		hooks:          hooks,
		promptRenderer: promptRenderer,
	}
}

// Run executes one SubagentTurn. A Subagent is request/response in V1:
// multiple model/tool rounds are still internal to this one call and no
// background mailbox or active-agent protocol is introduced.
func (t *TurnLoop) Run(ctx context.Context, agent types.AgentConfig, req types.SubagentRequest) (*types.SubagentResult, error) {
	if t.model == nil {
		return nil, fmt.Errorf("model provider is nil")
	}
	if t.promptRenderer == nil {
		return nil, fmt.Errorf("prompt renderer is nil")
	}
	if t.routeParser == nil {
		t.routeParser = NewRouteParser()
	}

	messages, err := t.promptRenderer.Render(agent, req, RenderContext{
		Variables:      req.Variables,
		TeamMemory:     req.TeamMemory,
		SubagentMemory: req.SubagentMemory,
		KnowledgeText:  req.KnowledgeText,
		Records:        req.Records,
	})
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	maxRounds := agent.Loop.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 3
	}

	totalUsage := types.TokenUsage{}
	toolSchemas := t.buildToolSchemas(agent)
	var lastText string
	var workspaceOps []types.WorkspaceOperation
	toolCalls := 0

	for round := 0; round < maxRounds; round++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if round == 0 && t.guardrail != nil {
			if err := t.guardrail.CheckInput(req.Input); err != nil {
				return &types.SubagentResult{
					Reply: err.Error(),
					Error: err.Error(),
					Next:  &types.Route{Action: types.NextCoordinate, Reason: err.Error()},
				}, nil
			}
		}

		resp, err := t.model.Chat(ctx, messages, toolSchemas, agent.Model)
		if err != nil {
			return nil, fmt.Errorf("llm chat: %w", err)
		}
		if resp == nil {
			return nil, fmt.Errorf("model returned nil response")
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.ReasoningTokens += resp.Usage.ReasoningTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
		lastText = resp.Text

		if len(resp.ToolCalls) == 0 {
			if t.guardrail != nil {
				if err := t.guardrail.CheckOutput(resp.Text); err != nil {
					return &types.SubagentResult{
						Reply:        resp.Text,
						Error:        err.Error(),
						Usage:        totalUsage,
						WorkspaceOps: workspaceOps,
						ToolCalls:    toolCalls,
						Next:         &types.Route{Action: types.NextCoordinate, Reason: err.Error()},
					}, nil
				}
			}

			action, cleanText := t.routeParser.ParseWithMode(resp.Text, maxRounds > 1)
			var parsed any
			if agent.Structured != nil {
				parsed, err = NewStructuredOutputManager().ParseAndValidate(cleanText, agent.Structured)
				if err != nil {
					return &types.SubagentResult{
						Reply:        cleanText,
						Error:        fmt.Sprintf("structured output: %v", err),
						Usage:        totalUsage,
						WorkspaceOps: workspaceOps,
						ToolCalls:    toolCalls,
						Next:         &types.Route{Action: types.NextCoordinate, Reason: err.Error()},
					}, nil
				}
			}

			return &types.SubagentResult{
				Reply:        cleanText,
				Parsed:       parsed,
				Next:         routeFromAction(action),
				Usage:        totalUsage,
				WorkspaceOps: workspaceOps,
				ToolCalls:    toolCalls,
			}, nil
		}

		toolCalls += len(resp.ToolCalls)
		if req.MaxToolCalls > 0 && toolCalls > req.MaxToolCalls {
			return &types.SubagentResult{
				Reply:        "Subagent tool-call limit reached.",
				Next:         &types.Route{Action: types.NextCoordinate, Reason: "tool-call limit reached"},
				Usage:        totalUsage,
				WorkspaceOps: workspaceOps,
				ToolCalls:    toolCalls,
				Error:        fmt.Sprintf("subagent exceeded max tool calls: %d", req.MaxToolCalls),
			}, nil
		}

		results := t.executeToolCalls(ctx, agent, req.MaxParallelTools, resp.ToolCalls)
		messages = append(messages, types.Message{
			Role:      "assistant",
			Content:   resp.Text,
			ToolCalls: resp.ToolCalls,
		})
		for i, call := range resp.ToolCalls {
			result := results[i]
			if result == nil {
				result = &types.ToolResult{Success: false, Error: "tool returned nil result"}
			}
			messages = append(messages, types.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    toolResultContent(result),
			})
			workspaceOps = append(workspaceOps, result.WorkspaceOps...)
		}
	}

	action, cleanText := t.routeParser.ParseWithMode(lastText, true)
	return &types.SubagentResult{
		Reply:        cleanText,
		Next:         &types.Route{Action: action, Reason: "subagent loop limit reached"},
		Usage:        totalUsage,
		WorkspaceOps: workspaceOps,
		ToolCalls:    toolCalls,
	}, nil
}

func (t *TurnLoop) executeToolCalls(ctx context.Context, agent types.AgentConfig, maxParallel int, calls []types.ToolCall) []*types.ToolResult {
	if batch, ok := t.toolExecutor.(BatchToolExecutor); ok && parallelToolsEnabled(agent) {
		return NewToolBatchExecutor(batch, maxParallel, true).Execute(ctx, calls)
	}

	results := make([]*types.ToolResult, len(calls))
	for i, call := range calls {
		result, err := t.toolExecutor.Execute(ctx, call.Name, call.Arguments)
		if err != nil {
			result = &types.ToolResult{Success: false, Error: err.Error()}
		}
		if result == nil {
			result = &types.ToolResult{Success: false, Error: "tool returned nil result"}
		}
		results[i] = result
	}
	return results
}

func toolResultContent(result *types.ToolResult) string {
	if result.Content != "" {
		return result.Content
	}
	if result.Error != "" {
		return result.Error
	}
	return ""
}

func parallelToolsEnabled(agent types.AgentConfig) bool {
	return agent.Loop.ToolExecution == "parallel_safe"
}

func routeFromAction(action types.NextAction) *types.Route {
	if action == "" {
		action = types.NextProceed
	}
	return &types.Route{Action: action}
}

func (t *TurnLoop) buildToolSchemas(agent types.AgentConfig) []types.JSONSchema {
	var schemas []types.JSONSchema

	builtinSchemas := map[string]types.JSONSchema{
		"Read":      {Name: "Read", Type: "object", Properties: map[string]types.JSONProperty{"file": {Type: "string", Description: "Path to the file to read"}}},
		"Write":     {Name: "Write", Type: "object", Properties: map[string]types.JSONProperty{"file": {Type: "string", Description: "Path to the file to write"}, "content": {Type: "string", Description: "Content to write"}}},
		"Grep":      {Name: "Grep", Type: "object", Properties: map[string]types.JSONProperty{"pattern": {Type: "string", Description: "Pattern to search for"}, "path": {Type: "string", Description: "File or directory to search in"}}},
		"Glob":      {Name: "Glob", Type: "object", Properties: map[string]types.JSONProperty{"pattern": {Type: "string", Description: "Glob pattern (e.g., *.go)"}}},
		"TodoWrite": {Name: "TodoWrite", Type: "object", Properties: map[string]types.JSONProperty{"items": {Type: "array", Description: "List of todo items"}}},
		"TodoRead":  {Name: "TodoRead", Type: "object", Properties: map[string]types.JSONProperty{}},
	}

	for _, toolName := range agent.Tools.Builtin {
		if schema, ok := builtinSchemas[toolName]; ok {
			schemas = append(schemas, schema)
		}
	}
	return schemas
}
