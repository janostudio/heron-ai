package config

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

type countingFileStore struct {
	storage.FileStore
	reads atomic.Int32
}

func (c *countingFileStore) Read(path string) ([]byte, error) {
	c.reads.Add(1)
	return c.FileStore.Read(path)
}

// TestSettingsAreReadOncePerLoaderGroup covers the repeated IO: seven Load*
// methods each read and parsed the same file, and ConfigLoader is constructed
// per call site, so one startup read it a dozen times.
func TestSettingsAreReadOncePerLoaderGroup(t *testing.T) {
	InvalidateSettingsCache()

	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "settings.json"), []byte(`{
  "runtime": {"max_team_turns": 5},
  "logging": {"level": "debug"},
  "tools": {"restricted_bash_phrases": ["dropdb"]},
  "knowledge": {},
  "web": {"provider": "direct"},
  "env": {"A": "b"},
  "mcp": []
}`), 0o644))

	files := &countingFileStore{FileStore: storage.NewFileStore(dir)}
	loader := &ConfigLoader{baseDir: dir, fileStore: files}

	require.Equal(t, 5, loader.LoadRuntimeLimits().MaxTeamTurns)
	require.Equal(t, "debug", loader.LoadLoggingSettings().Level)
	require.Equal(t, []string{"dropdb"}, loader.LoadToolPolicyPhrases())
	_ = loader.LoadKnowledgeSettings()
	_ = loader.LoadWebSettings()
	require.Equal(t, filepath.Join(dir, "b"), loader.LoadGlobalEnv()["A"], "relative env values resolve against the base dir")
	_, err := loader.LoadMCPServers()
	require.NoError(t, err)

	require.Equal(t, int32(1), files.reads.Load(),
		"the settings file must be read once, not once per section")

	// Another loader over the same directory reuses the cache.
	other := &ConfigLoader{baseDir: dir, fileStore: files}
	require.Equal(t, "debug", other.LoadLoggingSettings().Level)
	require.Equal(t, int32(1), files.reads.Load())

	// An explicit invalidation is the way to pick up an edit.
	InvalidateSettingsCache()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agents", "settings.json"), []byte(`{"logging":{"level":"warn"}}`), 0o644))
	require.Equal(t, "warn", loader.LoadLoggingSettings().Level)
	require.Equal(t, int32(2), files.reads.Load())
}
