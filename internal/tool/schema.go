package tool

import "github.com/heron-ai/heron-engine/pkg/types"

func GenerateSchema(t types.Tool) types.JSONSchema {
	params := t.Parameters()

	schema := types.JSONSchema{
		Type:       "object",
		Properties: make(map[string]types.JSONProperty),
	}

	for name, param := range params {
		if paramMap, ok := param.(map[string]any); ok {
			prop := types.JSONProperty{}
			if t, ok := paramMap["type"].(string); ok {
				prop.Type = t
			}
			if d, ok := paramMap["description"].(string); ok {
				prop.Description = d
			}
			schema.Properties[name] = prop
		}
	}

	return schema
}

func GenerateSchemas(tools []types.Tool) []types.JSONSchema {
	schemas := make([]types.JSONSchema, len(tools))
	for i, t := range tools {
		schemas[i] = GenerateSchema(t)
	}
	return schemas
}
