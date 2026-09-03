package model

import (
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
