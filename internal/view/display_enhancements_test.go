package view

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/assert"
)

// P1-①: workspace operations render as compact one-line summaries, not raw
// output pasted into the message body.
func TestRenderOpLinesCompactSummary(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model.addMessage(DisplayMessage{
		Role: RoleAgent, CallID: "acquire", AgentName: "searcher", TeamName: "intake",
		Content: "done",
		Ops: []OpOutput{
			{Kind: "bash", Subject: "acquire.sh genie issue-2429", ExitCode: 0},
			{Kind: "bash", Subject: "go test ./...", ExitCode: 1, Failed: true},
		},
	})

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "acquire.sh genie issue-2429")
	assert.Contains(t, out, "✓")
	assert.Contains(t, out, "✗ exit 1")
}

func TestRenderOpLineTruncatesLongSubject(t *testing.T) {
	line := stripANSI(renderOpLine(OpOutput{
		Kind: "bash", Subject: strings.Repeat("x", 500),
	}, 40))
	assert.LessOrEqual(t, len([]rune(line)), 40)
	assert.Contains(t, line, "…")
}

// P1-②: the status bar carries team, elapsed and tokens.
func TestStatusBarCarriesTeamTimeTokens(t *testing.T) {
	model := NewTUIModel("flow", "hy4-preview-ioa", 1, 1, &mockFlowRunner{})
	model.ready = true
	model.width = 120
	model.height = 30
	model.roundNum = 3
	model.lastTeam = "fix"
	model.lastDuration = 2300 * time.Millisecond
	model.totalUsage.TotalTokens = 59340

	out := stripANSI(model.View())
	assert.Contains(t, out, "Team: fix")
	assert.Contains(t, out, "Round: 3")
	assert.Contains(t, out, "2.3s")
	assert.Contains(t, out, "59.3k tok")
}

// P2-③: a round separator labels each new round. Team identity comes from the
// agent header ("team › agent"); there is no separate "▸ team" marker, which
// used to duplicate the team name (ISSUE-tui-team-duplicate-and-history-no-log).
func TestRoundSeparatorAndTeamIdentity(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	model.addMessage(DisplayMessage{Role: RoleUser, Content: "q1", RoundNum: 1})
	model.addMessage(DisplayMessage{Role: RoleAgent, TeamName: "intake", AgentName: "a", RoundNum: 1, Content: "r1"})
	model.addMessage(DisplayMessage{Role: RoleAgent, TeamName: "diagnose", AgentName: "b", RoundNum: 1, Content: "r2"})
	model.addMessage(DisplayMessage{Role: RoleUser, Content: "q2", RoundNum: 2})

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "Round 2", "a separator labels the new round")
	// Team identity is present exactly once per reply, in the agent header.
	assert.Contains(t, out, "intake › a")
	assert.Contains(t, out, "diagnose › b")
	// No redundant "▸ team" marker line.
	assert.NotContains(t, out, "▸ intake")
	assert.NotContains(t, out, "▸ diagnose")
}

// A long body is shown in full — no default folding. (An earlier fold feature
// was removed as unrequested; long content is browsed with Alt+Up/Down and the wheel.)
func TestLongMessageIsNotFolded(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 60, Height: 40})

	model.addMessage(DisplayMessage{Role: RoleAssistant, Content: strings.Repeat("line of text\n", 40)})

	out := stripANSI(model.viewport.View())
	assert.NotContains(t, out, "more lines", "content must not be folded")
	assert.Contains(t, out, "line of text")
}
