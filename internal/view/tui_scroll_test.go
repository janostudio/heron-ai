package view

import (
	"fmt"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Alt+↑/↓ scroll the transcript. Plain ↑/↓ are reserved for input history
// (the REPL convention); scrolling also has Alt+Up/Down, Home/End and the wheel,
// so releasing the arrows does not make long transcripts unreachable.
func TestTUIModel_AltArrowKeysScrollTranscript(t *testing.T) {
	runner := &mockFlowRunner{}
	model := NewTUIModel("test", "gpt-4", 1, 1, runner)
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 12}) // small height -> overflow

	// Enough messages that the transcript overflows the viewport.
	for i := 0; i < 40; i++ {
		model.addMessage(DisplayMessage{Role: RoleAssistant, Content: fmt.Sprintf("line %d", i)})
	}
	model.viewport.GotoBottom()
	require.Equal(t, 1.0, model.viewport.ScrollPercent(), "should start at bottom")

	yBefore := model.viewport.YOffset

	model.Update(tea.KeyMsg{Type: tea.KeyUp, Alt: true})
	assert.Less(t, model.viewport.YOffset, yBefore, "Alt+↑ must scroll the transcript up")

	model.Update(tea.KeyMsg{Type: tea.KeyDown, Alt: true})
	assert.Equal(t, yBefore, model.viewport.YOffset, "Alt+↓ must scroll back down")

	model.Update(tea.KeyMsg{Type: tea.KeyHome})
	assert.Equal(t, 0, model.viewport.YOffset, "Home jumps to top")

	model.Update(tea.KeyMsg{Type: tea.KeyEnd})
	assert.Equal(t, yBefore, model.viewport.YOffset, "End jumps to bottom")
}

// The wheel scrolls the transcript: mouse tracking is on so the wheel reaches
// the program instead of the terminal's (non-existent, under AltScreen)
// scrollback. The trade-off is that native drag-select is captured — users
// select with Shift/Option+drag. See ISSUE-tui-text-not-selectable.
func TestTUIModel_WheelScrollsTranscript(t *testing.T) {
	runner := &mockFlowRunner{}
	model := NewTUIModel("test", "gpt-4", 1, 1, runner)
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for i := 0; i < 40; i++ {
		model.addMessage(DisplayMessage{Role: RoleAssistant, Content: fmt.Sprintf("line %d", i)})
	}
	model.viewport.GotoBottom()
	before := model.viewport.YOffset

	model.Update(tea.MouseMsg{Type: tea.MouseWheelUp})
	assert.Less(t, model.viewport.YOffset, before, "wheel up must scroll")
}

func TestTUIModel_CtrlPNInputHistory(t *testing.T) {
	runner := &mockFlowRunner{}
	model := NewTUIModel("test", "gpt-4", 1, 1, runner)
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model.history = []string{"first", "second"}
	model.historyIdx = -1

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	assert.Equal(t, "second", model.textarea.Value(), "ctrl+p recalls the most recent input")

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlP})
	assert.Equal(t, "first", model.textarea.Value())

	model.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	assert.Equal(t, "second", model.textarea.Value())
}

// ↑/↓ recall input history (the REPL convention). This is the behaviour the
// arrows should have; scrolling lives on Alt+↑/↓, Home/End and wheel.
func TestTUIModel_ArrowKeysRecallInputHistory(t *testing.T) {
	runner := &mockFlowRunner{}
	model := NewTUIModel("test", "gpt-4", 1, 1, runner)
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model.history = []string{"first", "second"}
	model.historyIdx = -1

	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, "second", model.textarea.Value(), "↑ recalls the most recent input")

	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	assert.Equal(t, "first", model.textarea.Value(), "↑ again recalls the older input")

	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "second", model.textarea.Value(), "↓ moves toward newer input")

	model.Update(tea.KeyMsg{Type: tea.KeyDown})
	assert.Equal(t, "", model.textarea.Value(), "↓ past the newest clears the input")
}

// ↑/↓ must not hijack history when the cursor is mid-input in a multi-line box;
// the first ↑ moves the cursor, and only an ↑ on the first line recalls.
func TestTUIModel_ArrowKeyHistoryRespectsMultiLineCursor(t *testing.T) {
	runner := &mockFlowRunner{}
	model := NewTUIModel("test", "gpt-4", 1, 1, runner)
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	model.history = []string{"old message"}
	model.historyIdx = -1

	// Put the cursor on line 1 (second line) so ↑ should move the cursor, not
	// recall history.
	model.textarea.SetValue("line one\nline two")
	model.textarea.CursorDown()

	before := model.textarea.Value()
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	assert.NotEqual(t, "old message", model.textarea.Value(),
		"↑ with the cursor on a lower line must move the cursor, not recall history")
	assert.Contains(t, model.textarea.Value(), "line one", "content is preserved, was %q", before)
}
