package view

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/heron-ai/heron-engine/internal/sessionlist"
)

// ===== Styles =====

var (
	sessionTitleStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("205")).
				Padding(0, 1)

	sessionRowStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	sessionRowSelectedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("205")).
				Padding(0, 1)

	sessionHintStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				Padding(0, 1)
)

// SessionPicker is a bubbletea model that lets the user choose a session to
// resume. It is intentionally independent of TUIModel: the picker only
// selects; cmd/server decides what to do with the chosen id.
type SessionPicker struct {
	sessions []sessionlist.Session
	index    int
	chosen   string
	quit     bool
	width    int
	height   int
}

// NewSessionPicker builds a picker over the given sessions.
func NewSessionPicker(sessions []sessionlist.Session) *SessionPicker {
	return &SessionPicker{sessions: sessions}
}

// Chosen returns the selected session id, or "" if the user cancelled.
func (p *SessionPicker) Chosen() string { return p.chosen }

// Init implements tea.Model.
func (p *SessionPicker) Init() tea.Cmd { return nil }

// Update implements tea.Model.
func (p *SessionPicker) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		p.width = msg.Width
		p.height = msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q":
			p.quit = true
			return p, tea.Quit
		case "up", "k":
			if p.index > 0 {
				p.index--
			}
			return p, nil
		case "down", "j":
			if p.index < len(p.sessions)-1 {
				p.index++
			}
			return p, nil
		case "enter":
			if p.index >= 0 && p.index < len(p.sessions) {
				p.chosen = p.sessions[p.index].ID
			}
			p.quit = true
			return p, tea.Quit
		}
	}
	return p, nil
}

// View implements tea.Model.
func (p *SessionPicker) View() string {
	// After quitting, print nothing beyond a newline: the caller re-launches
	// the TUI (or exits) and any leftover full-screen frame would be noise.
	if p.quit {
		return ""
	}

	var b strings.Builder
	b.WriteString(sessionTitleStyle.Render("会话列表（↑↓ 选择，回车进入，q 退出）"))
	b.WriteString("\n\n")

	for i, s := range p.sessions {
		line := FormatSessionRow(s)
		if i == p.index {
			b.WriteString(sessionRowSelectedStyle.Render(line))
		} else {
			b.WriteString(sessionRowStyle.Render(line))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(sessionHintStyle.Render(fmt.Sprintf("共 %d 个会话", len(p.sessions))))
	b.WriteString("\n")
	return b.String()
}

// FormatSessionRow renders one session as a single display line. It is shared
// by the interactive picker and the non-interactive text view so both show the
// same information.
func FormatSessionRow(s sessionlist.Session) string {
	marker := "○"
	if s.Active {
		marker = "●"
	}
	title := s.Title
	if title == "" {
		title = "(无标题)"
	}
	return fmt.Sprintf("%s %-22s  %s  %d 轮  %s tok  %s",
		marker,
		s.ID,
		s.UpdatedAt.Local().Format("2006-01-02 15:04"),
		s.Turns,
		formatTokens(s.Tokens),
		title,
	)
}

// formatTokens renders a token count compactly (e.g. 59340 -> "59.3k").
func formatTokens(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// RenderSessionsText writes the plain-text session list used when stdout or
// stdin is not a terminal. It carries no ANSI styling so `heron --list | grep`
// behaves.
func RenderSessionsText(w io.Writer, sessions []sessionlist.Session) error {
	if len(sessions) == 0 {
		_, err := fmt.Fprintln(w, "暂无会话")
		return err
	}
	var errs []error
	write := func(format string, args ...any) {
		if _, err := fmt.Fprintf(w, format, args...); err != nil {
			errs = append(errs, err)
		}
	}
	for _, s := range sessions {
		write("%s\n", FormatSessionRow(s))
	}
	write("\n共 %d 个会话\n", len(sessions))
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}
