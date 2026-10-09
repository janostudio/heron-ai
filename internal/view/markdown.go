package view

import (
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/x/ansi"
)

// renderMarkdown renders assistant text as styled Markdown (code blocks, lists,
// bold, quotes) at the given content width, so an assistant reply reads
// differently from a plain user line. Rendering failures fall back to the raw
// text — a broken style must never hide the model's answer.
//
// The renderer is constructed per call: glamour's renderer is cheap and caches
// internally, and a per-call style keeps the width in lockstep with the current
// viewport without holding mutable state.
func renderMarkdown(text string, width int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if width < 20 {
		// Too narrow for glamour's block padding to look right; show the raw
		// text and let the viewport wrap it.
		return text
	}
	// A fixed dark style (not WithAutoStyle) keeps the output identical
	// whether or not stdout is a tty: WithAutoStyle degrades to a plain
	// "notty" style off-terminal that leaves the ** and ## markers visible.
	renderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithPreservedNewLines(),
	)
	if err != nil {
		return text
	}
	out, err := renderer.Render(text)
	if err != nil {
		return text
	}
	return hardWrapLongLines(trimMarkdownPadding(out), width)
}

// hardWrapLongLines breaks any line wider than width into multiple lines.
//
// glamour's WithWordWrap only breaks at spaces, so a long run with no spaces —
// a slash-separated team list, a file path, a URL, a hash — stays on one line
// well past the width. The viewport then hard-cuts it (bubbles/viewport v1.0.0
// does not soft-wrap), so the tail was invisible. ansi.Hardwrap breaks by
// display columns and is ANSI-aware, so styling survives the break.
func hardWrapLongLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if ansi.StringWidth(line) <= width {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(ansi.Hardwrap(line, width, true), "\n")...)
	}
	return strings.Join(out, "\n")
}

// trimMarkdownPadding drops the blank edge lines and the per-line trailing
// spaces glamour emits to pad every line to the wrap width. Left in place,
// that trailing padding inflates the viewport's line width and fights the
// gutter layout.
//
// The padding is wrapped in its own ANSI sequences, so the line does not end in
// a literal space and a plain TrimRight cannot see it — and truncating by
// visible width can slice a sequence in half. This walks the line as a sequence
// of tokens (escape sequence or rune), drops trailing whitespace runes together
// with the escape sequences that styled only them, and keeps everything else.
func trimMarkdownPadding(s string) string {
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = trimLineTrailingPadding(line)
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[0])) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(ansi.Strip(lines[len(lines)-1])) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// trimLineTrailingPadding removes trailing spaces and tabs from one line along
// with any escape sequences that appear after the last visible non-space rune.
// It does not touch interior styling.
func trimLineTrailingPadding(line string) string {
	// Track byte offsets of the end of the last visible non-space rune, and of
	// every escape sequence that started before it (so we can keep the whole
	// sequence and drop only trailing padding + the sequences after it).
	lastVisibleEnd := -1
	i := 0
	for i < len(line) {
		if seqLen := escapeSequenceLen(line[i:]); seqLen > 0 {
			i += seqLen
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if r != ' ' && r != '\t' {
			lastVisibleEnd = i + size
		}
		i += size
	}
	if lastVisibleEnd < 0 {
		return strings.TrimRight(ansi.Strip(line), " \t")
	}
	// Append a reset when the kept prefix left any style open, so styling from
	// this line cannot bleed into the next one once lines are joined.
	kept := line[:lastVisibleEnd]
	if strings.ContainsRune(kept, 0x1b) {
		kept += "\x1b[0m"
	}
	return kept
}

// escapeSequenceLen returns the byte length of an ANSI escape sequence at the
// start of s, or 0 if s does not begin with one. It handles the two forms the
// renderers emit: CSI (ESC [ … final-byte) and OSC (ESC ] … BEL or ST).
func escapeSequenceLen(s string) int {
	if len(s) == 0 || s[0] != 0x1b {
		return 0
	}
	if len(s) < 2 {
		return 0
	}
	switch s[1] {
	case '[': // CSI: params then a final byte in 0x40–0x7E
		for i := 2; i < len(s); i++ {
			if s[i] >= 0x40 && s[i] <= 0x7e {
				return i + 1
			}
		}
		return len(s)
	case ']': // OSC: terminated by BEL (0x07) or ST (ESC \)
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	default:
		return 2
	}
}


