package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPAdapter_ConnectAndList(t *testing.T) {
	adapter := NewMCPAdapter()

	err := adapter.Connect(context.Background(), MCPServer{
		Name:      "test-server",
		Transport: "stdio",
		Command:   "test-cmd",
	})
	require.NoError(t, err)

	servers := adapter.ListServers()
	assert.Len(t, servers, 1)
	assert.Equal(t, "test-server", servers[0])
}

func TestMCPAdapter_Disconnect(t *testing.T) {
	adapter := NewMCPAdapter()

	adapter.Connect(context.Background(), MCPServer{Name: "s1"})
	adapter.Connect(context.Background(), MCPServer{Name: "s2"})

	err := adapter.Disconnect("s1")
	require.NoError(t, err)

	servers := adapter.ListServers()
	assert.Len(t, servers, 1)
	assert.Equal(t, "s2", servers[0])
}

func TestMCPAdapter_Disconnect_Nonexistent(t *testing.T) {
	adapter := NewMCPAdapter()
	err := adapter.Disconnect("nonexistent")
	require.NoError(t, err) // deleting non-existent key is not an error in Go maps
}

func TestMCPAdapter_CallTool_NotImplemented(t *testing.T) {
	adapter := NewMCPAdapter()
	result, err := adapter.CallTool(context.Background(), "some-tool", map[string]any{
		"param": "value",
	})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "not implemented")
}

func TestMCPServer_Fields(t *testing.T) {
	s := MCPServer{
		Name:      "my-server",
		Transport: "http",
		URL:       "http://localhost:8080",
		Headers:   map[string]string{"Authorization": "Bearer token"},
		Env:       map[string]string{"KEY": "value"},
	}

	assert.Equal(t, "my-server", s.Name)
	assert.Equal(t, "http", s.Transport)
	assert.Equal(t, "http://localhost:8080", s.URL)
	assert.Equal(t, "Bearer token", s.Headers["Authorization"])
	assert.Equal(t, "value", s.Env["KEY"])
}
