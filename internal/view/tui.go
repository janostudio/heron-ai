package view

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ===== Interfaces =====

// FlowRunner is the interface for running flows from the TUI
type FlowRunner interface {
	Run(ctx context.Context, input string) (*FlowResult, error)
}

// FlowResult is the result returned by the flow runner
type FlowResult struct {
	Teams     []TeamOutput
	Status    types.SessionStatus
	Usage     types.TokenUsage
	SessionID string
}

// TeamOutput is the display projection for one TeamTurn.
type TeamOutput struct {
	TeamID  string
	Reply   string
	Records []types.SharedRecord
	// Duration is how long the TeamTurn took (FinishedAt - StartedAt), shown in
	// the status bar so a slow turn is visible.
	Duration time.Duration
	// Calls is the per-call breakdown, ordered as the team ran them. The TUI
	// shows one message per call so the user can see which agent answered,
	// rather than only the team's aggregated reply.
	Calls []CallOutput
}

// CallOutput is one executed call inside a TeamTurn, projected for display.
type CallOutput struct {
	CallID  string
	AgentID string
	Reply   string
	// Ops summarizes the workspace operations this call performed (commands,
	// reads, writes). Rendered as one compact line each instead of letting raw
	// command output flood the transcript.
	Ops []OpOutput
}

// OpOutput is one workspace operation, projected for display.
type OpOutput struct {
	Kind     string
	Subject  string
	ExitCode int
	Failed   bool
}

// ===== Message Types =====

// MessageRole defines the role of a message in the TUI display
type MessageRole string

const (
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleAgent     MessageRole = "agent"
	RoleSystem    MessageRole = "system"
	RoleUsage     MessageRole = "usage"
)

// scrollLines is how many transcript lines one arrow-key press (or one wheel
// notch) scrolls.
const scrollLines = 3

// DisplayMessage is a message displayed in the TUI viewport
type DisplayMessage struct {
	Role       MessageRole
	CallID     string
	Content    string
	RoundNum   int
	Timestamp  time.Time
	AgentIndex int
	// AgentName / TeamName identify who produced a RoleAgent message, shown as
	// "team › agent". Empty for user/assistant/system messages.
	AgentName string
	TeamName  string
	// Ops are the workspace operations behind a RoleAgent message, shown as a
	// compact one-line-each summary so raw command output does not flood the
	// transcript.
	Ops []OpOutput
}

// ===== Slash Commands =====

// slashCommand represents an available slash command with its description
type slashCommand struct {
	Name        string
	Description string
}

// defaultCommands returns the list of all available slash commands
var defaultCommands = []slashCommand{
	{"/help", "Show help information"},
	{"/exit", "Exit TUI"},
	{"/clear", "Clear message list"},
	{"/model", "Show current model info"},
	{"/usage", "Show token usage summary"},
	{"/flow", "Show current flow config"},
	{"/agents", "List all agents"},
	{"/welcome", "Show the full welcome banner"},
}

// GetAvailableCommands returns the list of available slash commands
func GetAvailableCommands() []slashCommand {
	return defaultCommands
}

// ===== Styles =====

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	inputStyle = lipgloss.NewStyle().
			Padding(0, 1)

	// User turns read as a distinct block: a cyan ">" gutter and a dim
	// left-accented body, so a prompt is never confused with model output.
	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	userGutterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true)

	// Assistant turns use a green "⏺" gutter; the body itself is rendered as
	// Markdown (see renderMarkdown), which is the primary visual difference.
	assistantMsgStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Padding(0, 1)

	assistantGutterStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("42")).
				Bold(true)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true).
			Align(lipgloss.Center)

	usageMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Padding(0, 1)

	// Workspace-operation summary styles (the compact per-tool lines).
	opStyleKind = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	opStyleDim  = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	opStyleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	opStyleFail = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))

	// Round separator.
	separatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("238"))

	spinnerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("39"))

	slashDropdownStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Background(lipgloss.Color("237")).
				Padding(0, 1)

	slashHighlightStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("205")).
				Padding(0, 1)

	// Agent colors for multi-agent display
	agentHeaderStyles = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("201")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true),
	}
)

func agentStyle(index int) lipgloss.Style {
	if index < 0 || index >= len(agentHeaderStyles) {
		return agentHeaderStyles[0]
	}
	return agentHeaderStyles[index]
}

// ===== Welcome Banner =====
//
// The welcome card is drawn by lipgloss, not by hand: an earlier hard-coded
// ASCII-art banner mis-aligned its right border because the block-drawing
// glyphs (█ ╗ ═ …) are wider than their rune count suggests and manual space
// counting cannot keep a frame straight. lipgloss.Border measures the real
// display width, so the frame is correct by construction and adapts to the
// content.

