package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/knowledge"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// inertModel is a ModelProvider that never chats. It exists for tests whose
// subject is startup wiring rather than a turn: BuildRuntime requires a
// provider, but nothing in the paths under test should ever reach it.
type inertModel struct{}

func (m *inertModel) Chat(_ context.Context, _ []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (*types.ChatResponse, error) {
	return &types.ChatResponse{Text: "unused"}, nil
}

func (m *inertModel) ChatStream(_ context.Context, _ []types.Message, _ []types.JSONSchema, _ types.ModelConfig) (<-chan types.ChatChunk, error) {
	ch := make(chan types.ChatChunk)
	close(ch)
	return ch, nil
}

// TestBuildRuntimeFailsLoudlyOnInvalidKnowledge is the startup-level guard for
// the knowledge scope/location validation.
//
// It exists because of how the knowledge index used to be wired:
//
//	if entries, loadErr := knowledgeStore.Load(ctx); loadErr == nil {
//		... build index, register ReadKnowledge ...
//	}
//
// That shape read "Load failed" as "there is no knowledge here". For an absent
// directory that is right, and for a *rejected* one it is exactly backwards:
// the tree is malformed and the engine would come up healthy with the whole
// knowledge feature silently off. The failure is then invisible — the agent
// simply behaves as if the workspace had no knowledge, which is
// indistinguishable from the truth and therefore unreportable.
//
// So this asserts the loud path: a store with an entry whose frontmatter scope
// cannot be expressed by its location must stop BuildRuntime, and the error
// must carry enough to find the file.
//
// Note which directory the knowledge tree goes in: BuildRuntime's FileStore is
// rooted at the workspaceRoot argument, not at the process cwd, so
// ".agents/knowledge" is relative to that. Putting the fixture under the config
// root (as the definition layout would suggest) would leave the knowledge store
// looking at an empty directory and this test would pass vacuously.
func TestBuildRuntimeFailsLoudlyOnInvalidKnowledge(t *testing.T) {
	_, flowPath, definitions := fixtureConfigRoot(t, t.TempDir())
	workspaceRoot := t.TempDir()

	// An agent-private entry in the shared knowledge directory: the silent-leak
	// case from the validation rules. The pre-existing examples/ trees contain
	// files like this, so this is a realistic broken tree, not a contrived one.
	knowledgeDir := filepath.Join(workspaceRoot, ".agents", "knowledge")
	require.NoError(t, os.MkdirAll(knowledgeDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(knowledgeDir, "leaky.md"), []byte(
		"---\nid: leaky\nscope:\n  type: agent\n  agents: [helper]\nstatus: active\n---\n\nShould not load.\n",
	), 0o644))

	store := types.NewDefinitionStore(definitions, filepath.Dir(flowPath), flowPath)

	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error")
	require.Error(t, err, "a broken knowledge tree must fail the build, not disable the feature quietly")
	require.Nil(t, bundle)

	require.Contains(t, err.Error(), "leaky.md", "the error must name the file that is wrong")
	require.Contains(t, err.Error(), "scope", "the error must say what is wrong with it")
	require.Contains(t, err.Error(), "Fix:", "the error must say how to fix it")
}

// TestBuildRuntimeToleratesAbsentKnowledgeDirectory is the counterweight to the
// test above, and the reason BuildRuntime cannot simply propagate every Load
// error: most workspaces have no knowledge base at all.
//
// "No directory" and "malformed directory" must be told apart, or making the
// second loud would make the first fatal and no clean workspace could start.
// The Exists check on the private stores is the same distinction one level
// down: most agents have no private knowledge directory, and only a directory
// that is present and then fails to load is an error.
func TestBuildRuntimeToleratesAbsentKnowledgeDirectory(t *testing.T) {
	_, flowPath, definitions := fixtureConfigRoot(t, t.TempDir())
	workspaceRoot := t.TempDir()

	// Guard the fixture: the directory really is absent, so this test would
	// fail rather than pass vacuously if the existence check were removed.
	_, statErr := os.Stat(filepath.Join(workspaceRoot, ".agents", "knowledge"))
	require.True(t, os.IsNotExist(statErr), "fixture must have no knowledge directory")

	store := types.NewDefinitionStore(definitions, filepath.Dir(flowPath), flowPath)

	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error")
	require.NoError(t, err, "a workspace with no knowledge must still start")
	require.NotNil(t, bundle)
}

// TestBuildRuntimeLoadsAgentPrivateKnowledge is the round-trip through the
// wiring internal/app actually uses: one store for the shared tree, one store
// per agent for the private tree. It is the test that would have caught the
// private-store derivation gap — a store rooted at
// .agents/agents/<id>/knowledge lists paths whose owner segment is present, and
// reading them as global would reject the very entries the engine just wrote,
// i.e. BuildRuntime would refuse to start on a correct tree.
//
// It asserts through the same store shape BuildRuntime builds rather than
// reaching into the runtime for its index: the wiring under test is "which
// store reads which directory and does its content survive validation", and an
// accessor added to the runtime only for this test would not make that claim
// stronger.
func TestBuildRuntimeLoadsAgentPrivateKnowledge(t *testing.T) {
	_, flowPath, definitions := fixtureConfigRoot(t, t.TempDir())
	workspaceRoot := t.TempDir()

	privateDir := filepath.Join(workspaceRoot, ".agents", "agents", "helper", "knowledge")
	require.NoError(t, os.MkdirAll(privateDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "checklist.md"), []byte(
		"---\nid: checklist\nscope:\n  type: agent\n  agents: [helper]\nstatus: active\n---\n\nHelper's own checklist.\n",
	), 0o644))
	// A bare scope in the private directory must normalize rather than fail, so
	// the test covers the fill-in path as well as the explicit one.
	require.NoError(t, os.WriteFile(filepath.Join(privateDir, "bare.md"), []byte(
		"---\nid: bare\nstatus: active\n---\n\nNo scope declared.\n",
	), 0o644))

	store := types.NewDefinitionStore(definitions, filepath.Dir(flowPath), flowPath)

	bundle, err := BuildRuntime(context.Background(), store, &inertModel{}, workspaceRoot, "error")
	require.NoError(t, err, "a correct private knowledge tree must not stop the build")
	require.NotNil(t, bundle)

	// The same store shape BuildRuntime constructs for this agent, over the
	// same workspace root.
	privateStore := knowledge.NewMarkdownStore(
		storage.NewFileStore(workspaceRoot),
		filepath.Join(".agents", "agents", "helper", "knowledge"),
	)
	require.True(t, privateStore.Exists())

	entries, err := privateStore.Load(context.Background())
	require.NoError(t, err)
	require.Len(t, entries, 2)

	byID := make(map[string]types.KnowledgeEntry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}

	// Both entries must carry the scope their location implies. An entry that
	// loaded but came back visible to everyone would be the leak this
	// validation exists to prevent.
	require.Equal(t, "agent", byID["checklist"].Scope.Type,
		"an explicit agent scope must survive the load")
	require.Equal(t, []string{"helper"}, byID["checklist"].Scope.Agents)

	require.Equal(t, "agent", byID["bare"].Scope.Type,
		"a bare scope in a private directory must normalize to the owner")
	require.Equal(t, []string{"helper"}, byID["bare"].Scope.Agents)
}
