package view

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Input history must survive a restart: it is written to disk on submit and
// loaded when a new model starts. This is the fix for "↑/↓ only reach the
// current session's inputs, gone after Ctrl+C".
func TestHistoryPersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	t.Setenv("HERON_TUI_HISTORY_FILE", path)

	// First "run": build a model, submit two inputs.
	first := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	first.Update(testWindowSize())
	submitInput(t, first, "first command")
	first.running = false
	submitInput(t, first, "second command")

	// Second "run": a fresh model loads the persisted history.
	second := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	second.LoadHistory()

	history := second.GetHistory()
	require.Len(t, history, 2)
	assert.Equal(t, "first command", history[0])
	assert.Equal(t, "second command", history[1])

	// ↑ recalls the most recent input.
	second.Update(testKeyUp())
	assert.Equal(t, "second command", second.textarea.Value())
}

func TestLoadHistoryMissingFileIsEmpty(t *testing.T) {
	t.Setenv("HERON_TUI_HISTORY_FILE", filepath.Join(t.TempDir(), "does-not-exist"))
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.LoadHistory()
	assert.Empty(t, model.GetHistory())
	assert.NotNil(t, model.GetHistory(), "never nil")
}

func TestAppendHistorySkipsAdjacentDuplicate(t *testing.T) {
	h := appendHistory([]string{"a"}, "a")
	assert.Equal(t, []string{"a"}, h, "a repeat of the last entry is not stored twice")

	h = appendHistory([]string{"a"}, "b")
	assert.Equal(t, []string{"a", "b"}, h)
}

func TestAppendHistoryFlattensNewlines(t *testing.T) {
	h := appendHistory(nil, "line one\nline two")
	require.Len(t, h, 1)
	assert.Equal(t, "line one line two", h[0], "one input is one history entry")
}

func TestAppendHistoryTrimsAndIgnoresBlank(t *testing.T) {
	assert.Empty(t, appendHistory(nil, "   "))
	assert.Equal(t, []string{"x"}, appendHistory(nil, "  x  "))
}

func TestAppendHistoryCaps(t *testing.T) {
	var h []string
	// Fill via appendHistory with unique values: adjacent duplicates are
	// dropped by design, so a fill of the same value never reaches the cap.
	for i := 0; i < tuiHistoryMax+10; i++ {
		h = appendHistory(h, string(rune('a'+i%26))+string(rune('0'+i%10)))
	}
	assert.LessOrEqual(t, len(h), tuiHistoryMax)
}

// TestPersistedFileIsReadable directly checks the on-disk format is one entry
// per line, so an external look at the file matches what ↑ recalls.
func TestPersistedFileIsReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history")
	t.Setenv("HERON_TUI_HISTORY_FILE", path)

	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(testWindowSize())
	submitInput(t, model, "hello world")

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", string(data))
}
