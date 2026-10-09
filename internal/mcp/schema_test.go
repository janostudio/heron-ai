package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToolDefinition_Schema(t *testing.T) {
	def := ToolDefinition{
		Server:      "github",
		Name:        "search",
		Description: "Search code",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query":     map[string]any{"type": "string", "description": "what to search"},
				"limit":     map[string]any{"type": "integer"},
				"verbose":   map[string]any{"type": "boolean"},
				"languages": map[string]any{"type": "array"},
				"options":   map[string]any{"type": "object"},
				"mode":      map[string]any{"type": "string", "enum": []any{"fast", "deep"}},
			},
			"required": []any{"query"},
		},
	}

	schema := def.Schema()
	assert.Equal(t, "search", schema.Name)
	assert.Equal(t, "Search code", schema.Description)
	assert.Equal(t, "object", schema.Type)
	assert.Equal(t, []string{"query"}, schema.Required)

	assert.Equal(t, "string", schema.Properties["query"].Type)
	assert.Equal(t, "what to search", schema.Properties["query"].Description)
	assert.Equal(t, "integer", schema.Properties["limit"].Type)
	assert.Equal(t, "boolean", schema.Properties["verbose"].Type)
	assert.Equal(t, "array", schema.Properties["languages"].Type)
	assert.Equal(t, "object", schema.Properties["options"].Type)
	assert.Equal(t, "string", schema.Properties["mode"].Type)
	assert.Equal(t, []string{"fast", "deep"}, schema.Properties["mode"].Enum)
}

// A JSON Schema type may be a list ("null" is how an optional field is
// expressed) or absent entirely. Both have to land on one of the engine's
// scalar names rather than leaving a property untyped, because an untyped
// property is one the provider may drop.
func TestToolDefinition_SchemaNormalisesTypes(t *testing.T) {
	def := ToolDefinition{
		Name: "t",
		InputSchema: map[string]any{
			"properties": map[string]any{
				"nullable": map[string]any{"type": []any{"string", "null"}},
				"untyped":  map[string]any{"description": "no type"},
				"choicy":   map[string]any{"enum": []any{"a", "b"}},
				"nested":   map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}},
				"listed":   map[string]any{"items": map[string]any{"type": "string"}},
			},
		},
	}

	properties := def.Schema().Properties
	assert.Equal(t, "string", properties["nullable"].Type)
	assert.Equal(t, "string", properties["untyped"].Type)
	assert.Equal(t, "string", properties["choicy"].Type)
	assert.Equal(t, "object", properties["nested"].Type)
	assert.Equal(t, "array", properties["listed"].Type)
}

func TestToolDefinition_SchemaWithoutInputSchema(t *testing.T) {
	schema := ToolDefinition{Name: "bare", Description: "no schema"}.Schema()
	assert.Equal(t, "bare", schema.Name)
	assert.Equal(t, "object", schema.Type)
	assert.Empty(t, schema.Required)
	assert.NotNil(t, schema.Properties)
}

func TestToolDefinition_SchemaIgnoresMalformedProperties(t *testing.T) {
	def := ToolDefinition{
		Name: "t",
		InputSchema: map[string]any{
			"properties": map[string]any{"broken": "not-an-object"},
			"required":   "not-a-list",
		},
	}
	schema := def.Schema()
	require.NotNil(t, schema.Properties)
	assert.Empty(t, schema.Properties)
	assert.Empty(t, schema.Required)
}