var welcomeCardStyle = lipgloss.NewStyle().
	Border(lipgloss.RoundedBorder()).
	BorderForeground(lipgloss.Color("205")).
	Padding(0, 2)

var welcomeTitleStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.Color("205"))

var welcomeSubtitleStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("241"))

var welcomeKeyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("39"))

var welcomeHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("243"))


// ===== TUIModel =====

// TUIModel is the bubbletea model for the interactive TUI
type TUIModel struct {
	flowName   string
	modelName  string
	agentCount int
	teamCount  int

	viewport viewport.Model
	textarea textarea.Model
	spinner  spinner.Model

	runner FlowRunner
	ctx    context.Context

	messages   []DisplayMessage
	history    []string
	historyIdx int
	totalUsage types.TokenUsage
	roundNum   int
	sessionID  string

	// Status-bar extras, updated as turns complete.
	lastTeam     string
	lastDuration time.Duration

	running     bool
	ready       bool
	quitting    bool
	showWelcome bool

	// Slash command autocomplete
	slashMode bool
	slashCmds []slashCommand
	slashIdx  int

	width  int
	height int
}

// NewTUIModel creates a new TUI model
func NewTUIModel(flowName string, modelName string, agentCount int, teamCount int, runner FlowRunner) *TUIModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message... (/help for commands, Ctrl+C to quit)"
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.CharLimit = 4096
	ta.Focus()

	vp := viewport.New(80, 20)

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = spinnerStyle

	return &TUIModel{
		flowName:    flowName,
		modelName:   modelName,
		agentCount:  agentCount,
		teamCount:   teamCount,
		viewport:    vp,
		textarea:    ta,
		spinner:     sp,
		runner:      runner,
		ctx:         context.Background(),
		history:     []string{},
		historyIdx:  -1,
		showWelcome: true,
		slashCmds:   defaultCommands,
		slashIdx:    0,
	}
}

// Init initializes the model
func (m *TUIModel) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.spinner.Tick)
}

