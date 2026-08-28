package eval

import (
	"testing"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestEngine_EvaluateRoute(t *testing.T) {
	engine := NewEngine()
	results := []types.TeamTurnResult{
		{Next: &types.Route{Action: types.NextProceed}},
		{Next: &types.Route{Action: types.NextComplete}},
	}

	result := engine.EvaluateRoute(results, []types.NextAction{
		types.NextProceed,
		types.NextComplete,
	})
	if !result.Passed {
		t.Fatalf("expected route evaluation to pass: %#v", result)
	}
}

func TestEngine_EvaluateRouteMismatch(t *testing.T) {
	engine := NewEngine()
	results := []types.TeamTurnResult{
		{Next: &types.Route{Action: types.NextProceed}},
		{Next: &types.Route{Action: types.NextComplete}},
	}

	result := engine.EvaluateRoute(results, []types.NextAction{
		types.NextFail,
		types.NextWaitInput,
	})
	if result.Passed {
		t.Fatal("expected route evaluation to fail")
	}
}

func TestEngine_EvaluateFailures(t *testing.T) {
	engine := NewEngine()
	result := engine.EvaluateFailures([]types.TeamTurnResult{
		{},
		{Error: "call failed"},
		{},
		{Error: "timeout"},
	})
	if result.Details["error_rate"] != 0.5 {
		t.Fatalf("expected error rate 0.5, got %v", result.Details["error_rate"])
	}
	if !result.Passed {
		t.Fatal("a 50% error rate meets the optional evaluation threshold")
	}
}
