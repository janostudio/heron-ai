package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The knowledge path restriction (batch R2) depends on one link holding:
// TurnLoop.Run publishes the caller's identity into the context in a form that
// a Tool in another package can read. internal/tool cannot import this package
// to find out, so nothing there would fail if this link were ever dropped —
// the restriction would just stop being applied, and the file tools would go
// back to returning other agents' private knowledge with no test turning red.
//
// These tests are that guard. They exercise the real Run entry point rather
// than withSpawnIdentity directly, because the failure being guarded against
// is precisely "Run stopped calling it".

// scopeCapturingExecutor records the ToolScope visible to a tool at execution
// time. It stands in for internal/tool's file tools, which read the scope the
// same way.
type scopeCapturingExecutor struct {
	scope types.ToolScope
	seen  bool
	calls int
}

func (e *scopeCapturingExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	e.calls++
	e.scope, e.seen = types.ToolScopeFromContext(ctx)
	return &types.ToolResult{Success: true, Content: "ok"}, nil
}

// runOneToolRound drives one TurnLoop.Run in which the model calls one tool,
// so the executor observes the context a real tool call would receive.
func runOneToolRound(t *testing.T, agent types.AgentConfig, req types.AgentRequest) *scopeCapturingExecutor {
	t.Helper()
	executor := &scopeCapturingExecutor{}
	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "calling",
				ToolCalls: []types.ToolCall{{
					ID:        "call-1",
					Name:      "Grep",
					Arguments: map[string]any{"pattern": "x"},
				}},
			},
			{Text: "done"},
		},
	}
	loop := NewTurnLoop(
		model,
		executor,
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	_, err := loop.Run(context.Background(), agent, req)
	require.NoError(t, err)
	require.Equal(t, 1, executor.calls, "the tool must have been called exactly once")
	return executor
}

func TestTurnLoopPublishesToolScopeToTools(t *testing.T) {
	executor := runOneToolRound(
		t,
		types.AgentConfig{
			Name:  "definition-name",
			Tools: types.ToolConfig{Builtin: []string{"Grep"}},
			Loop:  types.LoopConfig{MaxRounds: 2},
		},
		types.AgentRequest{
			AgentID:       "agent-a",
			TeamID:        "team-1",
			CallID:        "call-agent",
			AgentTurnID:   "agent-turn-1",
			ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
		},
	)

	require.True(t, executor.seen, "a tool executing inside Run must see a ToolScope")
	require.Equal(t, "agent-a", executor.scope.AgentID)
	require.Equal(t, "team-1", executor.scope.TeamID)
}

func TestTurnLoopToolScopeFallsBackToAgentName(t *testing.T) {
	// A request without an AgentID still has an identity: the agent's own
	// name. The scope must carry it, or a turn whose request omitted the id
	// would look anonymous and lose access to its own private knowledge.
	executor := runOneToolRound(
		t,
		types.AgentConfig{
			Name:  "definition-name",
			Tools: types.ToolConfig{Builtin: []string{"Grep"}},
			Loop:  types.LoopConfig{MaxRounds: 2},
		},
		types.AgentRequest{
			CallID:        "call-agent",
			AgentTurnID:   "agent-turn-1",
			ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
		},
	)

	require.True(t, executor.seen)
	require.Equal(t, "definition-name", executor.scope.AgentID)
	require.Empty(t, executor.scope.TeamID)
}

// TestWriteToolDeniesForeignKnowledgeThroughRealTurnLoop is the other half of
// the write restriction's guard, and it is a different claim from the ones in
// internal/tool: those check that WriteTool builds a restricted request, while
// this checks that a real TurnLoop hands WriteTool a context with the identity
// in it. Drop withSpawnIdentity from Run and every tool-level test still
// passes — WriteTool would faithfully pass a restriction computed from an
// empty scope, which denies the caller's own tree and looks like strictness
// rather than a bug.
//
// It runs the real tool registry and the real local workspace, so the path
// travels the whole chain: TurnLoop context → ToolScope → ToolPathRestriction
// → WriteRequest → localWorkspace.Write.
func TestWriteToolDeniesForeignKnowledgeThroughRealTurnLoop(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		".agents/agents/a/knowledge/own.md":   "A_OWN\n",
		".agents/agents/b/knowledge/other.md": "B_SECRET\n",
	}
	for relative, content := range files {
		full := filepath.Join(root, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)
	registry := tool.NewToolRegistry()
	registry.Register(tool.NewWriteTool(ws))

	model := &mockModelProvider{
		responses: []types.ChatResponse{
			{
				Text: "writing",
				ToolCalls: []types.ToolCall{
					{ID: "call-1", Name: "Write", Arguments: map[string]any{
						"file":    ".agents/agents/b/knowledge/planted.md",
						"content": "IMPLANTED\n",
						"mode":    "create",
					}},
					{ID: "call-2", Name: "Write", Arguments: map[string]any{
						"file":    ".agents/agents/a/knowledge/own-2.md",
						"content": "A_OWN_2\n",
						"mode":    "create",
					}},
				},
			},
			{Text: "done"},
		},
	}
	loop := NewTurnLoop(
		model,
		tool.NewToolExecutor(registry),
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	_, err = loop.Run(context.Background(), types.AgentConfig{
		Name:  "a",
		Tools: types.ToolConfig{Builtin: []string{"Write"}},
		Loop:  types.LoopConfig{MaxRounds: 2},
	}, types.AgentRequest{
		AgentID:       "a",
		TeamID:        "t1",
		CallID:        "call-agent",
		AgentTurnID:   "agent-turn-1",
		ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
	})
	require.NoError(t, err)

	// The foreign tree is untouched...
	require.NoFileExists(t, filepath.Join(root, ".agents/agents/b/knowledge/planted.md"))
	// ...while the agent's own tree was written, which is what makes the
	// assertion above mean "denied" rather than "the tool never ran".
	require.FileExists(t, filepath.Join(root, ".agents/agents/a/knowledge/own-2.md"))
}

func TestToolScopeAndSpawnIdentityAgreeOnAgentID(t *testing.T) {
	// The scope is derived from the same arguments that build the spawn
	// identity, so the two must never name different agents. A disagreement
	// would mean a tool sees one caller while the Spawn and State tools see
	// another — the kind of split that is invisible until someone's
	// knowledge or state goes to the wrong owner.
	agent := types.AgentConfig{Name: "definition-name"}
	req := types.AgentRequest{AgentID: "agent-a", TeamID: "team-1"}
	ctx := withSpawnIdentity(context.Background(), agent, req)

	scope, ok := types.ToolScopeFromContext(ctx)
	require.True(t, ok)
	require.Equal(t, currentAgentID(ctx), scope.AgentID)

	// And the fallback path agrees too.
	ctxNoID := withSpawnIdentity(context.Background(), agent, types.AgentRequest{})
	scopeNoID, ok := types.ToolScopeFromContext(ctxNoID)
	require.True(t, ok)
	require.Equal(t, currentAgentID(ctxNoID), scopeNoID.AgentID)
	require.Equal(t, "definition-name", scopeNoID.AgentID)
}
