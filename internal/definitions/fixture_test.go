package definitions

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// fixture builds a minimal real config tree: one flow, one coordinator team,
// one directory-form agent. Nearly every test needs exactly this as a starting
// point, and the coordinator matters — the loader rejects a flow with anything
// other than exactly one.
//
// Files are written inline rather than kept under testdata/ because the repo has
// no testdata convention and because each test wants to mutate its own copy
// (the rollback test hashes the whole tree).
type fixture struct {
	root     string
	flowPath string
	store    *types.DefinitionStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	root := filepath.Join(t.TempDir(), ".agents")
	f := &fixture{root: root, flowPath: filepath.Join(root, "flows", "default.yml")}

	f.write(t, "flows/default.yml", `
id: default
entry: default

teams:
  default:
    team: qa_team
    coordinator: true
    inputs:
      user_message: true
`)
	f.write(t, "teams/qa_team.yml", `
id: qa_team
goal: 回答用户的问题。

state:
  enabled: true
  max_items: 20

calls:
  answer:
    type: agent
    agent: assistant
    responsibility: 回答用户的问题。
    inputs:
      user_message: true
    output:
      record: Answer

output:
  from: answer
  record: Answer
`)
	f.write(t, "agents/assistant/AGENT.md", `---
name: assistant
persona:
  role: "助手"
  goal: "回答用户问题"
  backstory: "一个乐于助人的 AI 助手"
model:
  provider: openai
  model: gpt-4o-mini
  temperature: 0.7
tools:
  builtin:
    - Read
    - Grep
    - Glob
loop:
  max_rounds: 14
  tool_execution: sequential
hitl:
  enabled: true
---

你是助手，请简洁地回答用户问题。
`)

	f.reload(t)
	return f
}

func (f *fixture) write(t *testing.T, rel, content string) {
	t.Helper()
	path := filepath.Join(f.root, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func (f *fixture) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, filepath.FromSlash(rel)))
	require.NoError(t, err)
	return string(data)
}

func (f *fixture) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(f.root, filepath.FromSlash(rel)))
	return err == nil
}

// load runs the real loader, which is the same validation the writer relies on.
func (f *fixture) load(t *testing.T) *types.Definitions {
	t.Helper()
	definitions, err := config.NewConfigLoader(f.root).LoadDefinitions(
		context.Background(),
		config.DefinitionsLoadRequest{FlowPath: f.flowPath},
	)
	require.NoError(t, err)
	return definitions
}

// reload re-reads the tree and republishes it into the store, mirroring what the
// engine does at startup.
func (f *fixture) reload(t *testing.T) {
	t.Helper()
	definitions := f.load(t)
	f.store = types.NewDefinitionStore(definitions, f.root, f.flowPath)
}

func (f *fixture) writer(t *testing.T) *Writer {
	t.Helper()
	return NewWriter(f.store)
}

// snapshotTree hashes every file under the config root, so a test can assert the
// tree came back byte-identical after a failure. Staging directories are
// excluded: their absence is asserted separately and a leftover would otherwise
// show up as a diff.
func snapshotTree(t *testing.T, root string) map[string][]byte {
	t.Helper()
	snapshot := map[string][]byte{}
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			require.NotContains(t, entry.Name(), ".heron-staging-",
				"a staging directory survived the apply")
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[filepath.ToSlash(rel)] = data
		return nil
	}))
	return snapshot
}

func requireNoStagingDir(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	for _, entry := range entries {
		require.NotContains(t, entry.Name(), ".heron-staging-",
			"staging directory must always be cleaned up")
	}
}

func ctx() context.Context { return context.Background() }
