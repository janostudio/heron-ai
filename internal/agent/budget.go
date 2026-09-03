package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type budgetTracker struct {
	limit    types.AgentBudget
	usage    types.AgentBudgetUsage
	deadline time.Time
}

func newBudgetTracker(limit types.AgentBudget, fallbackRounds int, now time.Time) (*budgetTracker, error) {
	if limit.MaxModelRounds <= 0 && fallbackRounds > 0 {
		limit.MaxModelRounds = fallbackRounds
	}
	if limit.MaxWallTime != "" {
		duration, err := time.ParseDuration(limit.MaxWallTime)
		if err != nil {
			return nil, fmt.Errorf("parse agent budget max_wall_time: %w", err)
		}
		if duration <= 0 {
			return nil, fmt.Errorf("agent budget max_wall_time must be positive")
		}
		now = now.UTC()
		return &budgetTracker{
			limit:    limit,
			usage:    types.AgentBudgetUsage{StartedAt: now},
			deadline: now.Add(duration),
		}, nil
	}
	return &budgetTracker{
		limit: limit,
		usage: types.AgentBudgetUsage{StartedAt: now.UTC()},
	}, nil
}

func (b *budgetTracker) checkContext(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if !b.deadline.IsZero() && !time.Now().Before(b.deadline) {
		return fmt.Errorf("agent budget exceeded: max_wall_time")
	}
	return nil
}

func (b *budgetTracker) beforeModel(ctx context.Context) error {
	if err := b.checkContext(ctx); err != nil {
		return err
	}
	if b.limit.MaxModelRounds > 0 && b.usage.ModelRounds >= b.limit.MaxModelRounds {
		return fmt.Errorf("agent budget exceeded: max_model_rounds=%d", b.limit.MaxModelRounds)
	}
	return nil
}

func (b *budgetTracker) beforeTool(ctx context.Context, count int) error {
	if err := b.checkContext(ctx); err != nil {
		return err
	}
	if count < 0 {
		count = 0
	}
	if b.limit.MaxToolCalls > 0 && b.usage.ToolCalls+count > b.limit.MaxToolCalls {
		return fmt.Errorf("agent budget exceeded: max_tool_calls=%d", b.limit.MaxToolCalls)
	}
	return nil
}

func (b *budgetTracker) addTool(count, output, fileChanges int) {
	b.usage.ToolCalls += count
	b.usage.ToolOutput += output
	b.usage.FileChanges += fileChanges
}

func (b *budgetTracker) checkUsage() error {
	if b.limit.MaxInputTokens > 0 && b.usage.InputTokens > b.limit.MaxInputTokens {
		return fmt.Errorf("agent budget exceeded: max_input_tokens=%d", b.limit.MaxInputTokens)
	}
	if b.limit.MaxOutputTokens > 0 && b.usage.OutputTokens > b.limit.MaxOutputTokens {
		return fmt.Errorf("agent budget exceeded: max_output_tokens=%d", b.limit.MaxOutputTokens)
	}
	if b.limit.MaxFileChanges > 0 && b.usage.FileChanges > b.limit.MaxFileChanges {
		return fmt.Errorf("agent budget exceeded: max_file_changes=%d", b.limit.MaxFileChanges)
	}
	if b.limit.MaxToolOutput > 0 && b.usage.ToolOutput > b.limit.MaxToolOutput {
		return fmt.Errorf("agent budget exceeded: max_tool_output=%d", b.limit.MaxToolOutput)
	}
	return nil
}

func (b *budgetTracker) usageSnapshot() types.AgentBudgetUsage {
	return b.usage
}