// Update handles messages and updates the model
func (m *TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

		headerHeight := 1
		statusHeight := 1
		inputHeight := 3
		contentHeight := m.height - headerHeight - statusHeight - inputHeight - 1

		if !m.ready {
			m.viewport = viewport.New(msg.Width-2, contentHeight)
			m.textarea.SetWidth(msg.Width - 4)
			m.ready = true

			if len(m.messages) > 0 {
				// History was seeded before the first size arrived (resume):
				// render it at the real width instead of the welcome banner.
				m.renderMessages()
			} else {
				m.viewport.SetContent(m.buildWelcome())
			}
			m.viewport.GotoBottom()
		} else {
			m.viewport.Width = msg.Width - 2
			m.viewport.Height = contentHeight
			m.textarea.SetWidth(msg.Width - 4)

			// Re-wrap the current content at the new width. viewport.SetContent
			// wraps to the width in effect when it is called; updating Width
			// alone leaves previously wrapped (wider) lines in place and the
			// narrower viewport then *clips* them, dropping the tail of long
			// lines. Re-running the wrap is what keeps a resize (or a terminal
			// narrower than the default 80) from truncating message text.
			if len(m.messages) > 0 {
				m.renderMessages()
			} else {
				m.viewport.SetContent(m.buildWelcome())
			}
			m.viewport.GotoBottom()
		}

	case tea.MouseMsg:
		// Wheel scrolls the transcript. Drag events arrive too (mouse tracking
		// is on so the wheel works) but are ignored: we do not implement our
		// own selection. Users select text with Shift/Option+drag, which the
		// terminal handles natively by bypassing the capture.
		// msg.Button, not the deprecated msg.Type: bubbletea fills both from
		// the same button code, and Button is what distinguishes a wheel event
		// (MouseButtonWheelUp/Down) from a click.
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.viewport.ScrollUp(scrollLines)
			return m, nil
		case tea.MouseButtonWheelDown:
			m.viewport.ScrollDown(scrollLines)
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		if tuiDebugEnabled() {
			tuiLogf("key=%s alt=%v slashMode=%v historyLen=%d historyIdx=%d line=%d lineCount=%d value=%q",
				msg.String(), msg.Alt, m.slashMode, len(m.history), m.historyIdx,
				m.textarea.Line(), m.textarea.LineCount(), m.textarea.Value())
		}
		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlL:
			m.clearMessages()
			return m, nil

		case tea.KeyCtrlP:
			// Alias for ↑ (readline-style). Both recall older input.
			m.recallHistory(1)
			return m, nil

		case tea.KeyCtrlN:
			// Alias for ↓. Both move toward newer input.
			m.recallHistory(-1)
			return m, nil

		case tea.KeyUp:
			if m.slashMode {
				if m.slashIdx > 0 {
					m.slashIdx--
				}
				return m, nil
			}
			if msg.Alt {
			// Alt+↑ scrolls the transcript (REPL-safe: plain ↑ is history).
			m.viewport.ScrollUp(scrollLines)
				return m, nil
			}
			// In a multi-line input, ↑ first moves the cursor up; only once it
			// is on the first line does it recall input history — the standard
			// readline / PSReadLine behaviour. A single-line input is always on
			// line 0, so ↑ recalls immediately.
			if m.textarea.Line() > 0 {
				break
			}
			m.recallHistory(1)
			return m, nil

		case tea.KeyDown:
			if m.slashMode {
				filtered := m.filterCommands()
				if len(filtered) > 0 && m.slashIdx < len(filtered)-1 {
					m.slashIdx++
				}
				return m, nil
			}
			if msg.Alt {
				m.viewport.ScrollDown(scrollLines)
				return m, nil
			}
			if m.textarea.Line() < m.textarea.LineCount()-1 {
				break
			}
			m.recallHistory(-1)
			return m, nil

		case tea.KeyHome:
			m.viewport.GotoTop()
			return m, nil

		case tea.KeyEnd:
			m.viewport.GotoBottom()
			return m, nil

		case tea.KeyTab:
			if m.slashMode {
				filtered := m.filterCommands()
				if len(filtered) > 0 {
					m.slashIdx = (m.slashIdx + 1) % len(filtered)
					m.textarea.SetValue(filtered[m.slashIdx].Name + " ")
				}
				return m, nil
			}

		case tea.KeyEnter:
			if m.running {
				return m, nil
			}

			// If in slash mode with active dropdown, select the highlighted command
			if m.slashMode {
				filtered := m.filterCommands()
				if len(filtered) > 0 && m.slashIdx >= 0 && m.slashIdx < len(filtered) {
					selected := filtered[m.slashIdx]
					m.slashMode = false
					m.slashIdx = 0
					m.textarea.SetValue("")
					cmdResult := m.handleCommand(selected.Name)
					if cmdResult.quit {
						m.quitting = true
						return m, tea.Quit
					}
					if cmdResult.message != "" {
						m.addMessage(DisplayMessage{Role: RoleSystem, Content: cmdResult.message, Timestamp: time.Now()})
					}
					m.viewport.GotoBottom()
					return m, nil
				}
			}

			input := strings.TrimSpace(m.textarea.Value())
			if input == "" {
				return m, nil
			}

			// Check for slash commands
			if strings.HasPrefix(input, "/") {
				m.slashMode = false
				m.slashIdx = 0
				cmdResult := m.handleCommand(input)
				m.textarea.SetValue("")
				if cmdResult.quit {
					m.quitting = true
					return m, tea.Quit
				}
				if cmdResult.message != "" {
					m.addMessage(DisplayMessage{Role: RoleSystem, Content: cmdResult.message, Timestamp: time.Now()})
				}
				m.viewport.GotoBottom()
				return m, nil
			}

			// Add to history (persisted, so it survives a restart).
			m.history = appendHistory(m.history, input)
			m.historyIdx = -1

			// A round is one user input (and everything it triggers), so the
			// counter advances here, not per agent reply. One input can wake
			// several teams; counting replies split a single input into many
			// "rounds", which contradicted the user's model.
			m.roundNum++

			// Add user message
			m.addMessage(DisplayMessage{Role: RoleUser, Content: input, RoundNum: m.roundNum, Timestamp: time.Now()})

			m.textarea.SetValue("")
			m.running = true
			return m, tea.Batch(m.spinner.Tick, m.runFlow(input))
		}

	case flowResultMsg:
		m.running = false
		if msg.err != nil {
			m.addMessage(DisplayMessage{Role: RoleSystem, Content: fmt.Sprintf("Error: %v", msg.err), Timestamp: time.Now()})
		} else if msg.result == nil {
			// A runner that returns no result and no error is a programming
			// error; surface it instead of dereferencing nil.
			m.addMessage(DisplayMessage{Role: RoleSystem, Content: "Error: flow runner returned no result", Timestamp: time.Now()})
		} else {
			if msg.result.SessionID != "" {
				m.sessionID = msg.result.SessionID
			}
			m.totalUsage.PromptTokens += msg.result.Usage.PromptTokens
			m.totalUsage.CompletionTokens += msg.result.Usage.CompletionTokens
			m.totalUsage.ReasoningTokens += msg.result.Usage.ReasoningTokens
			m.totalUsage.TotalTokens += msg.result.Usage.TotalTokens
			m.totalUsage.PromptCacheHitTokens += msg.result.Usage.PromptCacheHitTokens
			m.totalUsage.PromptCacheMissTokens += msg.result.Usage.PromptCacheMissTokens
			m.totalUsage.CacheReadInputTokens += msg.result.Usage.CacheReadInputTokens
			m.totalUsage.CacheCreationInputTokens += msg.result.Usage.CacheCreationInputTokens
			// roundNum is NOT advanced here: a round is one user input, and one
			// input can produce several flowResultMsg (one per team that runs).
			// It advances on submit instead.
			// Status-bar extras: the last team to run and its wall time.
			if n := len(msg.result.Teams); n > 0 {
				m.lastTeam = msg.result.Teams[n-1].TeamID
				var total time.Duration
				for _, team := range msg.result.Teams {
					total += team.Duration
				}
				m.lastDuration = total
			}

			for i, team := range msg.result.Teams {
				if len(team.Calls) > 0 {
					// One message per call so each reply carries its own
					// "team › agent" identity. The team's aggregated Reply is
					// just these joined together, so showing the parts loses
					// nothing and gains attribution.
					for _, call := range team.Calls {
						m.addMessage(DisplayMessage{
							Role:       RoleAgent,
							CallID:     call.CallID,
							Content:    call.Reply,
							RoundNum:   m.roundNum,
							Timestamp:  time.Now(),
							AgentIndex: i,
							AgentName:  call.AgentID,
							TeamName:   team.TeamID,
							Ops:        call.Ops,
						})
					}
					continue
				}
				m.addMessage(DisplayMessage{
					Role:       RoleAgent,
					CallID:     team.TeamID,
					Content:    team.Reply,
					RoundNum:   m.roundNum,
					Timestamp:  time.Now(),
					AgentIndex: i,
					TeamName:   team.TeamID,
				})
			}

			if msg.result.Usage.TotalTokens > 0 {
				// One round can yield several of these (one per team), so the
				// label says "step", not "Round N" — the round number alone
				// would repeat and misread as progress.
				m.addMessage(DisplayMessage{
					Role:      RoleUsage,
					Content:   fmt.Sprintf("Round %d · %d tokens this step (Total: %d)", m.roundNum, msg.result.Usage.TotalTokens, m.totalUsage.TotalTokens),
					RoundNum:  m.roundNum,
					Timestamp: time.Now(),
				})
			}
		}
		m.viewport.GotoBottom()
		return m, m.spinner.Tick

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	// Update textarea and track slash mode
	oldValue := m.textarea.Value()
	var taCmd tea.Cmd
	m.textarea, taCmd = m.textarea.Update(msg)
	cmds = append(cmds, taCmd)

	newValue := m.textarea.Value()
	m.updateSlashMode(newValue, oldValue)

	// Update viewport
	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

