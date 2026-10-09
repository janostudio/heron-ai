package mcp

import (
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Schema projects an MCP inputSchema onto the engine's tool schema.
//
// The engine's JSONSchema is a deliberately narrow description — a scalar
// type, a description, an enum and a required set per property — so nested
// object and array definitions collapse to their type name. That is lossy for
// deeply nested parameters, and the loss is fine: the schema is what the model
// reads to decide to call the tool, while the server validates the arguments
// against its own full copy. Params are not validated against this projection
// anywhere in the call path, which is what keeps a collapsed `object`
// property from rejecting a correct call.
func (d ToolDefinition) Schema() types.JSONSchema {
	schema := types.JSONSchema{
		Name:        d.Name,
		Description: d.Description,
		Type:        "object",
		Properties:  map[string]types.JSONProperty{},
	}
	if len(d.InputSchema) == 0 {
		return schema
	}
	properties, _ := d.InputSchema["properties"].(map[string]any)
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		entry := types.JSONProperty{
			Type:        propertyType(property),
			Description: stringValue(property["description"]),
		}
		if enum, ok := property["enum"].([]any); ok {
			for _, item := range enum {
				if text, ok := item.(string); ok {
					entry.Enum = append(entry.Enum, text)
				}
			}
		}
		schema.Properties[name] = entry
	}
	if required, ok := d.InputSchema["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				schema.Required = append(schema.Required, name)
			}
		}
	}
	return schema
}

// propertyType normalises a JSON Schema type member. It may be absent, or a
// list of alternatives as in ["string", "null"]; both have to land on one of
// the engine's scalar names.
func propertyType(property map[string]any) string {
	switch declared := property["type"].(type) {
	case string:
		if declared != "" {
			return declared
		}
	case []any:
		for _, item := range declared {
			if text, ok := item.(string); ok && text != "" && text != "null" {
				return text
			}
		}
		if len(declared) > 0 {
			return "string"
		}
	}
	// No usable type: fall back to string rather than dropping the property,
	// because a property the model cannot see is a call it cannot make.
	if _, hasEnum := property["enum"]; hasEnum {
		return "string"
	}
	switch {
	case property["properties"] != nil:
		return "object"
	case property["items"] != nil:
		return "array"
	}
	return "string"
}

func stringValue(v any) string {
	text, _ := v.(string)
	return strings.TrimSpace(text)
}
