package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------- stdio ----

func TestAdapter_Stdio_DiscoversTools(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	assert.Equal(t, []string{"mock"}, adapter.ListServers())

	tools := adapter.Tools()
	require.Len(t, tools, 2)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "failing", tools[1].Name)
	// The server name is what CallTool routes on, so discovery must carry it.
	assert.Equal(t, "mock", tools[0].Server)
	assert.Equal(t, "Echo the message", tools[0].Description)

	assert.True(t, adapter.HasTool("echo"))
	assert.False(t, adapter.HasTool("nope"))
	assert.Equal(t, "mock", adapter.ServerForTool("echo"))
}

func TestAdapter_Stdio_CallToolSuccess(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	result, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "echo: hi", result.Content)
	assert.Equal(t, "mock", result.Metadata["mcp_server"])
}

// A tool that ran and failed must come back as a failure the model can read,
// not as a success carrying a stack trace in Content.
func TestAdapter_Stdio_CallToolReturnsError(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	result, err := adapter.CallTool(context.Background(), "failing", map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Equal(t, "boom: hi", result.Error)
	assert.Empty(t, result.Content)
}

// A JSON-RPC level error (unknown tool) is a transport failure, not a tool
// result: the call never happened.
func TestAdapter_Stdio_CallToolProtocolError(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	// "missing" is not in the server's tool list, so the server rejects it
	// with -32602. The adapter must not swallow that into a ToolResult.
	require.ErrorContains(t, callServerDirectly(t, adapter, "missing", nil), "unknown tool: missing")
}

// callServerDirectly reaches the client behind the adapter so a test can
// distinguish "the call failed" from "the tool reported failure" — the
// adapter deliberately flattens the latter into a ToolResult.
func callServerDirectly(t *testing.T, adapter *MCPAdapter, name string, args map[string]any) error {
	t.Helper()
	adapter.mu.RLock()
	conn := adapter.conns["mock"]
	adapter.mu.RUnlock()
	require.NotNil(t, conn)
	_, err := conn.client.CallTool(context.Background(), name, args)
	return err
}

func TestAdapter_Stdio_CallToolUnknownToAdapter(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	result, err := adapter.CallTool(context.Background(), "never-heard-of-it", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
	assert.Contains(t, result.Error, "is not available")
}

// Servers write non-JSON chatter to stdout. It must not desynchronise the
// framing, or discovery silently loses every tool.
func TestAdapter_Stdio_ToleratesNonJSONNoise(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer(mockModeNoise)))
	assert.Len(t, adapter.Tools(), 2)
}

func TestAdapter_Stdio_FollowsPagination(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer(mockModePaged)))

	tools := adapter.Tools()
	require.Len(t, tools, 2)
	assert.Equal(t, "echo", tools[0].Name)
	assert.Equal(t, "summarize", tools[1].Name)
}

func TestAdapter_Stdio_RejectsMissingCommand(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	err := adapter.Connect(context.Background(), MCPServer{
		Name: "ghost", Transport: TransportStdio, Command: "definitely-not-an-mcp-server",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "definitely-not-an-mcp-server")
	assert.Empty(t, adapter.ListServers(), "a failed connect must not register a server")
}

// A server that starts and immediately exits is the most common real failure
// (missing token, bad module). The error has to carry the child's stderr
// rather than a bare EOF.
func TestAdapter_Stdio_ReportsServerExitedWithStderr(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	err := adapter.Connect(context.Background(), mockStdioServer(mockModeExit))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MOCK_TOKEN is not set")
	assert.Empty(t, adapter.ListServers())
}

func TestAdapter_Stdio_HandshakeTimeout(t *testing.T) {
	adapter := newMockAdapter(t, Options{StartTimeout: 100 * time.Millisecond})
	err := adapter.Connect(context.Background(), mockStdioServer(mockModeSlow))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialize")
	assert.Empty(t, adapter.ListServers())
}

func TestAdapter_Stdio_CallTimeout(t *testing.T) {
	// The handshake is allowed to finish (the mock sleeps 1.5s on it); only
	// the call is bounded.
	adapter := newMockAdapter(t, Options{StartTimeout: 10 * time.Second, CallTimeout: 100 * time.Millisecond})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer(mockModeSlow)))

	_, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "hi"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded")
}

