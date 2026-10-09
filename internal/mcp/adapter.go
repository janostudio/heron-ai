package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const (
	// DefaultStartTimeout bounds one server's handshake: spawn the child,
	// initialize, discover tools.
	DefaultStartTimeout = 30 * time.Second
	// DefaultCallTimeout bounds one tools/call.
	DefaultCallTimeout = 120 * time.Second
)

// Transports this implementation supports.
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// MCPServer is one MCP server as configured.
type MCPServer struct {
	Name      string
	Transport string // stdio | http
	Command   string
	Args      []string
	URL       string
	Headers   map[string]string
	Env       map[string]string
}

// ServerFromConfig converts the persisted config shape. It exists so the JSON
// tags and defaults of the config live in one place and the adapter can assume
// a validated value.
func ServerFromConfig(cfg types.MCPServerConfig) MCPServer {
	return MCPServer{
		Name:      cfg.Name,
		Transport: cfg.Transport,
		Command:   cfg.Command,
		Args:      cfg.Args,
		URL:       cfg.URL,
		Headers:   cfg.Headers,
		Env:       cfg.Env,
	}
}

// Options tunes connection handling. Zero values fall back to the defaults
// above, so `Options{}` is a valid configuration.
type Options struct {
	// StartTimeout bounds the handshake for one server.
	StartTimeout time.Duration
	// CallTimeout bounds one tools/call. It applies only when the caller's
	// context carries no deadline of its own, so a caller that already has one
	// keeps it.
	CallTimeout time.Duration
	// HTTPClient is used by the http transport. Nil uses http.DefaultClient.
	HTTPClient *http.Client
}

func (o Options) startTimeout() time.Duration {
	if o.StartTimeout > 0 {
		return o.StartTimeout
	}
	return DefaultStartTimeout
}

func (o Options) callTimeout() time.Duration {
	if o.CallTimeout > 0 {
		return o.CallTimeout
	}
	return DefaultCallTimeout
}

// toolRef remembers which server serves a tool.
type toolRef struct {
	server string
	def    ToolDefinition
}

type connection struct {
	cfg    MCPServer
	client *Client
}

// MCPAdapter owns the set of live MCP connections and the tool names they
// provide.
//
// It is the Extension half of the MCP design (04E): the engine's Core owns
// tool schema, permissions and turn association, and everything here is
// connection lifecycle, discovery and result conversion. The adapter is safe
// for concurrent use — a Team may run several agents that call the same MCP
// tool at once.
type MCPAdapter struct {
	opts Options

	mu     sync.RWMutex
	conns  map[string]*connection
	order  []string
	index  map[string]toolRef
	issues []string
}

// NewMCPAdapter builds an empty adapter. Nothing is connected until Connect or
// ConnectConfigs is called.
func NewMCPAdapter(opts Options) *MCPAdapter {
	return &MCPAdapter{
		opts:  opts,
		conns: make(map[string]*connection),
		index: make(map[string]toolRef),
	}
}

// Connect brings one server up: start the transport, complete the handshake
// and discover its tools.
//
// On any failure nothing is registered and the transport is closed, so a
// server that fails to start costs nothing but the error returned.
func (a *MCPAdapter) Connect(ctx context.Context, server MCPServer) error {
	if a == nil {
		return errors.New("mcp adapter: not initialised")
	}
	if err := validateServer(server); err != nil {
		return err
	}
	transport, err := a.newTransport(server)
	if err != nil {
		return err
	}
	client := NewClient(server.Name, transport)
	startCtx, cancel := withTimeout(ctx, a.opts.startTimeout())
	defer cancel()
	if err := client.Connect(startCtx); err != nil {
		return err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	if previous, exists := a.conns[server.Name]; exists && previous != nil {
		// Reconnecting a name that is already live must not leak the previous
		// child: it is closed before the new one is installed.
		_ = previous.client.Close()
	} else {
		a.order = append(a.order, server.Name)
	}
	a.conns[server.Name] = &connection{cfg: server, client: client}
	a.reindex()
	return nil
}

// ConnectConfigs connects every configured server.
//
// One broken server does not stop the others: each failure is collected and
// returned together, with the server name attached, so the caller can report
// all of them instead of discovering them one restart at a time.
func (a *MCPAdapter) ConnectConfigs(ctx context.Context, configs []types.MCPServerConfig) error {
	if a == nil {
		return errors.New("mcp adapter: not initialised")
	}
	var errs []error
	for _, cfg := range configs {
		if err := a.Connect(ctx, ServerFromConfig(cfg)); err != nil {
			errs = append(errs, fmt.Errorf("mcp server %q: %w", cfg.Name, err))
		}
	}
	return errors.Join(errs...)
}

// validateServer rejects a config that cannot work before anything is spawned.
func validateServer(server MCPServer) error {
	if strings.TrimSpace(server.Name) == "" {
		return errors.New("mcp server: name is required")
	}
	switch strings.ToLower(strings.TrimSpace(server.Transport)) {
	case "", TransportStdio:
		if strings.TrimSpace(server.Command) == "" {
			return fmt.Errorf("mcp server %q: transport %q requires a command", server.Name, TransportStdio)
		}
	case TransportHTTP:
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("mcp server %q: transport %q requires a url", server.Name, TransportHTTP)
		}
	case "sse":
		// Named explicitly because it looks plausible: the older MCP
		// transports list reads "stdio | sse | http". The SSE transport — a
		// long-lived GET event stream plus a separate POST endpoint — is not
		// implemented, and silently treating it as http would produce a
		// connection that never works.
		return fmt.Errorf("mcp server %q: transport %q is not supported; use %q (streamable HTTP)",
			server.Name, "sse", TransportHTTP)
	default:
		return fmt.Errorf("mcp server %q: unknown transport %q; supported: %s, %s",
			server.Name, server.Transport, TransportStdio, TransportHTTP)
	}
	return nil
}

