package agent

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// AgentRuntime is the interface for running agents
type AgentRuntime interface {
	Run(ctx context.Context, agent types.AgentConfig, task types.TaskConfig, input string) (*types.AgentResult, error)
}

// TurnLoop implements AgentRuntime with a tool-use loop
type TurnLoop struct {
	model          types.ModelProvider
	toolExecutor   ToolExecutor
	guardrail      *GuardrailChecker
	signalParser   *SignalParser
	hitl           *HITLGate
	hooks          *HookExecutor
	promptRenderer PromptRenderer
}

// ToolExecutor interface (to avoid circular dependency with tool package)
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error)
}

// PromptRenderer interface (to avoid circular dependency with prompt package)
type PromptRenderer interface {
	Render(agent types.AgentConfig, task types.TaskConfig, input string, rctx RenderContext) ([]types.Message, error)
}

// RenderContext holds context for prompt rendering
type RenderContext struct {
	Variables      map[string]string
	AgentStateText string
	TeamStateText  string
	RecentMemories string
	KnowledgeText  string
	WorkerResults  []types.AgentResult
}

func NewTurnLoop(
	model types.ModelProvider,
	toolExecutor ToolExecutor,
	guardrail *GuardrailChecker,
	signalParser *SignalParser,
	hitl *HITLGate,
	hooks *HookExecutor,
	promptRenderer PromptRenderer,
) *TurnLoop {
	return &TurnLoop{
		model:          model,
		toolExecutor:   toolExecutor,
		guardrail:      guardrail,
		signalParser:   signalParser,
		hitl:           hitl,
		hooks:          hooks,
		promptRenderer: promptRenderer,
	}
}

func (t *TurnLoop) Run(ctx context.Context, agent types.AgentConfig, task types.TaskConfig, input string) (*types.AgentResult, error) {
	// Build initial messages
	messages, err := t.promptRenderer.Render(agent, task, input, RenderContext{})
	if err != nil {
		return nil, fmt.Errorf("render prompt: %w", err)
	}

	maxRounds := agent.Loop.MaxRounds
	if maxRounds <= 0 {
		maxRounds = 3 // default
	}

	totalUsage := types.TokenUsage{}
	toolSchemas := t.buildToolSchemas(agent)

	for round := 0; round < maxRounds; round++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		// Guardrail check on input
		if round == 0 && t.guardrail != nil {
			if err := t.guardrail.CheckInput(input); err != nil {
				return &types.AgentResult{
					Raw:   err.Error(),
					Error: err.Error(),
				}, nil
			}
		}

		// Call LLM
		resp, err := t.model.Chat(ctx, messages, toolSchemas, agent.Model)
		if err != nil {
			return nil, fmt.Errorf("llm chat: %w", err)
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		// No tool calls -> final answer
		if len(resp.ToolCalls) == 0 {
			// Guardrail check on output
			if t.guardrail != nil {
				if err := t.guardrail.CheckOutput(resp.Text); err != nil {
					return &types.AgentResult{
						Raw:   resp.Text,
						Error: err.Error(),
					}, nil
				}
			}

			// Parse signal
			signal, cleanText := t.signalParser.ParseWithMode(resp.Text, maxRounds > 1)

			// Structured output if configured
			var parsed any
			if agent.Structured != nil {
				parsed, err = NewStructuredOutputManager().ParseAndValidate(cleanText, agent.Structured)
				if err != nil {
					return &types.AgentResult{
						Raw:   cleanText,
						Error: fmt.Sprintf("structured output: %v", err),
					}, nil
				}
			}

			return &types.AgentResult{
				Raw:    cleanText,
				Parsed: parsed,
				Signal: signal,
				Usage:  totalUsage,
			}, nil
		}

		// Execute tool calls
		for _, tc := range resp.ToolCalls {
			result, err := t.toolExecutor.Execute(ctx, tc.Name, tc.Arguments)
			if err != nil {
				result = &types.ToolResult{
					Success: false,
					Error:   err.Error(),
				}
			}

			// Add tool result to messages
			messages = append(messages, types.Message{
				Role:      "assistant",
				Content:   resp.Text,
				ToolCalls: []types.ToolCall{tc},
			})
			messages = append(messages, types.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				Content:    result.Content,
			})
		}
	}

	// Max rounds reached, return last response
	signal, cleanText := t.signalParser.ParseWithMode("", true)
	return &types.AgentResult{
		Raw:    cleanText,
		Signal: signal,
		Usage:  totalUsage,
	}, nil
}

func (t *TurnLoop) buildToolSchemas(agent types.AgentConfig) []types.JSONSchema {
	var schemas []types.JSONSchema

	// Map of tool descriptions for builtin tools
	builtinSchemas := map[string]types.JSONSchema{
		"Read":      {Type: "object", Properties: map[string]types.JSONProperty{"file": {Type: "string", Description: "Path to the file to read"}}},
		"Write":     {Type: "object", Properties: map[string]types.JSONProperty{"file": {Type: "string", Description: "Path to the file to write"}, "content": {Type: "string", Description: "Content to write"}}},
		"Grep":      {Type: "object", Properties: map[string]types.JSONProperty{"pattern": {Type: "string", Description: "Pattern to search for"}, "path": {Type: "string", Description: "File or directory to search in"}}},
		"Glob":      {Type: "object", Properties: map[string]types.JSONProperty{"pattern": {Type: "string", Description: "Glob pattern (e.g., *.go)"}}},
		"TodoWrite": {Type: "object", Properties: map[string]types.JSONProperty{"items": {Type: "array", Description: "List of todo items"}}},
		"TodoRead":  {Type: "object", Properties: map[string]types.JSONProperty{}},
	}

	for _, toolName := range agent.Tools.Builtin {
		if schema, ok := builtinSchemas[toolName]; ok {
			schemas = append(schemas, schema)
		}
	}

	return schemas
}
