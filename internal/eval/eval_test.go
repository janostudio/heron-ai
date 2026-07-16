package eval

import (
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestEvalEngine_SignalEvaluation(t *testing.T) {
	engine := NewEvalEngine()

	runs := []types.TeamResult{
		{Signal: types.SignalContinue},
		{Signal: types.SignalGoalAchieved},
	}
	expected := []types.Signal{types.SignalContinue, types.SignalGoalAchieved}

	result := engine.EvaluateSignal(runs, expected)
	if !result.Passed {
		t.Error("expected pass for matching signals")
	}
}

func TestEvalEngine_SignalEvaluation_Mismatch(t *testing.T) {
	engine := NewEvalEngine()

	runs := []types.TeamResult{
		{Signal: types.SignalContinue},
		{Signal: types.SignalGoalAchieved},
	}
	expected := []types.Signal{types.SignalGoalFailed, types.SignalGoalImpossible}

	result := engine.EvaluateSignal(runs, expected)
	if result.Passed {
		t.Error("expected fail for mismatching signals")
	}
}

func TestEvalEngine_ToolUsage(t *testing.T) {
	engine := NewEvalEngine()

	runs := []types.TeamResult{
		{Error: ""},
		{Error: ""},
	}

	result := engine.EvaluateToolUsage(runs)
	if result.Score != 1.0 {
		t.Errorf("expected score 1.0, got %f", result.Score)
	}
}

func TestEvalEngine_ToolUsage_WithErrors(t *testing.T) {
	engine := NewEvalEngine()

	runs := []types.TeamResult{
		{Error: ""},
		{Error: "some error"},
		{Error: ""},
		{Error: "another error"},
	}

	result := engine.EvaluateToolUsage(runs)
	if result.Score >= 1.0 {
		t.Error("expected score < 1.0 with errors")
	}
}
