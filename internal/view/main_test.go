package view

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestMain points the persisted input history at a throwaway file so tests do
// not read or write the developer's real history, and do not see each other's
// entries. Individual tests that need their own file set
// HERON_TUI_HISTORY_FILE themselves.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "heron-view-history")
	if err == nil {
		_ = os.Setenv("HERON_TUI_HISTORY_FILE", filepath.Join(dir, "history"))
		defer os.RemoveAll(dir)
	}
	os.Exit(m.Run())
}

// ---- Shared test helpers ----
//
// All package-level test helpers live here so they are defined once and cannot
// drift out of sync with their callers (a half-written test file once referenced
// helpers that were added a moment later, which broke the build mid-edit).

// testWindowSize is the standard size events tests feed the model.
func testWindowSize() tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: 80, Height: 20} }

// testKeyUp is a plain ↑ key press.
func testKeyUp() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyUp} }

// submitInput types input and presses Enter (the branch that appends history).
func submitInput(t *testing.T, m *TUIModel, input string) {
	t.Helper()
	m.textarea.SetValue(input)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
}

// isolateHistory points the persisted input history at a file this test owns,
// so a model built here starts with empty history regardless of what other
// tests have persisted.
func isolateHistory(t *testing.T) {
	t.Helper()
	t.Setenv("HERON_TUI_HISTORY_FILE", filepath.Join(t.TempDir(), "history"))
}
