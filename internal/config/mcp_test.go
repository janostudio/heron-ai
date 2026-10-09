package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeSettings writes .agents/settings.json under root.
func writeSettings(t *testing.T, root, body string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"), []byte(body), 0o644))
}

func TestLoadMCPServers(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{
  "logging": {"level": "info"},
  "mcp": [
    {"name": "github", "transport": "stdio",
     "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"],
     "env": {"GITHUB_TOKEN": "abc"}},
    {"name": "issues", "transport": "http",
     "url": "https://mcp.example.internal/issues",
     "headers": {"Authorization": "Bearer t"}}
  ]
}`)

	servers, err := NewConfigLoader(root).LoadMCPServers()
	require.NoError(t, err)
	require.Len(t, servers, 2)

	assert.Equal(t, "github", servers[0].Name)
	assert.Equal(t, "stdio", servers[0].Transport)
	assert.Equal(t, "npx", servers[0].Command)
	assert.Equal(t, []string{"-y", "@modelcontextprotocol/server-github"}, servers[0].Args)
	assert.Equal(t, "abc", servers[0].Env["GITHUB_TOKEN"])

	assert.Equal(t, "issues", servers[1].Name)
	assert.Equal(t, "http", servers[1].Transport)
	assert.Equal(t, "https://mcp.example.internal/issues", servers[1].URL)
	assert.Equal(t, "Bearer t", servers[1].Headers["Authorization"])
}

// No .agents/settings.json is a legal workspace, and a settings.json with no
// mcp section is the ordinary case: both mean "no MCP servers", not an error.
func TestLoadMCPServers_Absent(t *testing.T) {
	servers, err := NewConfigLoader(t.TempDir()).LoadMCPServers()
	require.NoError(t, err)
	assert.Empty(t, servers)

	root := t.TempDir()
	writeSettings(t, root, `{"logging": {"level": "info"}}`)
	servers, err = NewConfigLoader(root).LoadMCPServers()
	require.NoError(t, err)
	assert.Empty(t, servers)
}

// A malformed file is not the same as an absent one, and must not be read as
// "no MCP configured" — the user wrote something and it did not parse.
func TestLoadMCPServers_MalformedFile(t *testing.T) {
	root := t.TempDir()
	writeSettings(t, root, `{"mcp": [ {"name":`)

	_, err := NewConfigLoader(root).LoadMCPServers()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "settings.json")
}