// updateSlashMode checks if slash mode should be activated/deactivated
func (m *TUIModel) updateSlashMode(newValue, oldValue string) {
	if strings.HasPrefix(newValue, "/") {
		m.slashMode = true
		// Reset index when filter changes
		if oldValue != newValue {
			m.slashIdx = 0
		}
	} else {
		m.slashMode = false
		m.slashIdx = 0
	}
}

// filterCommands returns slash commands matching the current input
func (m *TUIModel) filterCommands() []slashCommand {
	input := m.textarea.Value()
	if !strings.HasPrefix(input, "/") {
		return nil
	}

	query := strings.ToLower(strings.TrimPrefix(input, "/"))
	if query == "" {
		return m.slashCmds
	}

	var filtered []slashCommand
	for _, cmd := range m.slashCmds {
		if strings.Contains(strings.ToLower(cmd.Name), query) ||
			strings.Contains(strings.ToLower(cmd.Description), query) {
			filtered = append(filtered, cmd)
		}
	}
	return filtered
}

// View renders the TUI
func (m *TUIModel) View() string {
	if m.quitting {
		if m.sessionID != "" {
			return fmt.Sprintf("Goodbye!  Resume with: heron --session %s\n", m.sessionID)
		}
		return "Goodbye!\n"
	}
	if !m.ready {
		return "Initializing...\n"
	}

	// Header
	header := titleStyle.Render(fmt.Sprintf("Heron AI - %s | Tokens: %d", m.flowName, m.totalUsage.TotalTokens))
	header = lipgloss.NewStyle().Width(m.width).Render(header)

	// Content area (viewport)
	content := m.viewport.View()

	// Status bar
	// Status bar: model, last team, round, elapsed, tokens on the left; key
	// hints on the right.
	statusLeft := fmt.Sprintf("Model: %s", m.modelName)
	if m.lastTeam != "" {
		statusLeft += fmt.Sprintf(" | Team: %s", m.lastTeam)
	}
	statusLeft += fmt.Sprintf(" | Round: %d", m.roundNum)
	if m.lastDuration > 0 {
		statusLeft += fmt.Sprintf(" | %s", formatDuration(m.lastDuration))
	}
	if m.totalUsage.TotalTokens > 0 {
		statusLeft += fmt.Sprintf(" | %s tok", formatTokens(int64(m.totalUsage.TotalTokens)))
	}
	if m.running {
		statusLeft = m.spinner.View() + " " + statusLeft
	}
	statusRight := "↑↓ history | Ctrl+C: quit"
	statusLeftWidth := lipgloss.Width(statusLeft)
	statusRightWidth := lipgloss.Width(statusRight)
	middleWidth := m.width - statusLeftWidth - statusRightWidth
	if middleWidth < 0 {
		middleWidth = 0
	}
	status := statusBarStyle.Width(m.width).Render(
		lipgloss.JoinHorizontal(lipgloss.Left, statusLeft,
			lipgloss.NewStyle().Width(middleWidth).Render(""),
			statusRight),
	)

	// Input area
	input := inputStyle.Render(m.textarea.View())

	// Slash command dropdown
	var slashDropdown string
	if m.slashMode {
		filtered := m.filterCommands()
		if len(filtered) > 0 {
			var ddLines []string
			for i, cmd := range filtered {
				line := fmt.Sprintf("  %-12s  %s", cmd.Name, cmd.Description)
				if i == m.slashIdx {
					ddLines = append(ddLines, slashHighlightStyle.Render(line))
				} else {
					ddLines = append(ddLines, slashDropdownStyle.Render(line))
				}
			}
			slashDropdown = lipgloss.JoinVertical(lipgloss.Top, ddLines...)
		}
	}

	if slashDropdown != "" {
		return lipgloss.JoinVertical(lipgloss.Top,
			header,
			content,
			status,
			input,
			slashDropdown,
		)
	}

	return lipgloss.JoinVertical(lipgloss.Top,
		header,
		content,
		status,
		input,
	)
}

