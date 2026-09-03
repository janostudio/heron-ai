package view

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestAgentStyleClamping verifies the agentStyle helper returns distinct
// styles for distinct in-range indices and clamps out-of-range indices
// (including negative) to the first style without panicking.
func TestAgentStyleClamping(t *testing.T) {
	base := agentStyle(0).GetForeground()
	for i := 1; i < len(agentHeaderStyles); i++ {
		if agentStyle(i).GetForeground() == base {
			t.Fatalf("agentStyle(%d) foreground equals agentStyle(0)", i)
		}
	}

	if agentStyle(len(agentHeaderStyles)).GetForeground() != base {
		t.Fatal("out-of-range index should clamp to style[0]")
	}
	if agentStyle(-1).GetForeground() != base {
		t.Fatal("negative index should clamp to style[0]")
	}
}

// TestRenderMessagesAgentIndexColor verifies that renderMessages picks the
// agent style from the message's AgentIndex rather than a hardcoded [0], so
// multiple agents get visually distinct header colors.
func TestRenderMessagesAgentIndexColor(t *testing.T) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })

	m := NewTUIModel("flow", "model", 2, 1, nil)
	m.addMessage(DisplayMessage{Role: RoleAgent, CallID: "team-a", Content: "reply-a", AgentIndex: 0})
	m.addMessage(DisplayMessage{Role: RoleAgent, CallID: "team-b", Content: "reply-b", AgentIndex: 1})

	content := m.viewport.View()

	headerA := agentStyle(0).Render("[team-a]")
	headerB := agentStyle(1).Render("[team-b]")

	if !strings.Contains(content, headerA) {
		t.Fatalf("viewport missing header A %q in %q", headerA, content)
	}
	if !strings.Contains(content, headerB) {
		t.Fatalf("viewport missing header B %q in %q", headerB, content)
	}
	if headerA == headerB {
		t.Fatal("expected distinct header styles for distinct agent indices")
	}
}

// TestUpdateFlowResultSetsAgentIndex verifies the agent index is persisted on
// the DisplayMessage when a flow result arrives, so renderMessages can later
// resolve the correct color.
func TestUpdateFlowResultSetsAgentIndex(t *testing.T) {
	m := NewTUIModel("flow", "model", 2, 1, nil)
	m.ready = true

	model, _ := m.Update(flowResultMsg{result: &FlowResult{
		Teams: []TeamOutput{
			{TeamID: "team-0", Reply: "reply-0"},
			{TeamID: "team-1", Reply: "reply-1"},
		},
		Usage: types.TokenUsage{TotalTokens: 10},
	}})

	updated := model.(*TUIModel)
	var idxs []int
	for _, msg := range updated.GetMessages() {
		if msg.Role == RoleAgent {
			idxs = append(idxs, msg.AgentIndex)
		}
	}
	if len(idxs) != 2 {
		t.Fatalf("expected 2 agent messages, got %d", len(idxs))
	}
	if idxs[0] != 0 || idxs[1] != 1 {
		t.Fatalf("agent indices = %v, want [0 1]", idxs)
	}
}
