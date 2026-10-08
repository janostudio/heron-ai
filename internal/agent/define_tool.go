package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/heron-ai/heron-engine/internal/definitions"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// DefineTool is the builtin Define tool. It is the model-facing surface of
// internal/definitions: one call turns a spec into validated config files and
// publishes them to the live runtime.
//
// The tool resolves its identity from the execution context exactly like State
// and Spawn do, and it takes the identity for a different reason than they do:
// a definition is a tree-wide object, so there is nothing to scope it to an
// agent. Requiring an agent context is what keeps Define reachable only from
// inside a running AgentTurn, where the reload it triggers cannot race the
// turn that is reading the tree — the next turn reads the new tree, the
// current one finishes against the one it started with.
//
// Serial execution is the other half of that: two concurrent Define calls
// would interleave their read-modify-write of the same flow file, and the
// second would merge against a snapshot the first has already invalidated.
// Classification here, not just the writer's internal mutex, is what keeps a
// single ModelCall from fanning several creates into one another's staging
// trees.
type DefineTool struct {
	writer DefinitionWriter

	// store is the definition store the tool reports against. It is not
	// needed to write files — the writer owns that — but a nil store means the
	// tool was constructed for an engine that can never publish a change, so
	// holding it lets the nil-guard fail loudly instead of writing files into a
	// tree nothing will read.
	store *types.DefinitionStore
}

// DefinitionWriter is the slice of internal/definitions.Writer the tool needs.
//
// Declared here rather than taking *definitions.Writer directly so the tool
// depends on the behaviour it calls and not on the concrete writer, and so a
// test can assert what was forwarded without a real config tree.
type DefinitionWriter interface {
	CreateAgent(ctx context.Context, req definitions.CreateAgentRequest) (*definitions.ApplyResult, error)
	CreateTeam(ctx context.Context, req definitions.CreateTeamRequest) (*definitions.ApplyResult, error)
}

// NewDefineTool creates the Define tool over a writer and the store the
// resulting definitions are published into.
func NewDefineTool(writer DefinitionWriter, store *types.DefinitionStore) *DefineTool {
	return &DefineTool{writer: writer, store: store}
}

func (t *DefineTool) Name() string { return "Define" }

// Description is read by the model to decide whether to call the tool at all,
// so it carries the three facts a spec alone does not imply: changes land on
// the next turn, create_agent does not schedule anything, and upsert merges
// rather than replaces.
func (t *DefineTool) Description() string {
	return "Create or update this engine's Agent and Team definitions from a spec. " +
		"The definitions are written, validated and published immediately, but they take effect on the NEXT turn — " +
		"nothing already scheduled in the current turn can be retargeted to them. " +
		"action=create_agent writes one agent definition only: it is NOT added to any team and NOT bound into the flow, " +
		"so an agent created this way is valid but never scheduled until a create_team call references it. " +
		"action=create_team writes one team definition, creates any agent its calls reference but the tree does not have, " +
		"and binds the team into the flow when spec.bind is present. " +
		"mode=create fails if the target already exists; mode=upsert merges the spec into the existing definition " +
		"(nested objects merge by key; lists such as tools.builtin, skills, knowledge and rules are replaced wholesale; " +
		"body is replaced wholesale; omitted fields are preserved)."
}

// NeedsApproval is false by product decision: creation is routine and the gate
// that matters is already downstream — every spec is validated through the real
// config loader against a staged tree and rolled back if it does not load, so
// an approval prompt would only add a click to a change that cannot land broken.
func (t *DefineTool) NeedsApproval() bool { return false }

func (t *DefineTool) Execution() types.ToolExecutionSpec {
	return types.ToolExecutionSpec{Class: types.ToolSerial}
}

// Parameters declares the tool schema. The spec contents are action-dependent,
// so they are documented in the spec description rather than as nested schema
// properties: the shapes differ per action and a model reading one description
// beats one field for each of twenty keys.
func (t *DefineTool) Parameters() map[string]any {
	return map[string]any{
		"action": map[string]any{
			"type":        "string",
			"enum":        []string{"create_agent", "create_team"},
			"description": "What to define: create_agent writes one agent definition, create_team writes one team definition (plus the agents its calls need, plus its flow binding)",
			"required":    true,
		},
		"mode": map[string]any{
			"type":        "string",
			"enum":        []string{"create", "upsert"},
			"description": "create (default) fails if the name already exists; upsert merges the spec into the existing definition, preserving omitted fields",
		},
		"template": map[string]any{
			"type": "string",
			"description": "Optional built-in template to start from. Valid names: " +
				strings.Join(definitions.TemplateNames(), ", ") +
				". Omit for the built-in default shape.",
		},
		"spec": map[string]any{
			"type": "object",
			"description": "The definition content. " +
				"create_agent: name (required), persona.role/persona.goal/persona.backstory, model, " +
				"tools.builtin (list), skills (list), knowledge (list), rules (list), loop, hitl, body (the system prompt markdown). " +
				"create_team: id (required), goal, state, output, " +
				"calls (map of callName -> {type, agent, responsibility, depends_on, output}), " +
				"agent_specs (map of agentID -> agent spec, for agents created alongside the team), " +
				"bind ({key, coordinator, can_activate, depends_on, inputs, on_proceed}) to wire the team into the current flow. " +
				"name and id are interchangeable.",
			"required": true,
		},
	}
}

