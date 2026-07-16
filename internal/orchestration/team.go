package orchestration

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type TeamRunner struct {
	scheduler *TeamScheduler
}

func NewTeamRunner(scheduler *TeamScheduler) *TeamRunner {
	return &TeamRunner{scheduler: scheduler}
}

// Run executes a team and returns the result
func (r *TeamRunner) Run(ctx context.Context, team types.TeamConfig, agents map[string]types.AgentConfig, input string) (*types.TeamResult, error) {
	results, err := r.scheduler.Schedule(ctx, team.Stages, agents, input)
	if err != nil {
		return &types.TeamResult{
			Error: err.Error(),
		}, fmt.Errorf("schedule team %q: %w", team.Name, err)
	}

	if len(results) == 0 {
		return &types.TeamResult{
			Content: "",
			Signal:  types.SignalContinue,
		}, nil
	}

	// Last result determines the team result
	lastResult := results[len(results)-1]

	totalUsage := types.TokenUsage{}
	var content string
	for _, r := range results {
		totalUsage.PromptTokens += r.Usage.PromptTokens
		totalUsage.CompletionTokens += r.Usage.CompletionTokens
		totalUsage.TotalTokens += r.Usage.TotalTokens
		content += r.Raw + "\n"
	}

	return &types.TeamResult{
		Content:      content,
		Signal:       lastResult.Signal,
		Raw:          lastResult.Raw,
		Usage:        totalUsage,
		AgentResults: results,
	}, nil
}
