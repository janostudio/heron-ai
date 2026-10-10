package view

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/sessionlist"
)

func sampleSessions() []sessionlist.Session {
	return []sessionlist.Session{
		{ID: "fs_new", Title: "newest", Turns: 3, Tokens: 59340, UpdatedAt: time.Date(2026, 10, 9, 16, 12, 0, 0, time.UTC)},
		{ID: "fs_old", Title: "older", Turns: 1, Tokens: 5300, UpdatedAt: time.Date(2026, 10, 8, 6, 20, 0, 0, time.UTC)},
	}
}

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestSessionPickerEnterSelectsHighlighted(t *testing.T) {
	p := NewSessionPicker(sampleSessions())
	// Move down to the second row, then confirm.
	p.Update(keyMsg("down"))
	p.Update(keyMsg("enter"))
	assert.Equal(t, "fs_old", p.Chosen())
}

func TestSessionPickerEnterSelectsFirstByDefault(t *testing.T) {
	p := NewSessionPicker(sampleSessions())
	p.Update(keyMsg("enter"))
	assert.Equal(t, "fs_new", p.Chosen())
}

func TestSessionPickerEscCancels(t *testing.T) {
	p := NewSessionPicker(sampleSessions())
	p.Update(keyMsg("esc"))
	assert.Equal(t, "", p.Chosen())
	assert.True(t, p.quit)
}

func TestSessionPickerNavigationClamps(t *testing.T) {
	p := NewSessionPicker(sampleSessions())
	p.Update(keyMsg("up")) // already at top
	assert.Equal(t, 0, p.index)
	p.Update(keyMsg("down"))
	p.Update(keyMsg("down")) // past bottom
	assert.Equal(t, 1, p.index)
}

func TestRenderSessionsText(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RenderSessionsText(&buf, sampleSessions()))

	out := buf.String()
	assert.Contains(t, out, "fs_new")
	assert.Contains(t, out, "fs_old")
	assert.Contains(t, out, "newest")
	assert.Contains(t, out, "3 轮")
	assert.Contains(t, out, "59.3k tok")
	assert.Contains(t, out, "共 2 个会话")
	// Plain text: no ANSI escapes, so `| grep` stays clean.
	assert.False(t, strings.Contains(out, "\x1b["), "non-interactive output must be free of ANSI")
}

func TestRenderSessionsTextEmpty(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, RenderSessionsText(&buf, nil))
	assert.Contains(t, buf.String(), "暂无会话")
}

func TestFormatSessionRowMarksActive(t *testing.T) {
	active := sessionlist.Session{ID: "fs_a", Active: true, UpdatedAt: time.Now()}
	idle := sessionlist.Session{ID: "fs_i", Active: false, UpdatedAt: time.Now()}
	assert.True(t, strings.HasPrefix(FormatSessionRow(active), "●"))
	assert.True(t, strings.HasPrefix(FormatSessionRow(idle), "○"))
}

func manySessions(n int) []sessionlist.Session {
	out := make([]sessionlist.Session, n)
	for i := range out {
		out[i] = sessionlist.Session{
			ID:        "fs_" + string(rune('a'+i)),
			Title:     "session",
			UpdatedAt: time.Date(2026, 10, 9, 16, 0, 0, 0, time.UTC),
		}
	}
	return out
}

func windowSize(w, h int) tea.WindowSizeMsg {
	return tea.WindowSizeMsg{Width: w, Height: h}
}

func TestSessionPickerWindowFitsWhenShortList(t *testing.T) {
	p := NewSessionPicker(sampleSessions())
	p.Update(windowSize(80, 30))
	start, end := p.window()
	assert.Equal(t, 0, start)
	assert.Equal(t, len(p.sessions), end, "a short list should not scroll")
}

func TestSessionPickerScrollsToKeepSelectionVisible(t *testing.T) {
	p := NewSessionPicker(manySessions(50))
	// Small terminal: fewer rows than sessions, so the window must scroll.
	p.Update(windowSize(80, 12))

	rows := p.visibleRows()
	require.Less(t, rows, len(p.sessions))

	// Selecting the last session must bring it into the window.
	for i := 0; i < len(p.sessions)-1; i++ {
		p.Update(keyMsg("down"))
	}
	assert.Equal(t, len(p.sessions)-1, p.index)
	start, end := p.window()
	assert.LessOrEqual(t, start, p.index)
	assert.Greater(t, end, p.index, "selected row must be inside the rendered window")
	assert.Equal(t, rows, end-start)
	assert.Equal(t, len(p.sessions), end, "scrolled to the bottom edge")
}

func TestSessionPickerScrollsBackToTop(t *testing.T) {
	p := NewSessionPicker(manySessions(50))
	p.Update(windowSize(80, 12))
	for i := 0; i < len(p.sessions)-1; i++ {
		p.Update(keyMsg("down"))
	}
	for i := 0; i < len(p.sessions)-1; i++ {
		p.Update(keyMsg("up"))
	}
	start, _ := p.window()
	assert.Equal(t, 0, start, "back at the top edge")
	assert.Equal(t, 0, p.index)
}

func TestSessionPickerViewRendersOnlyWindow(t *testing.T) {
	p := NewSessionPicker(manySessions(50))
	p.Update(windowSize(80, 12))
	out := p.View()

	start, end := p.window()
	// Every rendered row is within the window...
	for i := start; i < end; i++ {
		assert.Contains(t, out, p.sessions[i].ID)
	}
	// ...and rows outside it are not rendered.
	for i := 0; i < len(p.sessions); i++ {
		if i < start || i >= end {
			assert.NotContains(t, out, p.sessions[i].ID,
				"off-window row %d must not be rendered", i)
		}
	}
	// The frame never exceeds the terminal height.
	assert.LessOrEqual(t, strings.Count(out, "\n"), p.height,
		"rendered frame must fit within the terminal height")
}

func TestSessionPickerFooterIndicatesScrollPosition(t *testing.T) {
	p := NewSessionPicker(manySessions(50))
	p.Update(windowSize(80, 12))
	out := p.View()
	assert.Contains(t, out, "共 50 个会话")
	assert.Contains(t, out, "↑↓")
	// Short list: no position range, just the total.
	q := NewSessionPicker(sampleSessions())
	q.Update(windowSize(80, 30))
	assert.Equal(t, "共 2 个会话", q.footerHint(q.window()))
}