func (m *TUIModel) buildWelcome() string {
	title := welcomeTitleStyle.Render("Heron AI")
	subtitle := welcomeSubtitleStyle.Render("Multi-Agent Generic Engine")

	info := fmt.Sprintf("%s  %s  %s  %s",
		welcomeKeyStyle.Render("Flow:")+" "+m.flowName,
		welcomeKeyStyle.Render("Model:")+" "+m.modelName,
		welcomeKeyStyle.Render("Agents:")+" "+fmt.Sprintf("%d", m.agentCount),
		welcomeKeyStyle.Render("Teams:")+" "+fmt.Sprintf("%d", m.teamCount),
	)

	hints := strings.Join([]string{
		welcomeKeyStyle.Render("/help") + welcomeHintStyle.Render(" commands") +
			welcomeHintStyle.Render("   ") +
			welcomeKeyStyle.Render("↑↓") + welcomeHintStyle.Render(" history") +
			welcomeHintStyle.Render("   ") +
			welcomeKeyStyle.Render("Enter") + welcomeHintStyle.Render(" send") +
			welcomeHintStyle.Render("   ") +
			welcomeKeyStyle.Render("Ctrl+C") + welcomeHintStyle.Render(" exit"),
		welcomeHintStyle.Render("To copy text, hold Shift (⌥ on some terminals) and drag with the mouse."),
	}, "\n")

	// Constrain the card to the viewport so a narrow terminal wraps instead of
	// clipping, and a very wide one does not sprawl.
	cardStyle := welcomeCardStyle
	if w := m.viewport.Width; w > 0 {
		inner := w - cardStyle.GetHorizontalFrameSize()
		if inner < 20 {
			inner = 20
		}
		if inner > 72 {
			inner = 72
		}
		cardStyle = cardStyle.Width(inner)
	}

	card := cardStyle.Render(strings.Join([]string{
		title,
		subtitle,
		"",
		info,
		"",
		hints,
	}, "\n"))

	// A blank line so the card sits a little below the header.
	return "\n" + card
}

func (m *TUIModel) addMessage(msg DisplayMessage) {
	m.messages = append(m.messages, msg)
	m.renderMessages()
}

