package consolidation

import (
	"context"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type ConsolidationAgent struct{}

func NewConsolidationAgent() *ConsolidationAgent {
	return &ConsolidationAgent{}
}

// Consolidate combines multiple agent results into a coherent summary
func (a *ConsolidationAgent) Consolidate(ctx context.Context, results []types.AgentResult) string {
	if len(results) == 0 {
		return ""
	}

	if len(results) == 1 {
		return results[0].Raw
	}

	var parts []string
	parts = append(parts, "## Consolidated Results\n")

	for i, result := range results {
		parts = append(parts, "### Result "+string(rune('1'+i)))
		parts = append(parts, result.Raw)
		parts = append(parts, "")
	}

	return strings.Join(parts, "\n")
}

// ExtractSignal determines the dominant signal from multiple results
func (a *ConsolidationAgent) ExtractSignal(results []types.AgentResult) types.Signal {
	if len(results) == 0 {
		return types.SignalContinue
	}

	signalCount := make(map[types.Signal]int)
	for _, result := range results {
		if result.Signal != "" {
			signalCount[result.Signal]++
		}
	}

	// Priority: goal_achieved > goal_failed > goal_impossible > wait_input > continue
	priority := []types.Signal{
		types.SignalGoalAchieved,
		types.SignalGoalFailed,
		types.SignalGoalImpossible,
		types.SignalWaitInput,
		types.SignalContinue,
	}

	for _, sig := range priority {
		if signalCount[sig] > 0 {
			return sig
		}
	}

	return types.SignalContinue
}
