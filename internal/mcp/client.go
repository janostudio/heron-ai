package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// clientName identifies this engine in the MCP handshake. Servers use it for
// logging and for capability negotiation, so it is a fixed product name rather
// than something the config can rename.
const clientName = "heron-engine"

// maxToolPages bounds tools/list pagination. A server that keeps returning a
// cursor forever would otherwise hang the handshake.
const maxToolPages = 100

// ToolDefinition is one tool as an MCP server describes it.
type ToolDefinition struct {
	// Server is the name of the MCP server that provides the tool. It is what
	// CallTool routes on and what a tool result reports back.
	Server      string
	Name        string
	Description string
	// InputSchema is the raw JSON Schema from the server. It is kept verbatim
	// because the engine's own schema type is a lossy projection; the server's
	// copy is what the arguments are eventually validated against.
	InputSchema map[string]any
}

// Client is one connection to one MCP server.
//
// A Client is safe for concurrent use: concurrent tools/call requests are
// identified by their JSON-RPC id and demultiplexed by the transport.
type Client struct {
	name      string
	transport Transport

	nextID  atomic.Int64
	mu      sync.RWMutex
	tools   []ToolDefinition
	server  ServerInfo
	version string
}

// ServerInfo is what a server reports about itself in the handshake. It is
// diagnostic only — nothing in the engine branches on it.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// NewClient builds a client for a server. The transport is started by Connect,
// not here, so a caller that fails to connect never owns a running child.
func NewClient(name string, transport Transport) *Client {
	return &Client{name: name, transport: transport}
}

// Connect starts the transport and completes the handshake: initialize,
// notifications/initialized, then tool discovery.
//
// A partially connected client is never returned usable: if any step fails the
// transport is closed before Connect returns, which is what keeps a failed
// stdio server from leaving a child process behind.
func (c *Client) Connect(ctx context.Context) error {
	if c == nil || c.transport == nil {
		return errors.New("mcp client: no transport")
	}
	if err := c.transport.Start(ctx); err != nil {
		return err
	}
	if err := c.handshake(ctx); err != nil {
		_ = c.transport.Close()
		return err
	}
	return nil
}

func (c *Client) handshake(ctx context.Context) error {
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": clientName},
	}
	res, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp server %q: initialize: %w", c.name, err)
	}
	var init struct {
		ProtocolVersion string     `json:"protocolVersion"`
		ServerInfo      ServerInfo `json:"serverInfo"`
	}
	if err := json.Unmarshal(res.Result, &init); err != nil {
		return fmt.Errorf("mcp server %q: decode initialize result: %w", c.name, err)
	}
	c.mu.Lock()
	c.server = init.ServerInfo
	c.version = init.ProtocolVersion
	c.mu.Unlock()

	// notifications/initialized completes the handshake. Best-effort: a server
	// that ignores it still has to answer tools/list, and failing the whole
	// connection over an acknowledgement would be a poor trade.
	_ = c.transport.Notify(ctx, Notification{JSONRPC: jsonrpcVersion, Method: "notifications/initialized"})

	tools, err := c.listTools(ctx)
	if err != nil {
		return fmt.Errorf("mcp server %q: tools/list: %w", c.name, err)
	}
	c.mu.Lock()
	c.tools = tools
	c.mu.Unlock()
	return nil
}

// listTools walks tools/list to exhaustion, following cursors.
func (c *Client) listTools(ctx context.Context) ([]ToolDefinition, error) {
	tools := make([]ToolDefinition, 0, 16)
	cursor := ""
	for page := 0; page < maxToolPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var pageResult struct {
			Tools      []mcpTool `json:"tools"`
			NextCursor string    `json:"nextCursor"`
		}
		if err := json.Unmarshal(res.Result, &pageResult); err != nil {
			return nil, fmt.Errorf("decode tools/list result: %w", err)
		}
		for _, tool := range pageResult.Tools {
			if tool.Name == "" {
				continue
			}
			tools = append(tools, ToolDefinition{
				Server:      c.name,
				Name:        tool.Name,
				Description: tool.Description,
				InputSchema: tool.InputSchema,
			})
		}
		if pageResult.NextCursor == "" || len(pageResult.Tools) == 0 {
			return tools, nil
		}
		cursor = pageResult.NextCursor
	}
	return tools, errors.New("tools/list did not finish within the page limit")
}

type mcpTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Tools returns the tools discovered during Connect, in discovery order.
func (c *Client) Tools() []ToolDefinition {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]ToolDefinition(nil), c.tools...)
}

// ServerInfo returns what the server said about itself.
func (c *Client) ServerInfo() ServerInfo {
	if c == nil {
		return ServerInfo{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.server
}

// ProtocolVersion returns the revision the server negotiated.
func (c *Client) ProtocolVersion() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.version
}

// CallTool invokes tools/call and converts the MCP result into the engine's
// ToolResult.
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if c == nil || c.transport == nil {
		return nil, errors.New("mcp client: not connected")
	}
	if args == nil {
		args = map[string]any{}
	}
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return nil, err
	}
	var out callToolResult
	if err := json.Unmarshal(res.Result, &out); err != nil {
		return nil, fmt.Errorf("mcp server %q: decode tools/call result: %w", c.name, err)
	}
	return out.toToolResult(c.name), nil
}

// Close shuts the transport down. For a stdio server this reaps the child.
func (c *Client) Close() error {
	if c == nil || c.transport == nil {
		return nil
	}
	return c.transport.Close()
}

func (c *Client) call(ctx context.Context, method string, params any) (*Response, error) {
	raw, err := encodeParams(params)
	if err != nil {
		return nil, err
	}
	id := c.nextID.Add(1)
	return c.transport.Send(ctx, Request{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Method:  method,
		Params:  raw,
	})
}

// callToolResult is the result member of a tools/call response.
type callToolResult struct {
	Content           []contentBlock `json:"content"`
	IsError           bool           `json:"isError"`
	StructuredContent any            `json:"structuredContent,omitempty"`
}

type contentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Resource *struct {
		URI      string `json:"uri"`
		MimeType string `json:"mimeType"`
		Text     string `json:"text"`
	} `json:"resource,omitempty"`
}

// toToolResult flattens an MCP result into the engine's shape.
//
// The content list is joined rather than kept structured: the engine hands a
// tool result to a model as text, and a model reading five text blocks reads
// exactly the same sentence whether they arrived as one string or five. A
// result flagged isError is moved into Error and its text cleared, because a
// failed call whose "content" also looks successful is how an agent ends up
// treating a stack trace as evidence.
func (r callToolResult) toToolResult(server string) *types.ToolResult {
	parts := make([]string, 0, len(r.Content))
	for _, block := range r.Content {
		switch {
		case block.Text != "":
			parts = append(parts, block.Text)
		case block.Resource != nil && block.Resource.Text != "":
			parts = append(parts, block.Resource.Text)
		}
	}
	content := strings.Join(parts, "\n")
	if content == "" && r.StructuredContent != nil {
		if encoded, err := json.Marshal(r.StructuredContent); err == nil {
			content = string(encoded)
		}
	}
	result := &types.ToolResult{
		Success:  !r.IsError,
		Content:  content,
		Metadata: map[string]any{"mcp_server": server},
	}
	if r.IsError {
		result.Error = content
		if result.Error == "" {
			result.Error = "MCP tool reported an error without a message"
		}
		result.Content = ""
	}
	return result
}