// recallHistory moves through the input history. dir +1 recalls an older entry
// (↑ / Ctrl+P), dir -1 moves toward the newest (↓ / Ctrl+N, then clears).
func (m *TUIModel) recallHistory(dir int) {
	if len(m.history) == 0 {
		tuiLogf("recall dir=%d -> no history", dir)
		return
	}
	if m.historyIdx == -1 {
		// Not browsing yet: start from the most recent entry.
		if dir < 0 {
			tuiLogf("recall dir=%d -> already newest, nothing to do", dir)
			return
		}
		m.historyIdx = 0
	} else {
		next := m.historyIdx + dir
		if next < 0 {
			// Walked past the newest entry: clear the input.
			m.historyIdx = -1
			m.textarea.SetValue("")
			tuiLogf("recall dir=%d -> past newest, cleared", dir)
			return
		}
		if next >= len(m.history) {
			tuiLogf("recall dir=%d -> at oldest", dir)
			return // already at the oldest entry
		}
		m.historyIdx = next
	}
	value := m.history[len(m.history)-1-m.historyIdx]
	m.textarea.SetValue(value)
	tuiLogf("recall dir=%d -> idx=%d value=%q", dir, m.historyIdx, value)
}

func (m *TUIModel) clearMessages() {
	m.messages = nil
	m.renderMessages()
}

