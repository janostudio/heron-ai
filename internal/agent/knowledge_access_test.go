package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/tool"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// The tests here guard the capability that replaced ReadKnowledge.
//
// ReadKnowledge fetched a knowledge body by id from an in-memory index. It is
// deleted (see the note on builtinSchemas in runtime.go): the knowledge index
// is gone, and reaching a body is now a plain file read at a path — which is
// what the pointer block tells the model to do and what the path restriction
// already governs.
//
// That is only an acceptable trade if the file tools really can reach a
// knowledge body, for the agent that owns it, through the real TurnLoop. These
// tests run the whole chain — TurnLoop context → ToolScope → ToolPathRestriction
// → workspace → filesystem — because that is where the capability would quietly
// disappear: a restriction that denied everything would still pass every
// unit-level "the file is denied" test.

// recordingExecutor wraps a real executor and records each result in call
// order. The TurnLoop does not expose its message list, so this is how a test
// observes what a tool actually returned.
//
// It implements ToolExecutor by delegation rather than reimplementing the
// registry, which is the point: the calls under test run the real file tools
// and the real workspace, so a passing assertion means the whole chain works.
//
// Results are keyed by the tool-call ID, which the fake model sets on each
// call; the executor is not told the id (the tool-call id never reaches the
// executor interface), so correlation is done by the fake model invoking
// exactly one call per batch in these tests.
type recordingExecutor struct {
	inner ToolExecutor
	mu    sync.Mutex
	// outputs is the result of each executed call, in order.
	outputs []string
}

func (e *recordingExecutor) Execute(ctx context.Context, name string, args map[string]any) (*types.ToolResult, error) {
	result, err := e.inner.Execute(ctx, name, args)
	if result != nil {
		text := result.Content
		if !result.Success {
			// The negative assertions need to see a refusal, not emptiness.
			text = result.Error
		}
		e.mu.Lock()
		e.outputs = append(e.outputs, text)
		e.mu.Unlock()
	}
	return result, err
}

// knowledgeBodyFixture writes one shared and one agent-private knowledge file
// and returns a TurnLoop over a real tool registry rooted at that workspace.
func knowledgeBodyFixture(t *testing.T) (root string, loop *TurnLoop, model *mockModelProvider) {
	t.Helper()
	root = t.TempDir()

	files := map[string]string{
		".agents/knowledge/shared-guide.md": "---\nid: shared-guide\nstatus: active\n---\n\n" +
			"SHARED_BODY: the shared guide says X.\n",
		".agents/agents/a/knowledge/own-notes.md": "---\nid: own-notes\nstatus: active\n---\n\n" +
			"PRIVATE_BODY: agent a's own notes say Y.\n",
		".agents/agents/b/knowledge/secret.md": "---\nid: secret\nstatus: active\n---\n\n" +
			"B_SECRET: never readable by a.\n",
	}
	for relative, content := range files {
		full := filepath.Join(root, filepath.FromSlash(relative))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(content), 0o644))
	}

	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)
	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool(ws))
	registry.Register(tool.NewGrepTool(ws))
	registry.Register(tool.NewGlobTool(ws))

	model = &mockModelProvider{}
	loop = NewTurnLoop(
		model,
		&recordingExecutor{inner: tool.NewToolExecutor(registry)},
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)
	return root, loop, model
}

