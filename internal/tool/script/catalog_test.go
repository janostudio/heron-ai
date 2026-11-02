package script

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type stubTool struct {
	name   string
	desc   string
	params map[string]any
}

func (s stubTool) Name() string               { return s.name }
func (s stubTool) Description() string        { return s.desc }
func (s stubTool) Parameters() map[string]any { return s.params }
func (s stubTool) NeedsApproval() bool        { return false }
func (s stubTool) Execute(_ context.Context, _ map[string]any) (*types.ToolResult, error) {
	return &types.ToolResult{Success: true}, nil
}

func writeTool() stubTool {
	return stubTool{
		name: "Write",
		desc: "Write file contents",
		params: map[string]any{
			"file":          map[string]any{"type": "string", "required": true, "description": "path"},
			"content":       map[string]any{"type": "string", "description": "full content for replace mode"},
			"mode":          map[string]any{"type": "string", "enum": []string{"create", "replace", "edit"}},
			"old_text":      map[string]any{"type": "string", "description": "exact text to replace in edit mode"},
			"base_revision": map[string]any{"type": "string", "description": "revision returned by Read"},
		},
	}
}

func readTool() stubTool {
	return stubTool{
		name: "Read",
		desc: "Read file contents",
		params: map[string]any{
			"file":       map[string]any{"type": "string", "required": true},
			"line_start": map[string]any{"type": "integer"},
		},
	}
}

// A mode enum must expand into one line per mode: a model that only sees
// "Write(file, content, mode)" writes whole-file overwrites even when edit mode
// exists and is safer.
func TestCatalogExpandsModes(t *testing.T) {
	entries := CatalogFromTools([]types.Tool{writeTool()})
	text := RenderCatalog(entries, DefaultCatalogBudget)

	lines := strings.Split(text, "\n")
	modes := map[string]bool{}
	for _, line := range lines {
		for _, mode := range []string{`mode:"create"`, `mode:"replace"`, `mode:"edit"`} {
			if strings.Contains(line, mode) {
				modes[mode] = true
			}
		}
	}
	assert.Len(t, modes, 3, "every mode must get its own line:\n%s", text)
	assert.Contains(t, text, "base_revision from a prior Read", "edit mode must state its preconditions")
}

func TestCatalogStatesReturnShape(t *testing.T) {
	entries := CatalogFromTools([]types.Tool{readTool()})
	text := RenderCatalog(entries, DefaultCatalogBudget)
	assert.Contains(t, text, "tools.Read({file, line_start?})")
	assert.Contains(t, text, "→ {ok, content, metadata{file, revision, truncated, line_start, line_end, total_lines, total_bytes}}")
}

// When the catalog does not fit, whole tools are dropped and counted; a
// half-rendered signature would be worse than an absent one.
func TestCatalogDropsWholeToolsWhenOverBudget(t *testing.T) {
	tools := []types.Tool{readTool(), writeTool()}
	entries := CatalogFromTools(tools)
	text := RenderCatalog(entries, 40)
	assert.Contains(t, text, "Omitted", "over-budget tools must be reported by count:\n%s", text)
	assert.NotContains(t, text, "…+", "no truncated signature should appear")
}

func TestCatalogGroupsMcpServers(t *testing.T) {
	entries := []CatalogTool{
		{Name: "mcp__github__list_prs", Summary: "list pull requests", ServerGroup: "mcp__github"},
		{Name: "mcp__github__get_pr", Summary: "get a pull request", ServerGroup: "mcp__github"},
	}
	text := RenderCatalog(entries, DefaultCatalogBudget)
	require.Contains(t, text, "mcp__github (server, 2 tools)")
	assert.NotContains(t, text, "list_prs", "server group collapses individual tools")
}

func TestScriptDescriptionCarriesCatalog(t *testing.T) {
	tool := NewScriptTool(nil, nil, func(names []string) []CatalogTool {
		return CatalogFromTools([]types.Tool{readTool()})
	}, Options{CatalogBudget: DefaultCatalogBudget})

	desc := tool.CodeDescription()
	assert.Contains(t, desc, "tools.Read({file, line_start?})")
	assert.Contains(t, desc, "synchronous", "the no-async contract must be stated")
	assert.Contains(t, desc, "ToolCallError")
}
