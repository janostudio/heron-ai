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

	"github.com/heron-ai/heron-engine/pkg/types"
)

// ===== Interfaces =====

// FlowRunner is the interface for running flows from the TUI
type FlowRunner interface {
	Run(ctx context.Context, input string) (*FlowResult, error)
}

// FlowResult is the result returned by the flow runner
type FlowResult struct {
	Stages []StageOutput
	Signal types.Signal
	Usage  types.TokenUsage
}

// StageOutput represents a single stage output for the TUI display
type StageOutput struct {
	StageName string
	TeamName  string
	Content   string
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

// DisplayMessage is a message displayed in the TUI viewport
type DisplayMessage struct {
	Role      MessageRole
	AgentName string
	Content   string
	RoundNum  int
	Timestamp time.Time
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

	userMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	assistantMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("255")).
			Padding(0, 1)

	systemMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true).
			Align(lipgloss.Center)

	usageMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			Padding(0, 1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

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
	agentColors = []lipgloss.Color{
		lipgloss.Color("39"),  // blue
		lipgloss.Color("42"),  // green
		lipgloss.Color("220"), // yellow
		lipgloss.Color("201"), // magenta
		lipgloss.Color("51"),  // cyan
	}

	agentHeaderStyles = []lipgloss.Style{
		lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("201")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("51")).Bold(true),
	}
)

func agentStyle(index int) lipgloss.Style {
	if index < len(agentHeaderStyles) {
		return agentHeaderStyles[index]
	}
	return agentHeaderStyles[0]
}

// ===== Welcome Banner =====

const welcomeBanner = `
╔══════════════════════════════════════════════╗
║                                              ║
║     ██╗  ██╗███████╗██████╗  ██████╗ ███╗  ██╗  ║
║     ██║  ██║██╔════╝██╔══██╗██╔═══██╗████╗ ██║  ║
║     ███████║█████╗  ██████╔╝██║   ██║██╔██╗██║  ║
║     ██╔══██║██╔══╝  ██╔══██╗██║   ██║██║╚████║  ║
║     ██║  ██║███████╗██║  ██║╚██████╔╝██║ ╚███║  ║
║     ╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝ ╚═════╝ ╚═╝  ╚══╝  ║
║                                              ║
║       Multi-Agent Generic Engine             ║
║                                              ║
║   Type /help for commands   Ctrl+C to exit   ║
║   Enter to send             Up/Down history  ║
║                                              ║
╚══════════════════════════════════════════════╝
`

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
	err    error
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
		history:     make([]string, 0, 100),
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

			// Build welcome content
			welcome := m.buildWelcome()
			m.viewport.SetContent(welcome)
			m.viewport.GotoBottom()
		} else {
			m.viewport.Width = msg.Width - 2
			m.viewport.Height = contentHeight
			m.textarea.SetWidth(msg.Width - 4)
		}

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			m.quitting = true
			return m, tea.Quit

		case tea.KeyCtrlL:
			m.clearMessages()
			return m, nil

		case tea.KeyUp:
			if m.slashMode {
				if m.slashIdx > 0 {
					m.slashIdx--
				}
				return m, nil
			}
			if m.historyIdx < len(m.history)-1 {
				m.historyIdx++
				m.textarea.SetValue(m.history[len(m.history)-1-m.historyIdx])
			}
			return m, nil

		case tea.KeyDown:
			if m.slashMode {
				filtered := m.filterCommands()
				if len(filtered) > 0 && m.slashIdx < len(filtered)-1 {
					m.slashIdx++
				}
				return m, nil
			}
			if m.historyIdx > 0 {
				m.historyIdx--
				m.textarea.SetValue(m.history[len(m.history)-1-m.historyIdx])
			} else if m.historyIdx == 0 {
				m.historyIdx = -1
				m.textarea.SetValue("")
			}
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

			// Add to history
			m.history = append(m.history, input)
			m.historyIdx = -1

			// Add user message
			m.addMessage(DisplayMessage{Role: RoleUser, Content: input, Timestamp: time.Now()})

			m.textarea.SetValue("")
			m.running = true
			return m, tea.Batch(m.spinner.Tick, m.runFlow(input))
		}

	case flowResultMsg:
		m.running = false
		if msg.err != nil {
			m.addMessage(DisplayMessage{Role: RoleSystem, Content: fmt.Sprintf("Error: %v", msg.err), Timestamp: time.Now()})
		} else {
			m.totalUsage.PromptTokens += msg.result.Usage.PromptTokens
			m.totalUsage.CompletionTokens += msg.result.Usage.CompletionTokens
			m.totalUsage.TotalTokens += msg.result.Usage.TotalTokens
			m.roundNum++

			for i, stage := range msg.result.Stages {
				m.addMessage(DisplayMessage{
					Role:      RoleAgent,
					AgentName: stage.TeamName + "/" + stage.StageName,
					Content:   stage.Content,
					RoundNum:  m.roundNum,
					Timestamp: time.Now(),
				})
				_ = agentStyle(i) // reserve for future per-agent coloring
			}

			if msg.result.Usage.TotalTokens > 0 {
				m.addMessage(DisplayMessage{
					Role:      RoleUsage,
					Content:   fmt.Sprintf("Round %d: %d tokens (Total: %d)", m.roundNum, msg.result.Usage.TotalTokens, m.totalUsage.TotalTokens),
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
	statusLeft := fmt.Sprintf("Model: %s | Round: %d", m.modelName, m.roundNum)
	if m.running {
		statusLeft = m.spinner.View() + " " + statusLeft
	}
	statusRight := "Ctrl+C: quit | /help: commands"
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
	info := fmt.Sprintf("Flow: %s | Model: %s | Agents: %d | Teams: %d\n",
		m.flowName, m.modelName, m.agentCount, m.teamCount)
	return welcomeBanner + "\n" + systemMsgStyle.Render(info)
}

func (m *TUIModel) addMessage(msg DisplayMessage) {
	m.messages = append(m.messages, msg)
	m.renderMessages()
}

func (m *TUIModel) clearMessages() {
	m.messages = nil
	m.renderMessages()
}

func (m *TUIModel) renderMessages() {
	var lines []string

	for _, msg := range m.messages {
		switch msg.Role {
		case RoleUser:
			lines = append(lines, userMsgStyle.Render("> "+msg.Content))
		case RoleAssistant:
			lines = append(lines, assistantMsgStyle.Render(msg.Content))
		case RoleAgent:
			header := fmt.Sprintf("[%s]", msg.AgentName)
			lines = append(lines, agentHeaderStyles[0].Render(header))
			lines = append(lines, assistantMsgStyle.Render(msg.Content))
		case RoleSystem:
			lines = append(lines, systemMsgStyle.Render(msg.Content))
		case RoleUsage:
			lines = append(lines, usageMsgStyle.Render(msg.Content))
		}
	}

	m.viewport.SetContent(strings.Join(lines, "\n"))
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
  Enter    - Send message
  Up/Down  - Navigate input history
  Ctrl+L   - Clear screen
  Ctrl+C   - Exit`}

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
