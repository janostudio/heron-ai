package view

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
)

// A RoleAgent message shows "team › agent" so a multi-agent run is readable.
func TestRenderMessagesShowsAgentIdentity(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	model.addMessage(DisplayMessage{
		Role: RoleAgent, CallID: "root_cause",
		AgentName: "root-cause-analyst", TeamName: "diagnose",
		Content: "content",
	})

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "diagnose › root-cause-analyst")
}

func TestAgentHeaderDegradesGracefully(t *testing.T) {
	assert.Equal(t, "t › a", agentHeader(DisplayMessage{TeamName: "t", AgentName: "a"}))
	assert.Equal(t, "a", agentHeader(DisplayMessage{AgentName: "a"}))
	assert.Equal(t, "t", agentHeader(DisplayMessage{TeamName: "t"}))
	assert.Equal(t, "[c]", agentHeader(DisplayMessage{CallID: "c"}))
}

// flowResultMsg emits one message per call when the calls are known, so each
// reply carries its own identity instead of one merged team blob.
func TestFlowResultEmitsPerCallMessages(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})

	updated, _ := model.Update(flowResultMsg{result: &FlowResult{
		Teams: []TeamOutput{{
			TeamID: "diagnose",
			Reply:  "merged (unused when calls exist)",
			Calls: []CallOutput{
				{CallID: "search", AgentID: "issue-searcher", Reply: "found issues"},
				{CallID: "root_cause", AgentID: "root-cause-analyst", Reply: "root cause is X"},
			},
		}},
	}})
	model = updated.(*TUIModel)

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "diagnose › issue-searcher")
	assert.Contains(t, out, "diagnose › root-cause-analyst")
	assert.Contains(t, out, "found issues")
	assert.Contains(t, out, "root cause is X")
	assert.NotContains(t, out, "merged (unused when calls exist)",
		"when per-call output exists the aggregated reply must not also be shown")
}

func TestFlowResultFallsBackToTeamReply(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})

	updated, _ := model.Update(flowResultMsg{result: &FlowResult{
		Teams: []TeamOutput{{TeamID: "default", Reply: "team aggregate", Calls: nil}},
	}})
	model = updated.(*TUIModel)

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "team aggregate")
	assert.Contains(t, out, "default")
}

// SeedHistory must render recovered replies as RoleAgent with their identity,
// so a resumed transcript attributes replies the same way a live one does
// (ISSUE-tui-resume-identity-and-round-count).
func TestSeedHistoryPreservesIdentity(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 30})

	model.SeedHistory([]HistoryTurn{{
		Input: "go",
		Messages: []HistoryMessage{
			{TeamID: "diagnose", CallID: "root_cause", AgentID: "root-cause-analyst", Reply: "cause is X"},
			{TeamID: "intake", CallID: "acquire", AgentID: "issue-searcher", Reply: "slot acquired"},
		},
	}})

	out := stripANSI(model.viewport.View())
	assert.Contains(t, out, "diagnose › root-cause-analyst")
	assert.Contains(t, out, "intake › issue-searcher")
	assert.Contains(t, out, "cause is X")
	assert.Contains(t, out, "slot acquired")
}

// A round is one user input: submitting increments roundNum exactly once, and
// a multi-team reply (several flowResultMsg) must not advance it further
// (ISSUE-tui-resume-identity-and-round-count problem 2).
func TestRoundAdvancesOncePerUserInput(t *testing.T) {
	model := NewTUIModel("flow", "gpt-4", 1, 1, &mockFlowRunner{})
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 40})

	// Submit one input.
	model.textarea.SetValue("do the thing")
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*TUIModel)
	assert.Equal(t, 1, model.roundNum, "submit advances the round")

	// Two teams run for this single input: two flowResultMsg.
	for _, team := range []string{"default", "intake"} {
		updated, _ = model.Update(flowResultMsg{result: &FlowResult{
			Teams: []TeamOutput{{TeamID: team, Reply: "reply from " + team}},
		}})
		model = updated.(*TUIModel)
	}
	assert.Equal(t, 1, model.roundNum, "further replies must not advance the round")

	// A second submit advances to 2.
	model.running = false
	model.textarea.SetValue("again")
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(*TUIModel)
	assert.Equal(t, 2, model.roundNum)
}

// Long runs with no spaces (slash lists, paths, URLs) must be hard-wrapped to
// the target width; glamour's word wrap leaves them on one over-wide line that
// the viewport then clips. Regression for the long-string defect in
// ISSUE-tui-conversation-display-research.
func TestRenderMarkdownHardWrapsLongTokens(t *testing.T) {
	long := "intake/diagnose/design/challenge/fix/verify/review/deliver/learn/audit"
	out := renderMarkdown("决定激活 "+long+" 团队。", 40)

	for _, line := range strings.Split(out, "\n") {
		assert.LessOrEqual(t, ansi.StringWidth(line), 40,
			"every rendered line must fit the wrap width: %q", ansi.Strip(line))
	}
	// Nothing is lost — the token survives (possibly split across lines).
	joined := strings.ReplaceAll(ansi.Strip(out), "\n", "")
	joined = strings.ReplaceAll(joined, " ", "")
	assert.Contains(t, joined, long)
}
