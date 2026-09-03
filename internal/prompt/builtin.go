package prompt

import (
	"encoding/json"
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

// PromptRenderer renders an Agent prompt from an Agent Definition and the
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
	Variables     map[string]string
	ContextBlocks []types.ContextBlock
}

// Render builds system and user messages for one Agent turn.
func (p *PromptRenderer) Render(agent types.AgentConfig, req types.AgentRequest, rctx RenderContext) ([]types.Message, error) {
	systemPrompt := p.BuildSystemPrompt(agent, rctx)
	userPrompt := p.BuildUserPrompt(agent, req, rctx)
	userParts := contextParts(rctx.ContextBlocks, "user")
	systemParts := contextParts(rctx.ContextBlocks, "system")

	var messages []types.Message
	messages = append(messages, types.Message{
		Role: "system", Content: systemPrompt, Parts: systemParts,
	})
	messages = append(messages, types.Message{
		Role: userRole(userPrompt, userParts), Content: userPrompt, Parts: userParts,
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

	for _, block := range rctx.ContextBlocks {
		if block.Placement == "system" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, renderContextBlock(block))
		}
	}

	// Tool usage instructions
	if len(agent.Tools.Builtin) > 0 {
		parts = append(parts, GetTemplate("tool-usage"))
	}

	// Memory management instructions are stable Agent policy. The actual
	// Team/Agent Memory snapshots are dynamic and belong in the user context
	// below, not in the cacheable system prefix.
	parts = append(parts, GetTemplate("memory-management"))

	// Execution management is also stable Agent policy. Keep it in the
	// system prefix rather than appending it after dynamic user context so
	// repeated Tool rounds can reuse the same prefix.
	parts = append(parts, GetTemplate("execution-management"))

	// Every Agent is a Team call. Keep the collaboration boundary
	// explicit without introducing direct call-to-call handoff state.
	parts = append(parts, GetTemplate("perspective-isolation"))

	// Output format
	if agent.Structured != nil {
		parts = append(parts, GetTemplate("output-format"))
		parts = append(parts, structuredOutputContract(agent.Structured))
	}

	return strings.Join(parts, "\n\n")
}

// BuildUserPrompt builds the user-visible context for one Agent turn.
func (p *PromptRenderer) BuildUserPrompt(_ types.AgentConfig, req types.AgentRequest, rctx RenderContext) string {
	if len(rctx.ContextBlocks) > 0 {
		var parts []string
		for _, block := range rctx.ContextBlocks {
			if block.Placement == "system" || (strings.TrimSpace(block.Text) == "" && len(block.Parts) == 0) {
				continue
			}
			parts = append(parts, renderContextBlock(block))
		}
		return strings.Join(parts, "\n\n")
	}
	return ""
}

func userRole(text string, parts []types.ContentPart) string {
	return "user"
}

func contextParts(blocks []types.ContextBlock, placement string) []types.ContentPart {
	var parts []types.ContentPart
	for _, block := range blocks {
		if block.Placement == placement || (placement == "user" && block.Placement == "") {
			parts = append(parts, cloneContentParts(block.Parts)...)
		}
	}
	return parts
}

func cloneContentParts(parts []types.ContentPart) []types.ContentPart {
	if len(parts) == 0 {
		return nil
	}
	result := make([]types.ContentPart, len(parts))
	for i, part := range parts {
		result[i] = part
		if part.Media != nil {
			media := *part.Media
			result[i].Media = &media
		}
	}
	return result
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

func renderContextBlock(block types.ContextBlock) string {
	switch block.Kind {
	case "responsibility":
		return "## Responsibility\n" + block.Text
	case "knowledge":
		return block.Text
	case "input":
		return "## Input\n" + block.Text
	case "team_memory":
		return "## Team Memory\n" + block.Text
	case "agent_memory":
		return "## Agent Memory\n" + block.Text
	case "entity_memory":
		return "## Entity Memory\n" + block.Text
	case "fanout_item":
		return "## Your Item\n" + block.Text
	case "records":
		var records []types.SharedRecord
		if err := json.Unmarshal([]byte(block.Text), &records); err == nil {
			return formatRecords(records)
		}
		return "## Shared Records\n" + block.Text
	default:
		return "## " + block.Kind + "\n" + block.Text
	}
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
You operate as a disciplined executor, not a passive responder.

1. Before acting, restate your plan: the goal, the concrete steps, and the definition of done.
2. Break complex work into small, verifiable steps. Execute them one at a time.
3. Track progress explicitly. After each step, note what is done and what remains.
4. Use tools to gather facts before making decisions; do not guess.
5. If a step fails or is blocked, stop and report the blocker and what you already tried, rather than retrying blindly.
6. Report findings clearly and concretely: what changed, why, and what is next.`

const toolUsageTemplate = `## Tool Usage
Tools are your only way to observe and change the outside world. Treat them as a source of ground truth.

- Before answering from memory, use tools to read the actual current state.
- Prefer the narrowest tool call that answers your question.
- Always check a tool's result before acting on it; a non-empty result is not the same as a correct result.
- If a tool errors, read the error and adapt. Do not repeat the identical call.
- Every tool call consumes part of your turn budget. Batch independent reads; avoid redundant or speculative calls.`

const memoryManagementTemplate = `## Memory Management
You maintain durable memory across turns. Use it to stay consistent and avoid re-deriving what you already learned.

- Record decisions together with their rationale, not just the conclusion.
- Record concrete findings (identifiers, values, facts), not vague impressions.
- Update memory when new information changes or supersedes a prior entry.
- Before redoing work, check whether memory already contains the answer.`

const knowledgeQueryTemplate = `## Knowledge Usage
When background knowledge is injected into your context:
- Treat injected knowledge as guidance, not fact. Current Workspace files and
  Session records always take precedence over knowledge.
- Weigh each entry by its confidence and basis. Lower-confidence or stale
  knowledge must not override what you can verify directly.
- Cross-reference multiple entries when they overlap; note when information is
  uncertain or conflicting.
- Use knowledge to avoid re-deriving known constraints, not to skip verification.`

const perspectiveIsolationTemplate = `## Perspective Isolation
You are one agent in a multi-agent team. Keep your knowledge boundary explicit.

- You know only what you have personally observed or been explicitly told.
- Never assume another agent's state, progress, or decisions.
- When you receive information from another agent, note its source and treat it as unverified until confirmed.
- Return clear findings, decisions, and open questions so the Team can coordinate the next step without guessing.`

const outputFormatTemplate = `## Output Format
Your output must follow the specified structured format.
- Ensure all required fields are present
- Follow the schema exactly
- If you cannot provide a value, use an appropriate default or null`

func structuredOutputContract(schema *types.StructuredOutput) string {
	if schema == nil {
		return ""
	}
	return "## Strict Structured Output Contract\n" +
		"Return exactly one JSON object and nothing else.\n" +
		"Do not write an explanation before or after it.\n" +
		"Do not use Markdown fences such as ```json.\n" +
		"Do not return a second JSON object.\n" +
		"Do not return YAML.\n" +
		"The first character must be { and the last character must be }.\n" +
		"If a field is unavailable, use an empty string, empty array, false, or null according to its type.\n" +
		"The runtime will reject prose, Markdown, YAML, and empty responses."
}
