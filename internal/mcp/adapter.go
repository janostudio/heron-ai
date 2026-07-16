package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type MCPServer struct {
	Name      string
	Transport string // stdio | http | sse
	Command   string
	Args      []string
	URL       string
	Headers   map[string]string
	Env       map[string]string
}

type MCPAdapter struct {
	mu      sync.RWMutex
	servers map[string]MCPServer
	tools   []types.Tool
}

func NewMCPAdapter() *MCPAdapter {
	return &MCPAdapter{
		servers: make(map[string]MCPServer),
	}
}

func (a *MCPAdapter) Connect(ctx context.Context, server MCPServer) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.servers[server.Name] = server
	return nil
}

func (a *MCPAdapter) Disconnect(name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.servers, name)
	return nil
}

func (a *MCPAdapter) ListServers() []string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	names := make([]string, 0, len(a.servers))
	for name := range a.servers {
		names = append(names, name)
	}
	return names
}

func (a *MCPAdapter) CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{
		Success: false,
		Error:   fmt.Sprintf("MCP tool %q not implemented (transport: placeholder)", name),
	}, nil
}
