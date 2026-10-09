package view

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRenderMessagesDistinguishesUserAndAssistant covers the visual contract:
// a user turn leads with a ">" gutter, an assistant turn with "⏺", and the
// assistant body is Markdown-rendered while the user body is shown verbatim.
func TestRenderMessagesDistinguishesUserAndAssistant(t *testing.T) {
	model := NewTUIModel("test", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})

	model.addMessage(DisplayMessage{Role: RoleUser, Content: "plain user line"})
	model.addMessage(DisplayMessage{Role: RoleAssistant, Content: "**bold** answer"})

	out := stripANSI(model.viewport.View())

	userLine := lineContaining(out, "plain user line")
	assistantLine := lineContaining(out, "bold")
	require.NotEmpty(t, userLine, "user line must be present")
	require.NotEmpty(t, assistantLine, "assistant line must be present")

	assert.True(t, strings.HasPrefix(strings.TrimLeft(userLine, " "), ">"),
		"user line must use the > gutter, got %q", userLine)
	assert.Contains(t, assistantLine, "⏺",
		"assistant line must use the ⏺ gutter, got %q", assistantLine)
	// Markdown consumed the ** markers (bold styling applied, markers gone).
	assert.NotContains(t, assistantLine, "**", "assistant body should be markdown-rendered")
	assert.Contains(t, assistantLine, "bold answer")
}

func TestRenderMarkdownPreservesPlainText(t *testing.T) {
	// No markdown markers: the text should survive intact.
	out := stripANSI(renderMarkdown("just a sentence", 60))
	assert.Contains(t, out, "just a sentence")
}

func TestRenderMarkdownNarrowFallsBackToRaw(t *testing.T) {
	// Below the minimum width glamour is skipped; raw text passes through.
	out := renderMarkdown("raw text", 5)
	assert.Equal(t, "raw text", out)
}

func lineContaining(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// TestTrimLineTrailingPadding is the regression for the escape-splicing bug:
// glamour pads each line with ANSI-wrapped spaces, and naive truncation by
// visible width sliced a sequence in half, leaking fragments like "8;5;252m".
func TestTrimLineTrailingPadding(t *testing.T) {
	line := "\x1b[38;5;252m  建议：补上。\x1b[0m\x1b[38;5;252m \x1b[0m\x1b[38;5;252m \x1b[0m"
	out := trimLineTrailingPadding(line)

	// No partial escape fragments survive.
	assert.NotContains(t, out, "8;5;252m\x1b[0m\n")
	assert.Equal(t, "  建议：补上。", ansi.Strip(out), "visible text is preserved, padding dropped")
	// Interior styling on the kept text is intact (leading color code kept),
	// and a reset closes the line so it cannot bleed into the next.
	assert.Contains(t, out, "\x1b[38;5;252m")
	assert.True(t, strings.HasSuffix(out, "\x1b[0m"))
}

func TestTrimLineTrailingPaddingNoTrailingSpace(t *testing.T) {
	line := "\x1b[1mbold text\x1b[0m"
	out := trimLineTrailingPadding(line)
	assert.Equal(t, "bold text", ansi.Strip(out), "trailing padding is not the only thing kept")
	assert.True(t, strings.HasSuffix(out, "\x1b[0m"), "line is closed with a reset")
}