func (t *DefineTool) Execute(ctx context.Context, params map[string]any) (*types.ToolResult, error) {
	if t == nil || t.writer == nil || t.store == nil {
		return &types.ToolResult{Success: false, Error: "define tool is not configured"}, nil
	}
	agentID := currentAgentID(ctx)
	if agentID == "" {
		return &types.ToolResult{Success: false, Error: "define tool is only available inside an Agent execution context"}, nil
	}

	action := strings.TrimSpace(stringParam(params, "action"))
	mode, err := parseDefineMode(params)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	template := strings.TrimSpace(stringParam(params, "template"))
	spec, err := defineSpecParam(params)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}

	var result *definitions.ApplyResult
	switch action {
	case "create_agent":
		result, err = t.writer.CreateAgent(ctx, definitions.CreateAgentRequest{
			Mode:     mode,
			Template: template,
			Spec:     spec,
		})
	case "create_team":
		result, err = t.writer.CreateTeam(ctx, definitions.CreateTeamRequest{
			Mode:     mode,
			Template: template,
			Spec:     spec,
		})
	default:
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("unknown action %q", action)}, nil
	}
	if err != nil {
		// The writer's errors are already written for the model — they name the
		// offending field and the validation reason — so they are passed
		// through verbatim rather than wrapped into something less specific.
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	if result == nil {
		return &types.ToolResult{Success: false, Error: "definition writer returned no result"}, nil
	}

	return t.report(action, mode, result)
}

// report turns an ApplyResult into the model's view of what happened.
//
// The JSON shape is the contract the model reads to decide its next call, so
// it states the two things the writer's own result cannot: that the change is
// not visible to the current turn, and what to do next. Without the hint a
// model that just created an agent has no signal that nothing will ever
// schedule it.
func (t *DefineTool) report(action string, mode definitions.Mode, result *definitions.ApplyResult) (*types.ToolResult, error) {
	// A nil Files slice must still marshal as [] rather than null: the model
	// reads this as a list and null is a shape it has to handle specially.
	files := result.Files
	if files == nil {
		files = []string{}
	}
	created := result.CreatedAgents
	if created == nil {
		created = []string{}
	}
	updated := result.UpdatedAgents
	if updated == nil {
		updated = []string{}
	}

	reload := "ok"
	if !result.Reloaded {
		reload = "pending: the files are on disk but not live yet; the next turn will still see the previous definitions until a reload or restart"
	}

	payload := map[string]any{
		"action":         action,
		"mode":           result.Mode,
		"files":          files,
		"created_agents": created,
		"updated_agents": updated,
		"reload":         reload,
		"effective":      "next_turn",
		"hint":           defineHint(action, mode, result),
	}
	if result.BoundToFlow != "" {
		payload["bound_to_flow"] = result.BoundToFlow
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	// Metadata carries the same object so event logs and the UI can read the
	// outcome structurally. The runtime flattens Metadata into the content
	// string for the model, so the duplication costs the model nothing and
	// saves every other consumer a re-parse of Content.
	return &types.ToolResult{Success: true, Content: string(data), Metadata: payload}, nil
}

// defineHint is the action-oriented next step, and it deliberately differs by
// action — the failure mode this guards against is a model that creates an
// agent, reports success, and moves on while the agent is never scheduled.
func defineHint(action string, mode definitions.Mode, result *definitions.ApplyResult) string {
	if mode == definitions.ModeUpsert && result.Mode == "updated" {
		return fmt.Sprintf(
			"Existing %s definition(s) were merged, not replaced: nested objects merged by key, "+
				"lists (tools.builtin, skills, knowledge, rules) and body replaced wholesale, omitted fields kept. "+
				"Verify the merged result on the next turn.",
			action,
		)
	}

	switch action {
	case "create_agent":
		return "The agent definition exists but is not scheduled by any team and is not bound into the flow, " +
			"so nothing will call it yet. Call this tool with action=create_team and reference the agent in spec.calls " +
			"(plus spec.bind) to wire it into the orchestration graph."
	case "create_team":
		if result.BoundToFlow != "" {
			return fmt.Sprintf(
				"The team is bound into flow %q and becomes routable on the next turn. "+
					"Reference it by its binding key in subsequent calls and persists across turns.",
				result.BoundToFlow,
			)
		}
		return "The team definition exists but is not bound into the flow (no spec.bind was given), " +
			"so it is valid but not routable. Call create_team again with spec.bind to wire it in."
	default:
		return "The definition change becomes effective on the next turn."
	}
}

// parseDefineMode reads the optional mode, defaulting to create.
//
// There is no "patch": a merge that only touched the fields it was given is
// exactly what upsert already is, and a third spelling for the same semantics
// would only give the model another way to be wrong.
func parseDefineMode(params map[string]any) (definitions.Mode, error) {
	raw := strings.TrimSpace(stringParam(params, "mode"))
	switch raw {
	case "":
		return definitions.ModeCreate, nil
	case string(definitions.ModeCreate):
		return definitions.ModeCreate, nil
	case string(definitions.ModeUpsert):
		return definitions.ModeUpsert, nil
	default:
		return "", fmt.Errorf("unknown mode %q", raw)
	}
}

// defineSpecParam extracts the required spec object.
//
// A JSON-decoded spec always arrives as map[string]any, and the distinction
// between "no spec" and "a spec of the wrong type" is worth keeping: they are
// different mistakes and the model fixes them differently.
func defineSpecParam(params map[string]any) (map[string]any, error) {
	raw, ok := params["spec"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("spec is required")
	}
	spec, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("spec must be an object")
	}
	return spec, nil
}

// stringParam reads a string parameter, tolerating a nil or absent value.
func stringParam(params map[string]any, name string) string {
	value, _ := params[name].(string)
	return value
}
