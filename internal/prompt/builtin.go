package prompt

import (
	"fmt"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BuiltinTemplates contains all built-in prompt templates
var BuiltinTemplates = map[string]string{
	"execution-management":  executionManagementTemplate,
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

// PromptRenderer renders a Subagent prompt from an Agent Definition and the
// normalized collaboration context supplied by TeamRuntime.
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

// RenderContext holds optional runtime context for prompt rendering. The
// renderer does not fetch memory, knowledge, or records itself; those are
// injected by the runtime so the prompt layer stays deterministic.
type RenderContext struct {
	Variables      map[string]string
	TeamMemory     string
	SubagentMemory string
	KnowledgeText  string
	Records        []types.SharedRecord
}

// Render builds system and user messages for one Subagent turn.
func (p *PromptRenderer) Render(agent types.AgentConfig, req types.SubagentRequest, rctx RenderContext) ([]types.Message, error) {
	systemPrompt := p.BuildSystemPrompt(agent, rctx)
	userPrompt := p.BuildUserPrompt(agent, req, rctx)

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

// BuildSystemPrompt builds the system prompt for an Agent Definition.
func (p *PromptRenderer) BuildSystemPrompt(agent types.AgentConfig, rctx RenderContext) string {
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

	// Every Subagent is a Team member. Keep the collaboration boundary
	// explicit without introducing direct member-to-member handoff state.
	parts = append(parts, GetTemplate("perspective-isolation"))

	// Output format
	if agent.Structured != nil {
		parts = append(parts, GetTemplate("output-format"))
	}

	if rctx.TeamMemory != "" {
		parts = append(parts, fmt.Sprintf("\n## Team Memory\n%s", rctx.TeamMemory))
	}
	if rctx.SubagentMemory != "" {
		parts = append(parts, fmt.Sprintf("\n## Subagent Memory\n%s", rctx.SubagentMemory))
	}

	return strings.Join(parts, "\n\n")
}

// BuildUserPrompt builds the user-visible context for one Subagent turn.
func (p *PromptRenderer) BuildUserPrompt(_ types.AgentConfig, req types.SubagentRequest, rctx RenderContext) string {
	var parts []string

	// Member responsibility
	if strings.TrimSpace(req.Responsibility) != "" {
		parts = append(parts, fmt.Sprintf("## Responsibility\n%s", req.Responsibility))
	}

	// User input
	if strings.TrimSpace(req.Input) != "" {
		parts = append(parts, fmt.Sprintf("## Input\n%s", req.Input))
	}

	// Knowledge context
	knowledge := rctx.KnowledgeText
	if knowledge == "" {
		knowledge = req.KnowledgeText
	}
	if knowledge != "" {
		parts = append(parts, knowledge)
	}

	teamMemory := rctx.TeamMemory
	if teamMemory == "" {
		teamMemory = req.TeamMemory
	}
	if teamMemory != "" {
		parts = append(parts, fmt.Sprintf("## Team Memory\n%s", teamMemory))
	}

	subagentMemory := rctx.SubagentMemory
	if subagentMemory == "" {
		subagentMemory = req.SubagentMemory
	}
	if subagentMemory != "" {
		parts = append(parts, fmt.Sprintf("## Subagent Memory\n%s", subagentMemory))
	}

	if len(rctx.Records) == 0 {
		rctx.Records = req.Records
	}
	if len(rctx.Records) > 0 {
		parts = append(parts, formatRecords(rctx.Records))
	}

	// Execution management template
	parts = append(parts, GetTemplate("execution-management"))

	return strings.Join(parts, "\n\n")
}

func formatRecords(records []types.SharedRecord) string {
	var parts []string
	parts = append(parts, "## Shared Records")
	for i, record := range records {
		title := record.Name
		if title == "" {
			title = fmt.Sprintf("record-%d", i+1)
		}
		section := fmt.Sprintf("### %s", title)
		if record.Kind != "" {
			section += fmt.Sprintf(" (%s)", record.Kind)
		}
		if record.Summary != "" {
			section += "\n" + record.Summary
		}
		if len(record.Data) > 0 {
			section += "\nData:\n" + formatRecordData(record.Data)
		}
		if len(record.Basis) > 0 {
			section += fmt.Sprintf("\nBasis references: %d", len(record.Basis))
		}
		parts = append(parts, section)
	}
	return strings.Join(parts, "\n")
}

func formatRecordData(data map[string]any) string {
	// Keep the prompt renderer dependency-free and readable. SharedRecord.Data
	// is intentionally business-defined, so JSON serialization is handled by
	// the standard formatter only where a stable representation is useful.
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var lines []string
	for _, key := range keys {
		value := data[key]
		lines = append(lines, fmt.Sprintf("- %s: %v", key, value))
	}
	return strings.Join(lines, "\n")
}

// Built-in template content

const executionManagementTemplate = `## Execution Management
When working on your responsibility:
1. Break down complex work into steps
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
- Return clear findings and decisions so the Team can coordinate the next step`

const outputFormatTemplate = `## Output Format
Your output must follow the specified structured format.
- Ensure all required fields are present
- Follow the schema exactly
- If you cannot provide a value, use an appropriate default or null`
