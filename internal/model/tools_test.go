package model

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestConvertOpenAIToolsOmitsAnyType(t *testing.T) {
	tools := convertOpenAITools([]types.JSONSchema{{
		Name: "Spawn",
		Type: "object",
		Properties: map[string]types.JSONProperty{
			"item":    {Type: "any", Description: "single item"},
			"key":     {Type: "string"},
			"deliver": {Type: "string", Enum: []string{"parent", "downstream"}},
		},
	}})
	require.Len(t, tools, 1)
	params := tools[0].Function.Parameters
	properties, ok := params["properties"].(map[string]any)
	require.True(t, ok)

	item, ok := properties["item"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, item, "type")
	assert.Equal(t, "single item", item["description"])

	key, ok := properties["key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", key["type"])

	deliver, ok := properties["deliver"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []string{"parent", "downstream"}, deliver["enum"])
}

func TestConvertAnthropicToolsOmitsAnyType(t *testing.T) {
	tools := convertAnthropicTools([]types.JSONSchema{{
		Name: "Spawn",
		Type: "object",
		Properties: map[string]types.JSONProperty{
			"item": {Type: "any"},
			"key":  {Type: "string"},
		},
	}})
	require.Len(t, tools, 1)
	properties, ok := tools[0].InputSchema["properties"].(map[string]any)
	require.True(t, ok)

	item, ok := properties["item"].(map[string]any)
	require.True(t, ok)
	assert.NotContains(t, item, "type")

	key, ok := properties["key"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", key["type"])
}

// TestResponseFormatNormalizesYAMLV2Maps guards against the
// "json: unsupported type: map[interface {}]interface {}" error: agent
// frontmatter is parsed by adrg/frontmatter (yaml.v2), which decodes nested
// mappings as map[interface{}]interface{}. Those must be rewritten before the
// request body is marshaled.
func TestResponseFormatNormalizesYAMLV2Maps(t *testing.T) {
	schema := &types.StructuredOutput{
		Type: "object",
		Schema: map[string]any{
			"report": map[string]any{
				"type": "object",
				"properties": map[string]any{
					// Simulates a nested value decoded by yaml.v2 as a
					// non-string-keyed map.
					"meta": map[interface{}]interface{}{
						"unit": "bytes",
						"tags": []interface{}{"a", "b"},
					},
				},
			},
		},
	}

	format := responseFormat(schema)
	require.NotNil(t, format)

	// The whole ResponseFormat must marshal without error.
	data, err := json.Marshal(format)
	require.NoError(t, err)

	// The nested map must have been rewritten to a string-keyed map.
	require.Contains(t, string(data), `"unit":"bytes"`)
}

// TestNormalizeJSONValueRecurses verifies the recursive rewriting handles
// maps, slices, and nested combinations.
func TestNormalizeJSONValueRecurses(t *testing.T) {
	in := map[string]any{
		"nested": map[interface{}]interface{}{
			"list": []interface{}{
				map[interface{}]interface{}{"k": "v"},
			},
		},
	}

	out := normalizeJSONValue(in)
	_, err := json.Marshal(out)
	require.NoError(t, err)

	root, ok := out.(map[string]any)
	require.True(t, ok)
	nested, ok := root["nested"].(map[string]any)
	require.True(t, ok)
	list, ok := nested["list"].([]any)
	require.True(t, ok)
	item, ok := list[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "v", item["k"])
}

// TestAnthropicStructuredFormatNormalizesYAMLV2Maps guards the anthropic
// structured-output path against the same yaml.v2 decode artifact that
// TestResponseFormatNormalizesYAMLV2Maps covers for openai: nested
// map[interface{}]interface{} values must be rewritten to string-keyed maps so
// the *anthropicOutputFormat can be marshaled.
func TestAnthropicStructuredFormatNormalizesYAMLV2Maps(t *testing.T) {
	schema := &types.StructuredOutput{
		Type: "object",
		Schema: map[string]any{
			"report": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"meta": map[interface{}]interface{}{
						"unit": "bytes",
						"tags": []interface{}{"a", "b"},
					},
				},
			},
		},
	}

	format := anthropicStructuredFormat(schema)
	require.NotNil(t, format)

	// The returned *anthropicOutputFormat must marshal without error.
	data, err := json.Marshal(format)
	require.NoError(t, err)

	// The nested map must have been rewritten to a string-keyed map.
	require.Contains(t, string(data), `"unit":"bytes"`)

	// The top-level format carries the expected type.
	assert.Equal(t, "json_schema", format.Type)
}

// TestAnthropicStructuredFormatShape verifies the returned format's top-level
// shape and that its schema is string-keyed all the way down.
func TestAnthropicStructuredFormatShape(t *testing.T) {
	schema := &types.StructuredOutput{
		Type: "object",
		Schema: map[string]any{
			"report": map[string]any{
				"type":     "object",
				"required": true,
				"properties": map[interface{}]interface{}{
					"title": map[string]any{"type": "string"},
				},
			},
		},
	}

	format := anthropicStructuredFormat(schema)
	require.NotNil(t, format)
	assert.Equal(t, "json_schema", format.Type)

	root, ok := format.Schema["properties"].(map[string]any)
	require.True(t, ok)
	report, ok := root["report"].(map[string]any)
	require.True(t, ok)
	// "required" must be stripped from the property and promoted to the
	// top-level required list.
	assert.NotContains(t, report, "required")
	required, ok := format.Schema["required"].([]string)
	require.True(t, ok)
	assert.Equal(t, []string{"report"}, required)

	// The nested "properties" (a map[interface{}]interface{} decoded by
	// yaml.v2) must be rewritten to a string-keyed map.
	nestedProps, ok := report["properties"].(map[string]any)
	require.True(t, ok)
	title, ok := nestedProps["title"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "string", title["type"])
}

// TestNormalizeJSONValueNonStringKeys verifies map[interface{}]interface{}
// entries whose keys are not strings are converted via fmt.Sprint.
func TestNormalizeJSONValueNonStringKeys(t *testing.T) {
	in := map[interface{}]interface{}{
		1:    "x",
		true: "y",
	}

	out := normalizeJSONValue(in)
	_, err := json.Marshal(out)
	require.NoError(t, err)

	m, ok := out.(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "x", m["1"])
	assert.Equal(t, "y", m["true"])
}

// TestNormalizeJSONValueEmptyAndNil verifies empty maps, empty slices, and nil
// fall through the default branch unchanged.
func TestNormalizeJSONValueEmptyAndNil(t *testing.T) {
	assert.Nil(t, normalizeJSONValue(nil))
	assert.Equal(t, "scalar", normalizeJSONValue("scalar"))
	assert.Equal(t, 42, normalizeJSONValue(42))

	emptyMap := map[interface{}]interface{}{}
	normalizedEmptyMap := normalizeJSONValue(emptyMap)
	nm, ok := normalizedEmptyMap.(map[string]any)
	require.True(t, ok)
	assert.Empty(t, nm)

	emptySlice := []interface{}{}
	normalizedEmptySlice := normalizeJSONValue(emptySlice)
	ns, ok := normalizedEmptySlice.([]any)
	require.True(t, ok)
	assert.Empty(t, ns)
}
