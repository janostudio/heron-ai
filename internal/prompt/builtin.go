package prompt

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// BuiltinTemplates contains all built-in prompt templates
var BuiltinTemplates = map[string]string{
	"execution-management":  executionManagementTemplate,
	"tool-usage":            toolUsageTemplate,
	"state-management":      stateManagementTemplate,
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
// renderer does not fetch state, knowledge, or records itself; those are
// injected by the runtime so the prompt layer stays deterministic.
type RenderContext struct {
	Variables     map[string]string
	ContextBlocks []types.ContextBlock
}

// Render builds the message sequence for one Agent turn.
//
// Provenance is carried by the protocol (role, plus OpenAI's `name`), not by
// prose in the content. The order is by stability: knowledge is byte-stable and
// goes first so it stays in the cacheable prefix and inside the region
// compaction protects; memory and collaborator output change every turn; the
// user's task goes last so the model answers it instead of continuing its own
// text.
func (p *PromptRenderer) Render(agent types.AgentConfig, req types.AgentRequest, rctx RenderContext) ([]types.Message, error) {
	systemPrompt := p.BuildSystemPrompt(agent, rctx)
	if team := strings.TrimSpace(req.TeamID); team != "" {
		systemPrompt += fmt.Sprintf(
			"\n\nYour team is %s. A message carrying another name comes from that team; a message with no name is your own.",
			team,
		)
	}
	systemParts := contextParts(rctx.ContextBlocks, "system")

	messages := []types.Message{{
		Role: "system", Content: systemPrompt, Parts: systemParts,
	}}

	for _, block := range rctx.ContextBlocks {
		if block.Kind != "knowledge" || strings.TrimSpace(block.Text) == "" {
			continue
		}
		messages = append(messages, types.Message{
			Role: "user", Content: wrapSystemReminder(block.Text), Injected: true,
		})
	}

	for _, block := range rctx.ContextBlocks {
		if !isMemoryKind(block.Kind) || strings.TrimSpace(block.Text) == "" {
			continue
		}
		messages = append(messages, types.Message{
			Role: "assistant", Content: renderContextBlock(block), Injected: true,
		})
	}

	unattributed := make([]string, 0, 2)
	for _, group := range collaboratorGroups(rctx.ContextBlocks) {
		if group.source == "" {
			unattributed = append(unattributed, group.text)
			continue
		}
		messages = append(messages, types.Message{
			Role: "assistant", TeamID: group.source, Content: group.text, Injected: true,
		})
	}

	userPrompt := p.BuildUserPrompt(agent, req, rctx)
	if len(unattributed) > 0 {
		// A record without a producer cannot be attributed, and an unnamed
		// assistant message would read as the model's own words. Keep it in the
		// user turn, marked as harness-supplied context.
		extra := wrapSystemReminder(strings.Join(unattributed, "\n\n"))
		if strings.TrimSpace(userPrompt) == "" {
			userPrompt = extra
		} else {
			userPrompt = extra + "\n\n" + userPrompt
		}
	}
	userParts := contextParts(rctx.ContextBlocks, "user")
	if strings.TrimSpace(userPrompt) == "" && len(userParts) == 0 {
		// A Team that receives no user input still needs a user turn to answer;
		// otherwise the request ends on an assistant message and the model
		// continues its own text instead of responding.
		userPrompt = "Continue with the task above."
	}
	messages = append(messages, types.Message{
		Role: userRole(userPrompt, userParts), Content: userPrompt, Parts: userParts, Injected: true,
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

	// State management instructions are stable Agent policy. The actual
	// Team/Agent State snapshots are dynamic and belong in the user context
	// below, not in the cacheable system prefix.
	parts = append(parts, GetTemplate("state-management"))

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

// BuildUserPrompt builds the trailing user turn: the task itself plus any
// context that has no speaker (the item this Agent owns).
func (p *PromptRenderer) BuildUserPrompt(_ types.AgentConfig, req types.AgentRequest, rctx RenderContext) string {
	if len(rctx.ContextBlocks) > 0 {
		var parts []string
		for _, block := range rctx.ContextBlocks {
			if block.Placement == "system" || (strings.TrimSpace(block.Text) == "" && len(block.Parts) == 0) {
				continue
			}
			// Provenance-bearing blocks become their own messages; repeating
			// them here would duplicate content and lose the role that carries
			// the source.
			if isCollaboratorKind(block.Kind) || isMemoryKind(block.Kind) || block.Kind == "knowledge" {
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
	case "team_state":
		return "## Team State\n" + block.Text
	case "agent_state":
		return "## Agent State\n" + block.Text
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

const stateManagementTemplate = `## State Management
You maintain durable state across turns. Use it to stay consistent and avoid re-deriving what you already learned.

- Record decisions together with their rationale, not just the conclusion.
- Record concrete findings (identifiers, values, facts), not vague impressions.
- Update state when new information changes or supersedes a prior entry.
- Before redoing work, check whether state already contains the answer.`

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
		// Without this line the model writes every prose field as one long
		// line: constrained to output only JSON, it plays safe and omits
		// newlines inside strings entirely, so multi-point replies arrive as a
		// single unreadable paragraph (ISSUE-structured-output-forces-single-line).
		// Newlines inside a JSON string are legal and encouraged for
		// readability; say so explicitly, because the other rules here are all
		// prohibitions and the model generalizes them into "no formatting".
		"Inside string fields, use `\\n` to separate points, steps, or paragraphs — newlines in string values are valid JSON and make long replies readable; do not collapse everything onto one line.\n" +
		"The runtime will reject prose, Markdown, YAML, and empty responses."
}
