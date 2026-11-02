package tool_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/internal/tool/script"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newScriptFixture wires the real tool stack: a local workspace, the real
// registry and executor, and a Script tool over them. It is deliberately not a
// mock — the point is to prove the wiring, not the engine in isolation. The
// executor is returned as well so tests can drive a tool directly and compare
// that path with the scripted one.
func newScriptFixture(t *testing.T) (*script.ScriptTool, *tool.ToolExecutor, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644))

	ws, err := workspace.NewLocalWithReadRoots(root, nil, nil)
	require.NoError(t, err)

	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool(ws))
	registry.Register(tool.NewWriteTool(ws))
	registry.Register(tool.NewGrepTool(ws))
	executor := tool.NewToolExecutor(registry)

	store := script.NewFileStepStore(storage.NewFileStore(t.TempDir()), "scripts")
	// Mirrors the production wiring: the catalog never lists Script itself, so a
	// description render cannot re-enter the registry.
	catalog := func(names []string) []script.CatalogTool {
		allowed := map[string]struct{}{}
		for _, name := range names {
			allowed[name] = struct{}{}
		}
		var visible []types.Tool
		for _, candidate := range registry.List() {
			if candidate.Name() == script.ToolName {
				continue
			}
			if len(allowed) > 0 {
				if _, ok := allowed[candidate.Name()]; !ok {
					continue
				}
			}
			visible = append(visible, candidate)
		}
		return script.CatalogFromTools(visible)
	}
	scriptTool := script.NewScriptTool(executor, store, catalog, script.Options{SpecOf: registry.ExecutionSpec})
	registry.Register(scriptTool)
	return scriptTool, executor, root
}

func TestScriptReadsRealWorkspace(t *testing.T) {
	scriptTool, _, _ := newScriptFixture(t)

	result, err := scriptTool.Execute(context.Background(), map[string]any{
		"code": `const r = tools.Read({file: "a.txt"}); return r.content;`,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)
	assert.Contains(t, result.Content, "alpha")
	assert.Equal(t, 1, result.Metadata["steps"])
}

// The same programme must not touch the workspace twice: the second run reads
// its answers from the replay log.
func TestScriptReplaysSecondRun(t *testing.T) {
	scriptTool, _, _ := newScriptFixture(t)
	code := `const r = tools.Read({file: "a.txt"}); return r.content;`

	first, err := scriptTool.Execute(context.Background(), map[string]any{"code": code})
	require.NoError(t, err)
	require.True(t, first.Success, "first run failed: %s", first.Error)
	assert.Equal(t, 1, first.Metadata["steps"])

	second, err := scriptTool.Execute(context.Background(), map[string]any{"code": code})
	require.NoError(t, err)
	require.True(t, second.Success, "replay failed: %s", second.Error)
	assert.Equal(t, 0, second.Metadata["steps"], "replayed calls are not new work")
	assert.Contains(t, second.Content, "alpha")
}

// A script that writes must surface the write operation to the turn, otherwise
// file history and undo would miss it.
func TestScriptWriteSurfacesWorkspaceOps(t *testing.T) {
	scriptTool, _, root := newScriptFixture(t)

	result, err := scriptTool.Execute(context.Background(), map[string]any{
		"code": `return tools.Write({file: "b.txt", content: "beta", mode: "create"}).metadata.revision;`,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)
	require.Len(t, result.WorkspaceOps, 1)
	assert.Equal(t, "write", result.WorkspaceOps[0].Kind)
	assert.Equal(t, "b.txt", result.WorkspaceOps[0].Path)

	body, readErr := os.ReadFile(filepath.Join(root, "b.txt"))
	require.NoError(t, readErr)
	assert.Equal(t, "beta", string(body))
}

// Grep's structured result is what makes batch aggregation worth doing, so it is
// the interesting case for "did the real tool result reach the script intact".
func TestScriptReadsStructuredToolResult(t *testing.T) {
	scriptTool, _, _ := newScriptFixture(t)

	result, err := scriptTool.Execute(context.Background(), map[string]any{
		"code": `const r = tools.Grep({pattern: "alpha"});
		         return r.ok + ":" + r.metadata.matches;`,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)
	assert.Equal(t, "true:1", result.Content)
}

// Batch mode over the real stack: two reads, one round of tool work, ordered
// results.
func TestScriptBatchOverRealTools(t *testing.T) {
	scriptTool, _, root := newScriptFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(root, "c.txt"), []byte("gamma"), 0o644))

	result, err := scriptTool.Execute(context.Background(), map[string]any{
		"code": `const rs = tools.all([{name:"Read", args:{file:"a.txt"}}, {name:"Read", args:{file:"c.txt"}}]);
		         return rs.map(r => r.content).join("|");`,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)
	assert.Contains(t, result.Content, "alpha")
	assert.Contains(t, result.Content, "gamma")
	assert.Equal(t, 2, result.Metadata["steps"])
}

// A denied tool call must be a catchable error the script can reason about, not
// a crashed run.
func TestScriptSurfacesToolFailure(t *testing.T) {
	scriptTool, _, _ := newScriptFixture(t)

	result, err := scriptTool.Execute(context.Background(), map[string]any{
		"code": `try { tools.Read({file: "missing.txt"}); return "no-throw"; }
		         catch (e) { return e.name + ":" + e.tool; }`,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "script failed: %s", result.Error)
	assert.Contains(t, result.Content, "ToolCallError")
	assert.Contains(t, result.Content, "Read")
}
