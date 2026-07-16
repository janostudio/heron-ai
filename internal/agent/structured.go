package agent

import (
	"encoding/json"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type StructuredOutputManager struct{}

func NewStructuredOutputManager() *StructuredOutputManager {
	return &StructuredOutputManager{}
}

func (m *StructuredOutputManager) ParseAndValidate(raw string, schema *types.StructuredOutput) (any, error) {
	if schema == nil {
		return raw, nil
	}

	var result any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse structured output: %w", err)
	}

	// If schema has required fields, validate them
	if schemaMap, ok := result.(map[string]any); ok {
		for key, val := range schema.Schema {
			required, ok := val.(map[string]any)["required"]
			if ok && required.(bool) {
				if _, exists := schemaMap[key]; !exists {
					return nil, fmt.Errorf("missing required field: %s", key)
				}
			}
		}
	}

	return result, nil
}

func (m *StructuredOutputManager) ToProviderFormat(schema *types.StructuredOutput) map[string]any {
	if schema == nil {
		return nil
	}

	return map[string]any{
		"type":        "json_schema",
		"json_schema": schema.Schema,
	}
}
