package orchestration

import (
	"context"
	"fmt"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type TeamScheduler struct {
	agentRuntime AgentRuntime
}

// AgentRuntime interface for running agents
type AgentRuntime interface {
	Run(ctx context.Context, agent types.AgentConfig, task types.TaskConfig, input string) (*types.AgentResult, error)
}

func NewTeamScheduler(agentRuntime AgentRuntime) *TeamScheduler {
	return &TeamScheduler{agentRuntime: agentRuntime}
}

// Schedule executes tasks according to the process type
func (s *TeamScheduler) Schedule(ctx context.Context, stages []types.StageConfig, agents map[string]types.AgentConfig, input string) ([]types.AgentResult, error) {
	var allResults []types.AgentResult

	for _, stage := range stages {
		switch stage.Process {
		case "parallel":
			results, err := s.runParallel(ctx, stage.Tasks, agents, input)
			if err != nil {
				return allResults, fmt.Errorf("parallel stage: %w", err)
			}
			allResults = append(allResults, results...)

		case "sequential":
			results, err := s.runSequential(ctx, stage.Tasks, agents, input, allResults)
			if err != nil {
				return allResults, fmt.Errorf("sequential stage: %w", err)
			}
			allResults = append(allResults, results...)

		default:
			return allResults, fmt.Errorf("unknown process type: %q", stage.Process)
		}
	}

	return allResults, nil
}

func (s *TeamScheduler) runParallel(ctx context.Context, tasks []types.TaskConfig, agents map[string]types.AgentConfig, input string) ([]types.AgentResult, error) {
	results := make([]types.AgentResult, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t types.TaskConfig) {
			defer wg.Done()

			agent, ok := agents[t.Agent]
			if !ok {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("agent %q not found for task %q", t.Agent, t.Name)
				}
				mu.Unlock()
				return
			}

			result, err := s.agentRuntime.Run(ctx, agent, t, input)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			results[idx] = *result
			mu.Unlock()
		}(i, task)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	return results, nil
}

func (s *TeamScheduler) runSequential(ctx context.Context, tasks []types.TaskConfig, agents map[string]types.AgentConfig, input string, previousResults []types.AgentResult) ([]types.AgentResult, error) {
	var results []types.AgentResult

	// Pass previous results as context
	contextInput := input
	if len(previousResults) > 0 {
		contextInput += "\n\n## Previous Results\n"
		for i, r := range previousResults {
			contextInput += fmt.Sprintf("Result %d: %s\n", i+1, r.Raw)
		}
	}

	for _, task := range tasks {
		agent, ok := agents[task.Agent]
		if !ok {
			return results, fmt.Errorf("agent %q not found for task %q", task.Agent, task.Name)
		}

		result, err := s.agentRuntime.Run(ctx, agent, task, contextInput)
		if err != nil {
			return results, fmt.Errorf("task %q: %w", task.Name, err)
		}

		results = append(results, *result)
		contextInput = result.Raw // pass result to next task
	}

	return results, nil
}
