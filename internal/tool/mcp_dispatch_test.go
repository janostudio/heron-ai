package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// stubMCP stands in for the real MCP adapter. The executor only ever talks to
// the two-method interface it declares, so a stub is the whole contract.
type stubMCP struct {
	tools    map[string]struct{}
	result   *types.ToolResult
	err      error
	lastArgs map[string]any
	calls    int
}

func (s *stubMCP) HasTool(name string) bool {
	_, ok := s.tools[name]
	return ok
}

func (s *stubMCP) CallTool(_ context.Context, _ string, args map[string]any) (*types.ToolResult, error) {
	s.calls++
	s.lastArgs = args
	return s.result, s.err
}

func newStubMCP(tools ...string) *stubMCP {
	set := make(map[string]struct{}, len(tools))
	for _, name := range tools {
		set[name] = struct{}{}
	}
	return &stubMCP{tools: set, result: &types.ToolResult{Success: true, Content: "from mcp"}}
}

func registryWithRead(t *testing.T) *ToolRegistry {
	t.Helper()
	registry := NewToolRegistry()
	registry.Register(&mockTool{
		name: "Read",
		params: map[string]any{
			"file": map[string]any{"type": "string", "required": true},
		},
		executeFn: func(context.Context, map[string]any) (*types.ToolResult, error) {
			return &types.ToolResult{Success: true, Content: "from registry"}, nil
		},
	})
	return registry
}

func TestToolExecutor_MCPToolIsDispatchedToTheAdapter(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	mcpStub := newStubMCP("github_search")
	executor.SetMCPDispatcher(mcpStub)

	result, err := executor.Execute(context.Background(), "github_search", map[string]any{"q": "heron"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "from mcp", result.Content)
	assert.Equal(t, 1, mcpStub.calls)
	// The registry never saw it, which is the point: MCP tools are not
	// registered there.
	assert.Equal(t, map[string]any{"q": "heron"}, mcpStub.lastArgs)
}

// An MCP schema is a lossy projection of the server's JSON Schema, so the
// executor must not validate against it. A nested object collapses to "object"
// and a union collapses to one member; either would reject a correct call.
func TestToolExecutor_MCPToolArgumentsAreNotValidated(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	executor.SetMCPDispatcher(newStubMCP("deep"))

	_, err := executor.Execute(context.Background(), "deep", map[string]any{
		"nested": map[string]any{"a": 1},
		"list":   []any{1, 2},
	})
	require.NoError(t, err)
}

func TestToolExecutor_UnknownToolStillFails(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	executor.SetMCPDispatcher(newStubMCP("github_search"))

	result, err := executor.Execute(context.Background(), "not_a_tool", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not found")
}

func TestToolExecutor_BuiltinStillGoesToRegistry(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	mcpStub := newStubMCP("github_search")
	executor.SetMCPDispatcher(mcpStub)

	result, err := executor.Execute(context.Background(), "Read", map[string]any{"file": "a.txt"})
	require.NoError(t, err)
	assert.Equal(t, "from registry", result.Content)
	assert.Equal(t, 0, mcpStub.calls)
}

func TestToolExecutor_MCPFailureBecomesFailedResult(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	mcpStub := newStubMCP("github_search")
	mcpStub.err = errors.New("MCP tool \"github_search\" on server \"github\": server exited")
	executor.SetMCPDispatcher(mcpStub)

	result, err := executor.Execute(context.Background(), "github_search", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "server exited")
}

// The parallel scheduler must not batch an MCP tool with read-only builtins:
// nothing here knows whether the remote call is a read or a deploy.
func TestToolExecutor_MCPToolRunsSerially(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	executor.SetMCPDispatcher(newStubMCP("github_search"))

	assert.Equal(t, types.ToolSerial, executor.ExecutionSpec("github_search").Class)
}

func TestToolExecutor_MCPToolNeedsNoApproval(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))
	executor.SetMCPDispatcher(newStubMCP("github_search"))

	needs, err := executor.NeedsApproval("github_search", nil)
	require.NoError(t, err)
	assert.False(t, needs)
}

func TestToolExecutor_WithoutMCPDispatcherIsUnchanged(t *testing.T) {
	executor := NewToolExecutor(registryWithRead(t))

	result, err := executor.Execute(context.Background(), "Read", map[string]any{"file": "a.txt"})
	require.NoError(t, err)
	assert.Equal(t, "from registry", result.Content)
	assert.Nil(t, executor.MCPExecutor())
}