// The orphan guard. The mock forks a grandchild the engine never sees; if
// disconnecting only signalled the direct child, that grandchild would survive
// and keep whatever it holds.
func TestAdapter_Stdio_DisconnectSignalsWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")

	adapter := newMockAdapter(t, Options{})
	server := mockStdioServer("", envChildPIDFile, pidFile)
	require.NoError(t, adapter.Connect(context.Background(), server))

	pid := mustAtoi(t, waitForFile(t, pidFile, 5*time.Second))
	require.Greater(t, pid, 1)
	require.False(t, processGone(pid), "the grandchild should be running while the server is up")

	require.NoError(t, adapter.Disconnect("mock"))
	assert.Empty(t, adapter.ListServers())

	// Disconnect blocks until the child is reaped, so this is really an
	// assertion about the grandchild it left behind.
	assert.Eventually(t, func() bool { return processGone(pid) }, 5*time.Second, 20*time.Millisecond,
		"the grandchild survived: killing only the direct child leaves an orphan process")
}

func TestAdapter_Close_DisconnectsEverything(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	first := mockStdioServer("")
	second := mockStdioServer("")
	second.Name = "mock2"
	require.NoError(t, adapter.Connect(context.Background(), first))
	require.NoError(t, adapter.Connect(context.Background(), second))
	require.Len(t, adapter.ListServers(), 2)

	require.NoError(t, adapter.Close())
	assert.Empty(t, adapter.ListServers())
	assert.Empty(t, adapter.Tools())
	assert.False(t, adapter.HasTool("echo"))
}

func TestAdapter_ReconnectDoesNotLeakPreviousChild(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	// Same name, so a leak would show up as two live servers.
	assert.Equal(t, []string{"mock"}, adapter.ListServers())
	assert.Len(t, adapter.Tools(), 2)
}

// ----------------------------------------------------------------- http ----

// mockHTTP serves the same tool set as the stdio mock over Streamable HTTP.
type mockHTTP struct {
	mode string
	// sse answers every POST with an SSE-framed body, which is what current
	// SDK-based servers do.
	sse bool
	url string

	mu       sync.Mutex
	sessions []string
	versions []string
}

