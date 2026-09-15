package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// readLogFile returns the concatenated contents of all log files under
// root/.agents/data/logs.
func readLogFiles(t *testing.T, root string) string {
	t.Helper()
	logsDir := filepath.Join(root, ".agents", "data", "logs")
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(logsDir, e.Name()))
		if err == nil {
			b.Write(data)
		}
	}
	return b.String()
}

// TestBuildLoggerLogLevelOverride verifies the command-line log-level override
// wins over settings.json logging.level, so debug messages are emitted.
func TestBuildLoggerLogLevelOverride(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"),
		[]byte(`{"logging":{"level":"info"}}`), 0o644))

	logger := buildLogger(root, "debug")
	require.NotNil(t, logger)
	logger.Debug("override-debug-line", nil)
	require.NoError(t, logger.Close())

	require.Contains(t, readLogFiles(t, root), "override-debug-line")
}

// TestBuildLoggerNoOverrideUsesConfig verifies that without an override the
// settings.json level (info) filters out debug messages.
func TestBuildLoggerNoOverrideUsesConfig(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".agents"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".agents", "settings.json"),
		[]byte(`{"logging":{"level":"info"}}`), 0o644))

	logger := buildLogger(root, "")
	require.NotNil(t, logger)
	logger.Debug("filtered-debug-line", nil)
	require.NoError(t, logger.Close())

	require.NotContains(t, readLogFiles(t, root), "filtered-debug-line")
}