// runKnowledgeTools drives one turn in which the model calls the given tools,
// one per turn, and returns each call's result text in order — content on
// success, the refusal reason on failure.
//
// One tool call per turn is deliberate: results come back in execution order,
// and a batch of several parallel-safe reads would make that order
// unspecified. Tests that need three probes issue three turns.
//
// The model and the recorder are both reset per turn: mockModelProvider walks
// a response list by an internal counter that Run does not rewind, so reusing
// one instance across turns would silently hand the second turn the default
// empty response and no tool calls.
func runKnowledgeTools(t *testing.T, loop *TurnLoop, model *mockModelProvider, self, team string, calls []types.ToolCall) []string {
	t.Helper()
	recorder, ok := loop.toolExecutor.(*recordingExecutor)
	require.True(t, ok, "the fixture must install a recordingExecutor")

	var outputs []string
	for _, call := range calls {
		model.callCount = 0
		model.responses = []types.ChatResponse{
			{Text: "looking", ToolCalls: []types.ToolCall{call}},
			{Text: "done"},
		}
		_, err := loop.Run(context.Background(), types.AgentConfig{
			Name:  self,
			Tools: types.ToolConfig{Builtin: []string{"Read", "Grep", "Glob"}},
			Loop:  types.LoopConfig{MaxRounds: 2},
		}, types.AgentRequest{
			AgentID:       self,
			TeamID:        team,
			CallID:        "call-agent",
			AgentTurnID:   "agent-turn-1",
			ContextBlocks: []types.ContextBlock{{Kind: "input", Text: "hello"}},
		})
		require.NoError(t, err)

		recorder.mu.Lock()
		require.Len(t, recorder.outputs, 1, "the single tool call must have produced a result")
		outputs = append(outputs, recorder.outputs[0])
		recorder.outputs = nil
		recorder.mu.Unlock()
	}
	return outputs
}

// TestReadReachesKnowledgeBodyByPath is the capability assertion: with
// ReadKnowledge gone, a plain Read must return a knowledge document's body.
//
// Without this, the deletion could have silently cost the ability to read any
// knowledge at all — the pointer block would tell the model to Read a path and
// the path would be refused, which looks like an empty knowledge base rather
// than a bug.
func TestReadReachesKnowledgeBodyByPath(t *testing.T) {
	_, loop, model := knowledgeBodyFixture(t)

	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "read-shared", Name: "Read", Arguments: map[string]any{
			"file": ".agents/knowledge/shared-guide.md",
		}},
	})

	require.Len(t, outputs, 1)
	require.Contains(t, outputs[0], "SHARED_BODY", "a plain Read must return a knowledge body")
}

// TestReadReachesOwnPrivateKnowledgeBody is the other half: the agent's own
// private tree is readable by it, not just the shared one. An over-broad denial
// would pass the test above and fail this one.
func TestReadReachesOwnPrivateKnowledgeBody(t *testing.T) {
	_, loop, model := knowledgeBodyFixture(t)

	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "read-own", Name: "Read", Arguments: map[string]any{
			"file": ".agents/agents/a/knowledge/own-notes.md",
		}},
	})

	require.Len(t, outputs, 1)
	require.Contains(t, outputs[0], "PRIVATE_BODY", "an agent must be able to read its own private knowledge")
}

// TestReadDeniesAnotherAgentsKnowledgeBody is the negative control that makes
// the two tests above mean "the model can read knowledge" rather than "the
// model can read anything".
func TestReadDeniesAnotherAgentsKnowledgeBody(t *testing.T) {
	_, loop, model := knowledgeBodyFixture(t)

	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "read-foreign", Name: "Read", Arguments: map[string]any{
			"file": ".agents/agents/b/knowledge/secret.md",
		}},
	})

	require.Len(t, outputs, 1)
	require.NotContains(t, outputs[0], "B_SECRET", "another agent's private knowledge must not be readable")
}