func (m *mockHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil || r.Method != http.MethodPost {
		http.Error(w, "expected POST", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ID     int64           `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	m.mu.Lock()
	m.sessions = append(m.sessions, r.Header.Get("Mcp-Session-Id"))
	m.versions = append(m.versions, r.Header.Get("MCP-Protocol-Version"))
	m.mu.Unlock()

	var result any
	switch req.Method {
	case "initialize":
		// A session id handed out at initialize must be echoed back on every
		// later request, or a stateful server treats the call as anonymous.
		w.Header().Set("Mcp-Session-Id", "session-1")
		result = mockInitializeResult()
	case "tools/list":
		result = mockToolsListResult(req.Params, m.mode)
	case "tools/call":
		body, rpcErr := mockToolsCallResult(req.Params)
		if rpcErr != nil {
			writeJSON(w, map[string]any{
				"jsonrpc": jsonrpcVersion, "id": req.ID,
				"error": map[string]any{"code": rpcErr.Code, "message": rpcErr.Message},
			}, m.sse)
			return
		}
		result = body
	default:
		writeJSON(w, map[string]any{
			"jsonrpc": jsonrpcVersion, "id": req.ID,
			"error": map[string]any{"code": -32601, "message": "method not found: " + req.Method},
		}, m.sse)
		return
	}
	writeJSON(w, map[string]any{"jsonrpc": jsonrpcVersion, "id": req.ID, "result": result}, m.sse)
}

func writeJSON(w http.ResponseWriter, payload any, sse bool) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if sse {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: "+string(raw)+"\n\n")
		return
	}
	_, _ = w.Write(raw)
}

func newMockHTTPServer(t *testing.T, mode string, sse bool) *mockHTTP {
	t.Helper()
	handler := &mockHTTP{mode: mode, sse: sse}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	handler.url = srv.URL
	return handler
}

func TestAdapter_HTTP_DiscoveryAndCall(t *testing.T) {
	handler := newMockHTTPServer(t, "", false)
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), MCPServer{
		Name: "remote", Transport: TransportHTTP, URL: handler.url,
	}))

	require.Len(t, adapter.Tools(), 2)
	result, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "echo: hi", result.Content)

	// initialize hands out a session; the call that follows must carry it.
	handler.mu.Lock()
	sessions := append([]string(nil), handler.sessions...)
	versions := append([]string(nil), handler.versions...)
	handler.mu.Unlock()
	require.NotEmpty(t, sessions)
	assert.Equal(t, "session-1", sessions[len(sessions)-1])
	assert.Equal(t, ProtocolVersion, versions[0])
}

func TestAdapter_HTTP_AcceptsSSEFramedResponse(t *testing.T) {
	handler := newMockHTTPServer(t, "", true)
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), MCPServer{
		Name: "remote", Transport: TransportHTTP, URL: handler.url,
	}))

	result, err := adapter.CallTool(context.Background(), "echo", map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.Equal(t, "echo: hi", result.Content)
}

func TestAdapter_HTTP_ConnectionFailure(t *testing.T) {
	// A port nothing listens on: the failure must be explicit, and must not
	// leave a half-registered server behind.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := srv.URL
	srv.Close()

	adapter := newMockAdapter(t, Options{StartTimeout: 5 * time.Second})
	err := adapter.Connect(context.Background(), MCPServer{
		Name: "remote", Transport: TransportHTTP, URL: deadURL,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "remote")
	assert.Empty(t, adapter.ListServers())
}

func TestAdapter_HTTP_ServerErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token expired", http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	adapter := newMockAdapter(t, Options{})
	err := adapter.Connect(context.Background(), MCPServer{
		Name: "remote", Transport: TransportHTTP, URL: srv.URL,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "token expired")
}

// ------------------------------------------------------------ validation ----

func TestAdapter_RejectsUnsupportedTransports(t *testing.T) {
	adapter := newMockAdapter(t, Options{})

	// sse is the one that looks plausible and must fail loudly: silently
	// treating it as http produces a connection that never works.
	err := adapter.Connect(context.Background(), MCPServer{Name: "old", Transport: "sse", URL: "http://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not supported")
	assert.Contains(t, err.Error(), "http")

	err = adapter.Connect(context.Background(), MCPServer{Name: "weird", Transport: "grpc"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown transport")
}

func TestAdapter_RejectsIncompleteServerConfig(t *testing.T) {
	adapter := newMockAdapter(t, Options{})

	err := adapter.Connect(context.Background(), MCPServer{Transport: TransportStdio, Command: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name is required")

	err = adapter.Connect(context.Background(), MCPServer{Name: "s", Transport: TransportStdio})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a command")

	err = adapter.Connect(context.Background(), MCPServer{Name: "s", Transport: TransportHTTP})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a url")

	err = adapter.Connect(context.Background(), MCPServer{Name: "s", Transport: TransportHTTP, URL: "ftp://x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http or https")
}

// ---------------------------------------------------------------- names ----

func TestAdapter_ToolNameCollisionIsReported(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	first := mockStdioServer("")
	second := mockStdioServer("")
	second.Name = "mock2"
	require.NoError(t, adapter.Connect(context.Background(), first))
	require.NoError(t, adapter.Connect(context.Background(), second))

	// Discovery order is configuration order, so the winner is deterministic:
	// whichever server was configured first.
	assert.Equal(t, "mock", adapter.ServerForTool("echo"))
	issues := adapter.Issues()
	require.Len(t, issues, 2, "both echo and failing collide")
	assert.Contains(t, issues[0], `"echo"`)
	assert.Contains(t, issues[0], "only \"mock\" is callable")
}

func TestAdapter_ToolSchemas(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	require.NoError(t, adapter.Connect(context.Background(), mockStdioServer("")))

	// By tool name.
	schemas := adapter.ToolSchemas([]string{"echo"})
	require.Len(t, schemas, 1)
	assert.Equal(t, "echo", schemas[0].Name)
	assert.Equal(t, "Echo the message", schemas[0].Description)
	assert.Equal(t, "object", schemas[0].Type)
	assert.Equal(t, "string", schemas[0].Properties["message"].Type)
	assert.Equal(t, "integer", schemas[0].Properties["count"].Type)
	assert.Equal(t, []string{"message"}, schemas[0].Required)

	// By server name: every tool that server provides.
	byServer := adapter.ToolSchemas([]string{"mock"})
	assert.Len(t, byServer, 2)

	// Unknown names are skipped rather than invented.
	assert.Empty(t, adapter.ToolSchemas([]string{"nope"}))
	assert.Empty(t, adapter.ToolSchemas(nil))
}

func TestAdapter_ConnectConfigsCollectsEveryFailure(t *testing.T) {
	adapter := newMockAdapter(t, Options{})
	good := mockStdioServer("")
	err := adapter.ConnectConfigs(context.Background(), []types.MCPServerConfig{
		{Name: "broken", Transport: "sse", URL: "http://x"},
		{Name: good.Name, Transport: good.Transport, Command: good.Command, Env: good.Env},
		{Name: "also-broken", Transport: TransportHTTP},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broken")
	assert.Contains(t, err.Error(), "also-broken")
	// One broken server does not stop the others.
	assert.Equal(t, []string{"mock"}, adapter.ListServers())
}

func TestAdapter_NilAdapterIsInert(t *testing.T) {
	var adapter *MCPAdapter
	assert.Empty(t, adapter.ListServers())
	assert.False(t, adapter.HasTool("echo"))
	assert.Empty(t, adapter.ToolSchemas([]string{"echo"}))
	result, err := adapter.CallTool(context.Background(), "echo", nil)
	require.NoError(t, err)
	assert.False(t, result.Success)
}