func (m *TUIModel) renderMessages() {
	var lines []string

	// bubbles/viewport v1.0.0 does not soft-wrap: SetContent splits on '\n'
	// only and visibleLines() *cuts* any line wider than the viewport. A long
	// message rendered as a single line is therefore hard-truncated at the
	// viewport width. Give every styled line an explicit width so lipgloss
	// wraps it to fit before it reaches the viewport.
	width := m.viewport.Width
	if width <= 0 {
		width = 80
	}
	// Styles carry Padding(0, 1) (two columns); systemMsgStyle carries none.
	paddedWidth := width - 2
	if paddedWidth < 1 {
		paddedWidth = 1
	}

	// Gutter is "> " / "⏺ " (2 columns). Body styles carry Padding(0, 1), so
	// the body gets width minus gutter minus its own padding.
	gutterWidth := 2
	bodyWidth := paddedWidth - gutterWidth
	if bodyWidth < 1 {
		bodyWidth = 1
	}

	prevRound := 0
	for _, msg := range m.messages {
		// Round boundary: a light separator with the round number.
		if msg.RoundNum > 0 && msg.RoundNum != prevRound {
			if prevRound != 0 {
				lines = append(lines, renderRoundSeparator(msg.RoundNum, width))
			}
			prevRound = msg.RoundNum
		}

		switch msg.Role {
		case RoleUser:
			lines = append(lines, renderGutterMessage(
				userGutterStyle.Render(">"),
				userMsgStyle.Width(bodyWidth).Render(msg.Content),
			))
		case RoleAssistant:
			lines = append(lines, renderGutterMessage(
				assistantGutterStyle.Render("⏺"),
				assistantMsgStyle.Width(bodyWidth).Render(renderMarkdown(msg.Content, bodyWidth-2)),
			))
		case RoleAgent:
			// Header identifies who spoke: "team › agent", falling back to
			// whichever of the two is known, then to the call id.
			header := agentHeader(msg)
			lines = append(lines, agentStyle(msg.AgentIndex).Width(paddedWidth).Render(header))
			// Compact one-line-per-op summary (tool calls, commands) instead of
			// letting raw output flood the body.
			for _, op := range msg.Ops {
				lines = append(lines, renderOpLine(op, bodyWidth))
			}
			if strings.TrimSpace(msg.Content) != "" {
				lines = append(lines, renderGutterMessage(
					assistantGutterStyle.Render("⏺"),
					assistantMsgStyle.Width(bodyWidth).Render(renderMarkdown(msg.Content, bodyWidth-2)),
				))
			}
		case RoleSystem:
			lines = append(lines, systemMsgStyle.Width(width).Render(msg.Content))
		case RoleUsage:
			lines = append(lines, usageMsgStyle.Width(paddedWidth).Render(msg.Content))
		}
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
}

// formatDuration renders a turn's wall time compactly (e.g. 120ms / 1.2s / 2m03s).
func formatDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// renderRoundSeparator draws the light rule shown between rounds, labeled with
// the round number so a long transcript has visible structure.
func renderRoundSeparator(round, width int) string {
	label := fmt.Sprintf(" Round %d ", round)
	ruleWidth := maxInt(width-lipgloss.Width(label), 2)
	left := ruleWidth / 2
	right := ruleWidth - left
	line := strings.Repeat("─", maxInt(left, 1)) + label + strings.Repeat("─", maxInt(right, 1))
	return separatorStyle.Width(width).Render(ansi.Truncate(line, maxInt(width, 1), ""))
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// agentHeader renders the speaker label for a RoleAgent message:
// "team › agent", degrading gracefully when only one side is known.
func agentHeader(msg DisplayMessage) string {
	switch {
	case msg.TeamName != "" && msg.AgentName != "":
		return fmt.Sprintf("%s › %s", msg.TeamName, msg.AgentName)
	case msg.AgentName != "":
		return msg.AgentName
	case msg.TeamName != "":
		return msg.TeamName
	default:
		return fmt.Sprintf("[%s]", msg.CallID)
	}
}

// renderOpLine renders one workspace operation as a compact line:
//
//	⏺ Bash  go test ./...  ✓
//	⏺ Bash  grep -rn foo .  ✗ exit 1
//
// The subject is truncated to the available width so a long command cannot
// push the line past the viewport.
func renderOpLine(op OpOutput, width int) string {
	mark := opStyleOK.Render("✓")
	if op.Failed {
		mark = opStyleFail.Render(fmt.Sprintf("✗ exit %d", op.ExitCode))
	}
	kind := op.Kind
	if kind == "" {
		kind = "op"
	}
	subject := strings.TrimSpace(op.Subject)

	// Budget for the subject: width minus the indent, the kind label, the two
	// gaps, and the trailing mark.
	fixed := 2 + lipgloss.Width(opStyleKind.Render(kind)) + 2 + lipgloss.Width(mark)
	avail := width - fixed
	if avail < 4 {
		avail = 4
	}
	if lipgloss.Width(subject) > avail {
		subject = ansi.Truncate(subject, avail-1, "…")
	}

	line := fmt.Sprintf("  %s %s  %s", opStyleKind.Render(kind), opStyleDim.Render(subject), mark)
	return lipgloss.NewStyle().Width(width).Render(line)
}

// renderGutterMessage puts a fixed-width gutter in the first column and the
// (possibly multi-line) body beside it, so continuation lines stay indented
// under the message text rather than wrapping back under the gutter.
func renderGutterMessage(gutter, body string) string {
	return lipgloss.JoinHorizontal(lipgloss.Top,
		lipgloss.NewStyle().Width(2).Render(gutter),
		" ",
		body,
	)
}

type commandResult struct {
	message string
	quit    bool
}

func (m *TUIModel) handleCommand(input string) commandResult {
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return commandResult{}
	}

	cmd := parts[0]
	switch cmd {
	case "/exit", "/quit":
		return commandResult{quit: true}

	case "/help":
		return commandResult{message: `Available commands:
  /help    - Show this help
  /exit    - Exit TUI
  /clear   - Clear message list
  /model   - Show current model info
  /usage   - Show token usage summary
  /flow    - Show current flow config
  /agents  - List all agents
  /welcome - Show full welcome banner

Keyboard shortcuts:
  Enter        - Send message
  Up/Down      - Previous / next input history (Ctrl+P/Ctrl+N also work)
  Alt+Up/Down  - Scroll transcript one line
  Home/End     - Jump to top / bottom
  Ctrl+L       - Clear screen
  Ctrl+C       - Exit

Mouse:
  Wheel         - Scroll transcript
  Shift/Option  - Hold while dragging to select text, then copy as usual
                  (the TUI captures the mouse for wheel scrolling, so a plain
                  drag does not select)`}

	case "/clear":
		m.clearMessages()
		return commandResult{message: "Messages cleared."}

	case "/model":
		return commandResult{message: fmt.Sprintf("Model: %s", m.modelName)}

	case "/usage":
		return commandResult{message: fmt.Sprintf("Token usage - Prompt: %d, Completion: %d, Total: %d",
			m.totalUsage.PromptTokens, m.totalUsage.CompletionTokens, m.totalUsage.TotalTokens)}

	case "/flow":
		return commandResult{message: fmt.Sprintf("Current flow: %s", m.flowName)}

	case "/agents":
		return commandResult{message: fmt.Sprintf("Agents: %d, Teams: %d", m.agentCount, m.teamCount)}

	case "/welcome":
		m.showWelcome = true
		m.clearMessages()
		welcome := m.buildWelcome()
		m.viewport.SetContent(welcome)
		return commandResult{message: "Welcome banner restored!"}

	default:
		return commandResult{message: fmt.Sprintf("Unknown command: %s. Type /help for available commands.", cmd)}
	}
}

type flowResultMsg struct {
	result *FlowResult
	err    error
}

func (m *TUIModel) runFlow(input string) tea.Cmd {
	return func() tea.Msg {
		result, err := m.runner.Run(m.ctx, input)
		return flowResultMsg{result: result, err: err}
	}
}

// ===== Public Test Helpers =====

// GetMessages returns the current message list
func (m *TUIModel) GetMessages() []DisplayMessage { return m.messages }

// SetSessionID seeds the session id shown on exit. It is used when the TUI is
// launched directly into an existing session (heron --list → pick → resume),
// where no flowResultMsg has run yet to record the id.
func (m *TUIModel) SetSessionID(id string) { m.sessionID = id }

// LoadHistory loads persisted input history into the model. It is called by the
// entry point (not the constructor) so that constructing a model has no
// filesystem side effects — tests build models freely.
func (m *TUIModel) LoadHistory() {
	m.history = loadHistory()
	m.historyIdx = -1
}

// HistoryTurn is one prior user input and the replies it produced, preloaded
// into the viewport when a session is resumed so the user sees the conversation
// the model already remembers — with each reply's team/agent identity intact.
type HistoryTurn struct {
	Input    string
	Messages []HistoryMessage
	Usage    types.TokenUsage
}

// HistoryMessage is one reply inside a HistoryTurn, carrying its origin.
type HistoryMessage struct {
	TeamID  string
	CallID  string
	AgentID string
	Reply   string
	Ops     []OpOutput
}

// historyMessages projects a turn's replies into RoleAgent messages carrying
// their team/agent identity, matching how live replies are rendered.
func historyMessages(msgs []HistoryMessage, round int) []DisplayMessage {
	out := make([]DisplayMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, DisplayMessage{
			Role:      RoleAgent,
			CallID:    m.CallID,
			Content:   m.Reply,
			RoundNum:  round,
			Timestamp: time.Now(),
			AgentName: m.AgentID,
			TeamName:  m.TeamID,
			Ops:       m.Ops,
		})
	}
	return out
}

