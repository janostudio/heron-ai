package consolidation

import (
	"context"
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestConsolidationAgent_EmptyResults(t *testing.T) {
	agent := NewConsolidationAgent()
	result := agent.Consolidate(context.Background(), nil)
	if result != "" {
		t.Error("expected empty string for nil results")
	}
}

func TestConsolidationAgent_SingleResult(t *testing.T) {
	agent := NewConsolidationAgent()
	results := []types.AgentResult{{Raw: "Hello"}}
	result := agent.Consolidate(context.Background(), results)
	if result != "Hello" {
		t.Errorf("expected 'Hello', got %q", result)
	}
}

func TestConsolidationAgent_MultipleResults(t *testing.T) {
	agent := NewConsolidationAgent()
	results := []types.AgentResult{
		{Raw: "First"},
		{Raw: "Second"},
	}
	result := agent.Consolidate(context.Background(), results)
	if !contains(result, "First") || !contains(result, "Second") {
		t.Error("expected consolidated result to contain both inputs")
	}
}

func TestExtractSignal_Priority(t *testing.T) {
	agent := NewConsolidationAgent()
	results := []types.AgentResult{
		{Signal: types.SignalContinue},
		{Signal: types.SignalGoalAchieved},
	}
	signal := agent.ExtractSignal(results)
	if signal != types.SignalGoalAchieved {
		t.Errorf("expected goal_achieved, got %s", signal)
	}
}

func TestExtractSignal_Empty(t *testing.T) {
	agent := NewConsolidationAgent()
	signal := agent.ExtractSignal(nil)
	if signal != types.SignalContinue {
		t.Errorf("expected continue, got %s", signal)
	}
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
