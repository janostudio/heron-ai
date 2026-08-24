// Package eval contains optional evaluation helpers for the new Flow/Team
// runtime. It is not part of the core execution path.
package eval

import "github.com/heron-ai/heron-engine/pkg/types"

type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

type Result struct {
	Passed  bool
	Score   float64
	Details map[string]float64
}

// EvaluateRoute compares the resolved TeamTurn routes with an expected
// sequence. Tests can use it for a single Team, a complete FlowTurn, or a
// replayed set of TeamTurn results.
func (e *Engine) EvaluateRoute(results []types.TeamTurnResult, expected []types.NextAction) *Result {
	if len(results) == 0 {
		return &Result{
			Passed:  len(expected) == 0,
			Score:   boolScore(len(expected) == 0),
			Details: map[string]float64{"route_accuracy": boolScore(len(expected) == 0)},
		}
	}

	matches := 0
	for i, result := range results {
		if i < len(expected) && result.Next != nil && result.Next.Action == expected[i] {
			matches++
		}
	}
	score := float64(matches) / float64(len(results))
	return &Result{
		Passed:  score >= 0.5,
		Score:   score,
		Details: map[string]float64{"route_accuracy": score},
	}
}

// EvaluateFailures measures how many TeamTurns completed without an error.
func (e *Engine) EvaluateFailures(results []types.TeamTurnResult) *Result {
	if len(results) == 0 {
		return &Result{Passed: true, Score: 1, Details: map[string]float64{"error_rate": 0}}
	}

	failures := 0
	for _, result := range results {
		if result.Error != "" {
			failures++
		}
	}
	errorRate := float64(failures) / float64(len(results))
	score := 1 - errorRate
	return &Result{
		Passed:  score >= 0.5,
		Score:   score,
		Details: map[string]float64{"error_rate": errorRate},
	}
}

func boolScore(value bool) float64 {
	if value {
		return 1
	}
	return 0
}