func (a *MCPAdapter) newTransport(server MCPServer) (Transport, error) {
	switch strings.ToLower(strings.TrimSpace(server.Transport)) {
	case TransportHTTP:
		return NewHTTPTransport(server.URL, server.Headers, a.opts.HTTPClient), nil
	default:
		return NewStdioTransport(server.Command, server.Args, server.Env), nil
	}
}

// Disconnect closes one server and forgets its tools.
func (a *MCPAdapter) Disconnect(name string) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	conn, ok := a.conns[name]
	delete(a.conns, name)
	if ok {
		for i, existing := range a.order {
			if existing == name {
				a.order = append(a.order[:i], a.order[i+1:]...)
				break
			}
		}
	}
	a.reindex()
	a.mu.Unlock()
	if !ok || conn == nil {
		return nil
	}
	return conn.client.Close()
}

// Close disconnects every server. It is the shutdown path: without it a stdio
// server outlives the process that started it.
func (a *MCPAdapter) Close() error {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	names := append([]string(nil), a.order...)
	a.mu.RUnlock()
	var errs []error
	for _, name := range names {
		if err := a.Disconnect(name); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ListServers returns the connected server names in connection order.
func (a *MCPAdapter) ListServers() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.order...)
}

// Tools returns every discovered tool across all servers.
func (a *MCPAdapter) Tools() []ToolDefinition {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	tools := make([]ToolDefinition, 0, len(a.index))
	for _, name := range a.order {
		conn := a.conns[name]
		if conn == nil {
			continue
		}
		tools = append(tools, conn.client.Tools()...)
	}
	return tools
}

// HasTool reports whether a tool name is currently served.
func (a *MCPAdapter) HasTool(name string) bool {
	if a == nil {
		return false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.index[name]
	return ok
}

// ServerForTool returns the server providing a tool, or "" if none does.
func (a *MCPAdapter) ServerForTool(name string) string {
	if a == nil {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.index[name].server
}

// ToolSchemas returns schemas for the named tools.
//
// A name may be either a tool name or a server name; a server name expands to
// every tool that server provides. Both forms are accepted because a
// configuration author writing `tools.mcp: ["github"]` is naming the server
// they just configured, and rejecting that in favour of listing each tool
// would be a needless trap. Unknown names are skipped rather than invented.
func (a *MCPAdapter) ToolSchemas(names []string) []types.JSONSchema {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	schemas := make([]types.JSONSchema, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == "" {
			continue
		}
		if _, ok := a.conns[name]; ok {
			conn := a.conns[name]
			if conn == nil {
				continue
			}
			for _, def := range conn.client.Tools() {
				if _, dup := seen[def.Name]; dup {
					continue
				}
				seen[def.Name] = struct{}{}
				schemas = append(schemas, def.Schema())
			}
			continue
		}
		ref, ok := a.index[name]
		if !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		schemas = append(schemas, ref.def.Schema())
	}
	return schemas
}

// CallTool routes a call to the server that provides it.
//
// A tool nobody provides is a failed ToolResult, not an error: the model is
// the one that asked, and it needs to be told, in the same channel every other
// tool failure arrives on, that the tool is not available. A tool that exists
// but fails on the wire returns an error, which the executor turns into the
// same shape.
func (a *MCPAdapter) CallTool(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	if a == nil {
		return &types.ToolResult{Success: false, Error: "MCP is not configured"}, nil
	}
	a.mu.RLock()
	ref, ok := a.index[name]
	conn := a.conns[ref.server]
	a.mu.RUnlock()
	if !ok || conn == nil {
		return &types.ToolResult{
			Success: false,
			Error: fmt.Sprintf("MCP tool %q is not available; check the mcp section of .agents/settings.json "+
				"and that the agent lists it under tools.mcp", name),
		}, nil
	}
	callCtx, cancel := withTimeout(ctx, a.opts.callTimeout())
	defer cancel()
	result, err := conn.client.CallTool(callCtx, name, args)
	if err != nil {
		return nil, fmt.Errorf("MCP tool %q on server %q: %w", name, ref.server, err)
	}
	return result, nil
}

// Issues returns the non-fatal problems found while connecting, currently only
// tool-name collisions between servers. Collisions are reported rather than
// resolved silently: two servers claiming one tool name means whichever the
// model calls, one of them will not answer, and the operator needs to know.
func (a *MCPAdapter) Issues() []string {
	if a == nil {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]string(nil), a.issues...)
}

// reindex rebuilds the tool-name -> server map. It must be called with the
// write lock held.
//
// Iteration follows connection order, which is configuration order, so the
// winner of a collision is deterministic rather than whichever server
// happened to answer first.
func (a *MCPAdapter) reindex() {
	index := make(map[string]toolRef, len(a.index))
	issues := make([]string, 0)
	for _, name := range a.order {
		conn := a.conns[name]
		if conn == nil {
			continue
		}
		for _, def := range conn.client.Tools() {
			if existing, exists := index[def.Name]; exists {
				issues = append(issues, fmt.Sprintf(
					"MCP tool %q is provided by %q and %q; only %q is callable",
					def.Name, existing.server, name, existing.server))
				continue
			}
			index[def.Name] = toolRef{server: name, def: def}
		}
	}
	a.index = index
	a.issues = issues
}

// withTimeout applies a default deadline only when the caller has none. A
// caller that already bounded the work keeps its own bound.
func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
