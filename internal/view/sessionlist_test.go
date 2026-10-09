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
