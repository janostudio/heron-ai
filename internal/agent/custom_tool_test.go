package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// schemaAwareExecutor is a ToolExecutor that can also describe its tools, the
// way internal/tool.ToolExecutor does through the registry.
type schemaAwareExecutor struct {
	schemas map[string]types.JSONSchema
}

func (s *schemaAwareExecutor) Execute(_ context.Context, name string, _ map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true, Content: name}, nil
}

func (s *schemaAwareExecutor) ToolSchema(name string) (types.JSONSchema, bool) {
	schema, ok := s.schemas[name]
	return schema, ok
}

// TestCustomToolsAreAdvertised covers `tools.custom`: the field is documented
// and the executor can run those tools, but buildToolSchemas only walked
// builtin and MCP names, so the model was never told the tool exists and never
// called it.
func TestCustomToolsAreAdvertised(t *testing.T) {
	executor := &schemaAwareExecutor{schemas: map[string]types.JSONSchema{
		"Deploy": {Name: "Deploy", Type: "object", Properties: map[string]types.JSONProperty{
			"env": {Type: "string", Description: "target environment"},
		}},
	}}
	loop := NewTurnLoop(&mockModelProvider{}, executor, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}})

	schemas := loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Custom: []string{"Deploy", "NotRegistered"}},
	})

	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	require.Contains(t, names, "Deploy", "a registered custom tool must be advertised")
	require.NotContains(t, names, "NotRegistered", "an unknown name must not invent an empty schema")
}

// TestCustomToolsNeedASchemaProvider keeps the fallback honest: an executor
// without the capability advertises nothing rather than an empty schema.
func TestCustomToolsNeedASchemaProvider(t *testing.T) {
	loop := NewTurnLoop(&mockModelProvider{}, &mockToolExecutor{}, nil, NewRouteParser(), nil, NewHookExecutor(),
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hi"}}})
	require.Empty(t, loop.buildToolSchemas(types.AgentConfig{
		Tools: types.ToolConfig{Custom: []string{"Deploy"}},
	}))
}
