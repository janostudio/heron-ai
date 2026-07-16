package prompt

import (
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BuiltinTemplates contains all built-in prompt templates
var BuiltinTemplates = map[string]string{
	"task-management":       taskManagementTemplate,
	"tool-usage":            toolUsageTemplate,
	"memory-management":     memoryManagementTemplate,
	"knowledge-query":       knowledgeQueryTemplate,
	"perspective-isolation": perspectiveIsolationTemplate,
	"output-format":         outputFormatTemplate,
}

// GetTemplate returns a template by name
func GetTemplate(name string) string {
	return BuiltinTemplates[name]
}

// ListTemplates returns all template names
func ListTemplates() []string {
	names := make([]string, 0, len(BuiltinTemplates))
	for name := range BuiltinTemplates {
		names = append(names, name)
	}
	return names
}

// PromptRenderer renders prompts with agent and task context
type PromptRenderer struct {
	templates map[string]string
}

// NewPromptRenderer creates a new PromptRenderer
func NewPromptRenderer(templates map[string]string) *PromptRenderer {
	if templates == nil {
		templates = make(map[string]string)
	}
	return &PromptRenderer{templates: templates}
}

// RenderContext holds all context for prompt rendering
type RenderContext struct {
	Variables      map[string]string
	AgentStateText string
	TeamStateText  string
	RecentMemories string
	KnowledgeText  string
	WorkerResults  []types.AgentResult
}

// Render builds system and user messages for an agent turn
func (p *PromptRenderer) Render(agent types.AgentConfig, task types.TaskConfig, input string, rctx RenderContext) ([]types.Message, error) {
	systemPrompt := p.BuildSystemPrompt(agent, nil, nil, rctx.AgentStateText, rctx.TeamStateText)
	userPrompt := p.BuildUserPrompt(agent, task, input, rctx)

	var messages []types.Message
	messages = append(messages, types.Message{
		Role:    "system",
		Content: systemPrompt,
	})
	messages = append(messages, types.Message{
		Role:    "user",
		Content: userPrompt,
	})

	return messages, nil
}

// BuildSystemPrompt builds the system prompt for an agent
func (p *PromptRenderer) BuildSystemPrompt(agent types.AgentConfig, skills []types.Skill, templates map[string]string, agentState string, teamState string) string {
	var parts []string

	// Persona
	if agent.Persona.Role != "" {
		parts = append(parts, fmt.Sprintf("You are %s.", agent.Persona.Role))
	}
	if agent.Persona.Goal != "" {
		parts = append(parts, fmt.Sprintf("Your goal: %s", agent.Persona.Goal))
	}
	if agent.Persona.Backstory != "" {
		parts = append(parts, fmt.Sprintf("Background: %s", agent.Persona.Backstory))
	}

	// Agent body (additional instructions)
	if agent.Body != "" {
		parts = append(parts, agent.Body)
	}

	// Tool usage instructions
	if len(agent.Tools.Builtin) > 0 {
		parts = append(parts, GetTemplate("tool-usage"))
	}

	// Memory management
	parts = append(parts, GetTemplate("memory-management"))

	// Perspective isolation for multi-agent scenarios
	if len(agent.Handoffs) > 0 {
		parts = append(parts, GetTemplate("perspective-isolation"))
	}

	// Output format
	if agent.Structured != nil {
		parts = append(parts, GetTemplate("output-format"))
	}

	// Agent state
	if agentState != "" {
		parts = append(parts, fmt.Sprintf("\n## Current State\n%s", agentState))
	}

	// Team state
	if teamState != "" {
		parts = append(parts, fmt.Sprintf("\n## Team Context\n%s", teamState))
	}

	return strings.Join(parts, "\n\n")
}

// BuildUserPrompt builds the user prompt for an agent turn
func (p *PromptRenderer) BuildUserPrompt(agent types.AgentConfig, task types.TaskConfig, input string, rctx RenderContext) string {
	var parts []string

	// Task description
	if task.Description != "" {
		parts = append(parts, fmt.Sprintf("## Task\n%s", task.Description))
	}

	// User input
	if input != "" {
		parts = append(parts, fmt.Sprintf("## Input\n%s", input))
	}

	// Knowledge context
	if rctx.KnowledgeText != "" {
		parts = append(parts, rctx.KnowledgeText)
	}

	// Recent memories
	if rctx.RecentMemories != "" {
		parts = append(parts, fmt.Sprintf("## Recent Memories\n%s", rctx.RecentMemories))
	}

	// Worker results (from previous parallel stages)
	if len(rctx.WorkerResults) > 0 {
		parts = append(parts, "## Previous Results")
		for i, result := range rctx.WorkerResults {
			parts = append(parts, fmt.Sprintf("### Result %d\n%s", i+1, result.Raw))
		}
	}

	// Task management template
	parts = append(parts, GetTemplate("task-management"))

	return strings.Join(parts, "\n\n")
}

// Built-in template content

const taskManagementTemplate = `## Task Management
When working on your task:
1. Break down complex tasks into steps
2. Track your progress
3. Use tools when needed
4. Report your findings clearly`

const toolUsageTemplate = `## Tool Usage
You have access to tools that help you complete your task.
- Use tools when you need to read, write, or search information
- Each tool call counts toward your turn limit
- Be efficient and only use tools when necessary`

const memoryManagementTemplate = `## Memory Management
You maintain your own memory. Use memory to:
- Track important information across turns
- Note decisions and their rationale
- Record findings and insights
- Update your understanding as you learn more`

const knowledgeQueryTemplate = `## Knowledge Query
When you need background information:
- Search the knowledge base for relevant context
- Cross-reference multiple sources when possible
- Note when information is uncertain or conflicting`

const perspectiveIsolationTemplate = `## Perspective Isolation
You are one of multiple agents working together. Important rules:
- You only know what you've personally observed or been told
- Do not assume knowledge that other agents have
- When receiving information from others, consider the source
- You can hand off tasks to other agents when appropriate`

const outputFormatTemplate = `## Output Format
Your output must follow the specified structured format.
- Ensure all required fields are present
- Follow the schema exactly
- If you cannot provide a value, use an appropriate default or null`
