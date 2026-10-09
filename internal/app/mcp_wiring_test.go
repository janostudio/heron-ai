package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// mcpFixtureServer is the smallest thing that speaks enough MCP for the
// startup path: initialize, one tool, one call. It is HTTP rather than stdio
// because a test that spawns a real child process belongs in internal/mcp,
// where the transport is the subject; here the subject is the wiring from
// settings.json to a live adapter, and any transport that works proves it.
type mcpFixtureServer struct {
	url string
	srv *httptest.Server
}

func newMCPFixtureServer(t *testing.T) *mcpFixtureServer {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		var req struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var result any
		switch req.Method {
		case "initialize":
			result = map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo":      map[string]any{"name": "fixture", "version": "1"},
			}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name":        "fixture_echo",
				"description": "Echo a message",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"message": map[string]any{"type": "string"}},
					"required":   []any{"message"},
				},
			}}}
		case "tools/call":
			var params struct {
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(req.Params, &params)
			message, _ := params.Arguments["message"].(string)
			result = map[string]any{
				"content": []any{map[string]any{"type": "text", "text": "echo: " + message}},
				"isError": false,
			}
		default:
			result = map[string]any{}
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(srv.Close)
	return &mcpFixtureServer{url: srv.URL, srv: srv}
}

// TestBuildRuntimeWiresMCPFromSettings covers the whole startup chain, which is
// the thing that was missing: the config existed, the agent could declare MCP
// tools, and nothing connected the two. Each half on its own has no symptom —
// the adapter connects fine, the schema loop skips an unknown name silently —
// so only an end-to-end assertion can tell the difference.
//
// It goes through BuildRuntime rather than calling the adapter directly
// because the failure mode being guarded against is a missing wire between
// them, and a test that builds both halves by hand cannot see a missing wire.
func TestBuildRuntimeWiresMCPFromSettings(t *testing.T) {
	server := newMCPFixtureServer(t)
	root := t.TempDir()
	configRoot, flowPath, definitions := fixtureConfigRoot(t, root)
	// BuildRuntime reads MCP servers with a ConfigLoader rooted at
	// workspaceRoot, so the config root and the workspace root have to be the
	// same directory here — two temp dirs (as the knowledge tests use) would
	// leave the loader looking at a settings.json with no mcp section and this
	// test would pass vacuously.
	workspaceRoot := root

	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "settings.json"), []byte(fmt.Sprintf(`{
  "mcp": [
    {"name": "fixture", "transport": "http", "url": %q}
  ]
}`, server.url)), 0o644))

	store := types.NewDefinitionStore(definitions, configRoot, flowPath)
	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error")
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	// 1. The server was connected from settings.json.
	require.Contains(t, bundle.MCP.ListServers(), "fixture")
	// 2. Its tools were discovered.
	require.True(t, bundle.MCP.HasTool("fixture_echo"))
	// 3. The executor dispatches to it rather than the builtin registry.
	result, err := bundle.ToolExecutor.Execute(context.Background(), "fixture_echo",
		map[string]any{"message": "hi"})
	require.NoError(t, err)
	assert.True(t, result.Success)
	assert.Equal(t, "echo: hi", result.Content)
	// 4. The adapter produces a schema the model can be shown.
	schemas := bundle.MCP.ToolSchemas([]string{"fixture_echo"})
	require.Len(t, schemas, 1)
	assert.Equal(t, "fixture_echo", schemas[0].Name)
	assert.Equal(t, "string", schemas[0].Properties["message"].Type)
}

// A server that cannot be reached must not stop the engine from starting: the
// failure is logged and the agent simply has no MCP tools. Aborting startup
// over one bad server would make an unrelated conversation unusable.
func TestBuildRuntimeSurvivesUnreachableMCPServer(t *testing.T) {
	dead := newMCPFixtureServer(t)
	deadURL := dead.url
	dead.srv.Close()

	root := t.TempDir()
	configRoot, flowPath, definitions := fixtureConfigRoot(t, root)
	require.NoError(t, os.WriteFile(filepath.Join(configRoot, "settings.json"), []byte(fmt.Sprintf(`{
  "mcp": [
    {"name": "dead", "transport": "http", "url": %q},
    {"name": "broken", "transport": "sse", "url": "http://example.invalid"}
  ]
}`, deadURL)), 0o644))

	store := types.NewDefinitionStore(definitions, configRoot, flowPath)
	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, root, "error")
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	assert.Empty(t, bundle.MCP.ListServers())
	assert.False(t, bundle.MCP.HasTool("anything"))
}

// No mcp section is the ordinary case and must stay free of connections.
func TestBuildRuntimeWithNoMCPConfig(t *testing.T) {
	root := t.TempDir()
	_, flowPath, definitions := fixtureConfigRoot(t, root)
	store := types.NewDefinitionStore(definitions, filepath.Join(root, ".agents"), flowPath)

	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, root, "error")
	require.NoError(t, err)
	defer func() { _ = bundle.Close() }()

	assert.Empty(t, bundle.MCP.ListServers())
}