// SeedHistory replaces the welcome banner with a session's prior turns and
// scrolls to the bottom. It is a no-op for an empty history, so a fresh session
// keeps the welcome screen.
func (m *TUIModel) SeedHistory(turns []HistoryTurn) {
	if len(turns) == 0 {
		return
	}
	m.showWelcome = false
	m.messages = nil
	for i, turn := range turns {
		round := i + 1
		if strings.TrimSpace(turn.Input) != "" {
			m.messages = append(m.messages, DisplayMessage{
				Role:      RoleUser,
				Content:   turn.Input,
				RoundNum:  round,
				Timestamp: time.Now(),
			})
		}
		m.messages = append(m.messages, historyMessages(turn.Messages, round)...)
		// Restore the session's historical token spend so the header reads as a
		// session total, not just this run.
		m.totalUsage.PromptTokens += turn.Usage.PromptTokens
		m.totalUsage.CompletionTokens += turn.Usage.CompletionTokens
		m.totalUsage.ReasoningTokens += turn.Usage.ReasoningTokens
		m.totalUsage.TotalTokens += turn.Usage.TotalTokens
		m.totalUsage.PromptCacheHitTokens += turn.Usage.PromptCacheHitTokens
		m.totalUsage.PromptCacheMissTokens += turn.Usage.PromptCacheMissTokens
		m.totalUsage.CacheReadInputTokens += turn.Usage.CacheReadInputTokens
		m.totalUsage.CacheCreationInputTokens += turn.Usage.CacheCreationInputTokens
	}
	m.roundNum = len(turns)
	if m.ready {
		m.renderMessages()
		m.viewport.GotoBottom()
	}
}

// GetSlashCommands returns the available slash commands
func (m *TUIModel) GetSlashCommands() []slashCommand { return m.slashCmds }

// IsSlashMode returns whether slash autocomplete mode is active
func (m *TUIModel) IsSlashMode() bool { return m.slashMode }

// GetSlashIdx returns the currently selected index in the slash dropdown
func (m *TUIModel) GetSlashIdx() int { return m.slashIdx }

// GetTotalUsage returns the accumulated token usage
func (m *TUIModel) GetTotalUsage() types.TokenUsage { return m.totalUsage }

// IsRunning returns whether a flow is currently running
func (m *TUIModel) IsRunning() bool { return m.running }

// IsShowWelcome returns whether the welcome banner is currently shown
func (m *TUIModel) IsShowWelcome() bool { return m.showWelcome }

// GetHistory returns the input history
func (m *TUIModel) GetHistory() []string { return m.history }
