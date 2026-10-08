package team

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/runtime/call"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// blockCapturingRunner is a types.AgentRunner that records the ContextBlocks
// of the calls it is handed. Those blocks are the only place the knowledge
// pointer exists before PromptRenderer turns them into messages, so this is
// how a Team-runtime test observes what the model would be told.
type blockCapturingRunner struct {
	mu     sync.Mutex
	blocks []types.ContextBlock
}

func (r *blockCapturingRunner) Run(_ context.Context, _ types.AgentConfig, req types.AgentRequest) (*types.AgentResult, error) {
	r.mu.Lock()
	r.blocks = append(r.blocks, req.ContextBlocks...)
	r.mu.Unlock()
	return &types.AgentResult{Status: types.TurnCompleted, Reply: "ok"}, nil
}

func (r *blockCapturingRunner) knowledgeBlocks() []types.ContextBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	var blocks []types.ContextBlock
	for _, block := range r.blocks {
		if block.Kind == "knowledge" {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

// runOneAgentCall drives one TeamTurn containing a single agent call and
// returns the knowledge blocks the agent's CallRequest carried.
func runOneAgentCall(t *testing.T, runtime *Runtime, agentID, teamID string) []types.ContextBlock {
	t.Helper()
	runner := &blockCapturingRunner{}
	registry := call.NewRegistry()
	require.NoError(t, registry.Register(call.NewAgentExecutor(runner)))
	runtime.executors = registry

	_, err := runtime.Run(context.Background(), types.TeamTurnRequest{
		FlowSession: types.FlowSession{ID: "fs-1"},
		FlowTurn:    types.FlowTurn{ID: "ft-1"},
		TeamSession: types.TeamSession{ID: "ts-1"},
		TeamTurn:    types.TeamTurn{ID: "tt-1", TeamID: teamID},
		Team: types.Team{
			ID: teamID,
			Calls: map[string]types.Call{
				"work": {ID: "work", Type: types.CallAgent, AgentID: agentID, Responsibility: "do work"},
			},
		},
	})
	require.NoError(t, err)
	return runner.knowledgeBlocks()
}

// TestTeamRuntimeAddsKnowledgePointerBlock is the wiring guard for the block
// that replaced per-query knowledge injection.
//
// The pointer's own tests (internal/knowledge/pointer_test.go) prove what the
// text says. This proves the Team runtime actually puts it in front of the
// model, with the right Kind and Stability — the half that would fail silently
// if SetKnowledgePointer were dropped, since the model would simply never be
// told where knowledge lives and the workspace would behave as if it had none.
func TestTeamRuntimeAddsKnowledgePointerBlock(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	require.NoError(t, files.Write(".agents/knowledge/guide.md", []byte(
		"---\nid: guide\nstatus: active\n---\n\nA shared guide.\n")))

	runtime := newTestRuntime(call.NewRegistry(), map[string]types.AgentConfig{
		"assistant": {Name: "assistant", Tools: types.ToolConfig{Builtin: []string{"Grep"}}},
	})
	runtime.SetKnowledgePointer(knowledge.NewKnowledgePointer(files, ".agents/knowledge", ".agents"))

	blocks := runOneAgentCall(t, runtime, "assistant", "t1")

	require.Len(t, blocks, 1)
	block := blocks[0]
	require.Contains(t, block.Text, ".agents/knowledge/")
	require.Contains(t, block.Text, "Grep", "the block must direct the model to the file tools")

	// Stability is the property the design note argues for: the block is a
	// function of the agent's identity, not of the query, so it is `stable`
	// and can sit in a cached prefix. Asserting it here means a regression to
	// `semi_stable` — which would silently cost a cache miss every turn — has
	// to be a deliberate edit rather than a typo.
	require.Equal(t, "stable", block.Stability)
	require.Equal(t, "system", block.Placement)
	require.False(t, block.Compressible,
		"the block is fixed and load-bearing; compacting it away would disable knowledge lookup")
}

// TestTeamRuntimeKnowledgeBlockIsQueryIndependent is the runtime-level version
// of the pointer's own property test: two TeamTurns with completely different
// inputs must produce the same block, which is what "no longer depends on the
// query" means end to end.
func TestTeamRuntimeKnowledgeBlockIsQueryIndependent(t *testing.T) {
	files := storage.NewFileStore(t.TempDir())
	require.NoError(t, files.Write(".agents/knowledge/guide.md", []byte(
		"---\nid: guide\nstatus: active\n---\n\nA shared guide.\n")))

	runtime := newTestRuntime(call.NewRegistry(), map[string]types.AgentConfig{
		"assistant": {Name: "assistant", Tools: types.ToolConfig{Builtin: []string{"Grep"}}},
	})
	runtime.SetKnowledgePointer(knowledge.NewKnowledgePointer(files, ".agents/knowledge", ".agents"))

	first := runOneAgentCall(t, runtime, "assistant", "t1")
	second := runOneAgentCall(t, runtime, "assistant", "t1")

	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Equal(t, first[0].Text, second[0].Text,
		"the same agent must get byte-identical pointer text on every turn")
}

// TestTeamRuntimeOmitsKnowledgeBlockWhenUnwired pins the opt-in shape: a
// runtime without a pointer adds no block, which is what keeps the CLI paths
// that build no knowledge-capable runtime unchanged.
func TestTeamRuntimeOmitsKnowledgeBlockWhenUnwired(t *testing.T) {
	runtime := newTestRuntime(call.NewRegistry(), map[string]types.AgentConfig{
		"assistant": {Name: "assistant", Tools: types.ToolConfig{Builtin: []string{"Grep"}}},
	})

	require.Empty(t, runOneAgentCall(t, runtime, "assistant", "t1"))
}
