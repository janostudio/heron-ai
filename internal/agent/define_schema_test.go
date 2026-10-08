package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/definitions"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestBuildToolSchemasIncludesDefine guards the SILENT half of the
// double-registration that makes the Define tool reachable.
//
// internal/app registers the tool with the tool registry, which makes it
// executable. Visibility to the model is a second, independent claim: the
// hardcoded builtinSchemas map below buildToolSchemas, whose lookup loop
// SKIPS an unknown name without an error. A tool registered but missing from
// that map therefore has no symptom whatsoever — the executor would run it
// happily if the model ever asked, but the model is never told it exists, and
// every test that drives the executor directly still passes.
//
// This test is what makes that failure loud. It asserts the model-visible
// surface, not the execution surface.
func TestBuildToolSchemasIncludesDefine(t *testing.T) {
	loop := &TurnLoop{}

	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Define"}},
	})

	require.Len(t, schemas, 1, "an agent declaring only Define must see exactly one schema")
	schema := schemas[0]
	require.Equal(t, "Define", schema.Name)

	action, ok := schema.Properties["action"]
	require.True(t, ok, "action must be declared")
	assert.Equal(t, "string", action.Type)
	assert.Equal(t, []string{"create_agent", "create_team"}, action.Enum)
	assert.Contains(t, schema.Required, "action")

	spec, ok := schema.Properties["spec"]
	require.True(t, ok, "spec must be declared")
	assert.Equal(t, "object", spec.Type)
	assert.Contains(t, schema.Required, "spec")

	mode, ok := schema.Properties["mode"]
	require.True(t, ok, "mode must be declared")
	assert.Equal(t, []string{"create", "upsert"}, mode.Enum)

	// The template list is read from definitions.TemplateNames(), so it cannot
	// drift when a template is added — same contract the tool's own
	// Parameters() test pins.
	template, ok := schema.Properties["template"]
	require.True(t, ok, "template must be declared")
	for _, name := range definitions.TemplateNames() {
		assert.Contains(t, template.Description, name,
			"the template description must list %q so the model can name a real template", name)
	}

	// The descriptions are not decoration: they are the only place the model
	// learns when a change lands and what create_agent does not do. A thin
	// description here degrades tool use with no failing assertion anywhere,
	// so the two load-bearing facts are asserted by content.
	assert.Contains(t, schema.Description, "NEXT turn")
	assert.Contains(t, schema.Description, "NOT added to any team")
	assert.Contains(t, spec.Description, "create_agent")
	assert.Contains(t, spec.Description, "create_team")

	// An agent that does not declare Define must not receive it: the schema
	// list is filtered by tools.builtin, and Define is not implicitly granted.
	undeclared := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Read"}},
	})
	for _, s := range undeclared {
		assert.NotEqual(t, "Define", s.Name, "Define must stay opt-in via tools.builtin")
	}
}

// TestBuildToolSchemasDefineMatchesToolParameters pins the second half of the
// duplication: builtinSchemas restates what (*DefineTool).Parameters() already
// declares, and the two are edited by hand. This asserts the restatement
// agrees with the source of truth on everything the model acts on, so a change
// to Parameters() that is not mirrored in the schema map fails here instead of
// quietly shipping two different contracts.
func TestBuildToolSchemasDefineMatchesToolParameters(t *testing.T) {
	params := NewDefineTool(&fakeDefinitionWriter{}, types.NewDefinitionStore(&types.Definitions{}, "", "")).Parameters()

	schemas := (&TurnLoop{}).buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Define"}},
	})
	require.Len(t, schemas, 1)
	schema := schemas[0]

	require.Len(t, schema.Properties, len(params),
		"the schema must declare exactly the parameters the tool declares")

	for name, raw := range params {
		declared, ok := raw.(map[string]any)
		require.True(t, ok, "parameter %q must be a map", name)

		property, ok := schema.Properties[name]
		require.True(t, ok, "parameter %q is declared by the tool but missing from builtinSchemas", name)

		if want, _ := declared["type"].(string); want != "" {
			assert.Equal(t, want, property.Type, "type mismatch for %q", name)
		}
		if want, _ := declared["description"].(string); want != "" {
			assert.Equal(t, want, property.Description, "description mismatch for %q", name)
		}
		if want, ok := declared["enum"].([]string); ok {
			assert.Equal(t, want, property.Enum, "enum mismatch for %q", name)
		}
		if required, _ := declared["required"].(bool); required {
			assert.Contains(t, schema.Required, name, "parameter %q is required by the tool", name)
		}
	}

	assert.Equal(t, NewDefineTool(&fakeDefinitionWriter{}, types.NewDefinitionStore(&types.Definitions{}, "", "")).Description(),
		schema.Description, "the tool description must be mirrored verbatim")
}

// TestDefineToolSerialExecutionIsModelVisibleClass pins the classification the
// parallel-safe scheduler reads. It is asserted here (not only in
// define_tool_test.go) because the schema map is where the registry-facing
// name is claimed: a Define call that the scheduler believed was parallel-safe
// could interleave two applies' read-modify-write of the same flow file.
func TestDefineToolSerialExecutionIsModelVisibleClass(t *testing.T) {
	loop := &TurnLoop{}

	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Builtin: []string{"Define"}},
	})
	require.Len(t, schemas, 1)
	require.Equal(t, "Define", schemas[0].Name)

	tool := NewDefineTool(&fakeDefinitionWriter{}, types.NewDefinitionStore(&types.Definitions{}, "", ""))
	assert.Equal(t, types.ToolSerial, tool.Execution().Class,
		"two concurrent Define calls would interleave one flow file's read-modify-write")
}
