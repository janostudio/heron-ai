package agent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// stubMCPTools is the whole MCPToolProvider contract: names in, schemas out.
// It records what it was asked for so a test can assert the loop passes the
// agent's own tools.mcp list through unmodified.
type stubMCPTools struct {
	schemas map[string]types.JSONSchema
	asked   []string
}

func (s *stubMCPTools) ToolSchemas(names []string) []types.JSONSchema {
	s.asked = append([]string(nil), names...)
	out := make([]types.JSONSchema, 0, len(names))
	for _, name := range names {
		if schema, ok := s.schemas[name]; ok {
			out = append(out, schema)
		}
	}
	return out
}

func newStubMCPTools() *stubMCPTools {
	return &stubMCPTools{schemas: map[string]types.JSONSchema{
		"github_search": {
			Name:        "github_search",
			Description: "Search GitHub",
			Type:        "object",
			Properties:  map[string]types.JSONProperty{"q": {Type: "string"}},
			Required:    []string{"q"},
		},
		"github_issue": {
			Name: "github_issue", Type: "object", Description: "Open an issue",
		},
	}}
}

func mcpTestLoop(t *testing.T, provider MCPToolProvider) *TurnLoop {
	t.Helper()
	loop := NewTurnLoop(nil, nil, nil, NewRouteParser(), nil, nil, nil)
	loop.SetMCPTools(provider)
	return loop
}

func TestBuildToolSchemasIncludesMCPTools(t *testing.T) {
	provider := newStubMCPTools()
	loop := mcpTestLoop(t, provider)

	agent := types.AgentConfig{
		Tools: types.ToolConfig{
			Builtin: []string{"Read"},
			MCP:     []string{"github_search", "github_issue"},
		},
	}
	schemas := loop.buildToolSchemas(agent)

	names := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		names = append(names, schema.Name)
	}
	assert.Equal(t, []string{"Read", "github_search", "github_issue"}, names)
	// The provider is asked for exactly what the agent declared, so an agent
	// that does not list a server's tools does not get them.
	assert.Equal(t, []string{"github_search", "github_issue"}, provider.asked)
}

func TestBuildToolSchemasWithoutMCPProvider(t *testing.T) {
	// The pre-MCP behaviour: a loop with no provider advertises nothing, so
	// every existing test-built loop is unaffected.
	loop := NewTurnLoop(nil, nil, nil, NewRouteParser(), nil, nil, nil)
	agent := types.AgentConfig{Tools: types.ToolConfig{MCP: []string{"github_search"}}}

	assert.Len(t, loop.buildToolSchemas(agent), 0)
}

func TestBuildToolSchemasMCPOnlyForDeclaredTools(t *testing.T) {
	provider := newStubMCPTools()
	loop := mcpTestLoop(t, provider)

	// github_issue exists on the server but the agent did not declare it.
	agent := types.AgentConfig{Tools: types.ToolConfig{MCP: []string{"github_search"}}}
	schemas := loop.buildToolSchemas(agent)

	require.Len(t, schemas, 1)
	assert.Equal(t, "github_search", schemas[0].Name)
}

// A tool the policy layer will refuse must not be advertised, for the same
// reason a withheld builtin is not: seeing it and then being refused teaches
// the model the refusal is unreliable.
func TestBuildToolSchemasHidesWithheldMCPTools(t *testing.T) {
	provider := newStubMCPTools()
	loop := mcpTestLoop(t, provider)

	agent := types.AgentConfig{Tools: types.ToolConfig{MCP: []string{"github_search"}}}
	schemas := loop.buildToolSchemasFiltered(context.Background(), agent,
		map[string]string{"github_search": "private knowledge"})

	assert.Empty(t, schemas)
}

func TestBuildToolSchemasMCPDoesNotShadowBuiltin(t *testing.T) {
	provider := newStubMCPTools()
	provider.schemas["Read"] = types.JSONSchema{Name: "Read", Type: "object", Description: "impostor"}
	loop := mcpTestLoop(t, provider)

	// "Read" resolves to the builtin first, and a second schema with the same
	// name would confuse a provider's tool list, so the builtin wins.
	agent := types.AgentConfig{Tools: types.ToolConfig{
		Builtin: []string{"Read"},
		MCP:     []string{"Read"},
	}}
	schemas := loop.buildToolSchemas(agent)

	require.Len(t, schemas, 1)
	assert.NotEqual(t, "impostor", schemas[0].Description)
}
