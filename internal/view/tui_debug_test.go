package view

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The debug log is opt-in and writes to a file (never the AltScreen), and a
// recall attempt is recorded so a "history did not fire" report can be
// diagnosed.
func TestTUIDebugLogRecordsRecall(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "tui-debug.log")
	t.Setenv("HERON_TUI_DEBUG", "1")
	t.Setenv("HERON_TUI_DEBUG_FILE", logPath)

	resetTUIDebug()
	tuiDebugEnabled()

	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model.history = []string{"earlier"}
	model.historyIdx = -1
	model.Update(tea.KeyMsg{Type: tea.KeyUp})

	data, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(data), "key=up")
	assert.Contains(t, string(data), "recall dir=1")
}

// With the flag unset the logger is inert (no file created, no panic).
func TestTUIDebugLogDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "should-not-exist.log")
	t.Setenv("HERON_TUI_DEBUG", "")
	t.Setenv("HERON_TUI_DEBUG_FILE", logPath)
	resetTUIDebug()

	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model.Update(tea.KeyMsg{Type: tea.KeyUp})

	assert.NoFileExists(t, logPath)
}