// TestGrepSearchesKnowledgeBodies is the retrieval replacement.
//
// The injector used to search entries in memory (metadata only, no regex, no
// lines). Grep is what the model uses now, and this asserts it actually sees
// knowledge bodies — the whole premise of moving to agentic search.
func TestGrepSearchesKnowledgeBodies(t *testing.T) {
	_, loop, model := knowledgeBodyFixture(t)

	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "grep-shared", Name: "Grep", Arguments: map[string]any{
			"pattern": "SHARED_BODY",
			"path":    ".agents/knowledge",
		}},
		{ID: "grep-own", Name: "Grep", Arguments: map[string]any{
			"pattern": "PRIVATE_BODY",
			"path":    ".agents/agents/a/knowledge",
		}},
		{ID: "grep-foreign", Name: "Grep", Arguments: map[string]any{
			"pattern": "B_SECRET",
			"path":    ".agents/agents/b/knowledge",
		}},
	})

	require.Len(t, outputs, 3)
	require.Contains(t, outputs[0], "SHARED_BODY", "Grep must find a shared knowledge body")
	require.Contains(t, outputs[1], "PRIVATE_BODY", "Grep must find the agent's own knowledge body")
	require.NotContains(t, outputs[2], "B_SECRET",
		"Grep must not reach another agent's private knowledge")
}

// TestKnowledgeBodyHasNoIDToPathMapping documents the one capability that IS
// lost with ReadKnowledge, so the loss is recorded rather than discovered
// later.
//
// ReadKnowledge resolved an entry's *id* to its *path* through the index. With
// the index gone there is no id lookup: an agent holding only the string
// "own-notes" must search for it (Glob/Grep) to find the file. That is the
// intended flow — the pointer block names directories, not entries, and
// locating a file by name is what Glob is for — but it is a genuine difference
// from the deleted tool and worth pinning so nobody assumes an id lookup still
// exists.
func TestKnowledgeBodyHasNoIDToPathMapping(t *testing.T) {
	_, loop, model := knowledgeBodyFixture(t)

	// Reading by the bare id fails: an id is not a path, and nothing
	// translates one into the other any more.
	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "read-by-id", Name: "Read", Arguments: map[string]any{"file": "own-notes"}},
	})
	require.Len(t, outputs, 1)
	require.NotContains(t, outputs[0], "PRIVATE_BODY",
		"a bare id is not a path; reading by id is not supported any more")
}

// TestGlobThenReadRecoversTheBodyByID completes the previous test: the
// two-step route (Glob for the id, then Read the path) must actually work, or
// "still reachable by id" would be false and the deletion would be a real
// capability loss rather than an indirection.
func TestGlobThenReadRecoversTheBodyByID(t *testing.T) {
	root := t.TempDir()
	full := filepath.Join(root, ".agents/agents/a/knowledge/own-notes.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(
		"---\nid: own-notes\nstatus: active\n---\n\nPRIVATE_BODY: agent a's own notes say Y.\n"), 0o644))

	ws, err := workspace.NewLocal(root)
	require.NoError(t, err)
	registry := tool.NewToolRegistry()
	registry.Register(tool.NewReadTool(ws))
	registry.Register(tool.NewGlobTool(ws))

	model := &mockModelProvider{}
	loop := NewTurnLoop(
		model,
		&recordingExecutor{inner: tool.NewToolExecutor(registry)},
		nil,
		NewRouteParser(),
		nil,
		nil,
		&mockPromptRenderer{messages: []types.Message{{Role: "user", Content: "hello"}}},
	)

	outputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "glob", Name: "Glob", Arguments: map[string]any{
			"pattern": ".agents/agents/a/knowledge/*.md",
		}},
	})

	require.Len(t, outputs, 1)
	found := outputs[0]
	require.Contains(t, found, "own-notes.md",
		"Glob must be able to locate the file from the knowledge directory the pointer block named")

	// The discovered path is then readable, which closes the loop: id ->
	// (Glob) -> path -> (Read) -> body.
	discovered := strings.TrimSpace(strings.SplitN(found, "\n", 2)[0])
	readOutputs := runKnowledgeTools(t, loop, model, "a", "t1", []types.ToolCall{
		{ID: "read", Name: "Read", Arguments: map[string]any{"file": discovered}},
	})
	require.Len(t, readOutputs, 1)
	require.Contains(t, readOutputs[0], "PRIVATE_BODY")
}
