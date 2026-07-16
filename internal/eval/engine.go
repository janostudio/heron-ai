package eval

import "github.com/heron-ai/heron-engine/pkg/types"

type EvalEngine struct{}

func NewEvalEngine() *EvalEngine {
	return &EvalEngine{}
}

type EvalResult struct {
	Passed  bool
	Score   float64
	Details map[string]float64
}

func (e *EvalEngine) EvaluateSignal(runs []types.TeamResult, expectedSignals []types.Signal) *EvalResult {
	score := 0.0
	matches := 0

	for i, run := range runs {
		if i < len(expectedSignals) && run.Signal == expectedSignals[i] {
			matches++
		}
	}

	if len(runs) > 0 {
		score = float64(matches) / float64(len(runs))
	}

	return &EvalResult{
		Passed:  score >= 0.5,
		Score:   score,
		Details: map[string]float64{"signal_accuracy": score},
	}
}

func (e *EvalEngine) EvaluateToolUsage(runs []types.TeamResult) *EvalResult {
	if len(runs) == 0 {
		return &EvalResult{Passed: true, Score: 1.0}
	}

	score := 1.0
	totalErrors := 0

	for _, run := range runs {
		if run.Error != "" {
			totalErrors++
		}
	}

	if totalErrors > 0 {
		score = 1.0 - float64(totalErrors)/float64(len(runs))
		if score < 0 {
			score = 0
		}
	}

	return &EvalResult{
		Passed:  score >= 0.5,
		Score:   score,
		Details: map[string]float64{"error_rate": float64(totalErrors) / float64(len(runs))},
	}
}
