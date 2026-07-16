package orchestration

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type FlowEngine struct {
	flow         types.FlowConfig
	teams        map[string]types.TeamConfig
	agents       map[string]types.AgentConfig
	teamRunner   *TeamRunner
	signalRouter *SignalRouter
	loopGuard    *LoopGuard
	currentStage string // tracks current stage for Resume
	roundNum     int    // tracks round number
}

func NewFlowEngine(
	flow types.FlowConfig,
	teams map[string]types.TeamConfig,
	agents map[string]types.AgentConfig,
	teamRunner *TeamRunner,
) *FlowEngine {
	router := NewSignalRouter(flow)
	return &FlowEngine{
		flow:         flow,
		teams:        teams,
		agents:       agents,
		teamRunner:   teamRunner,
		signalRouter: router,
		loopGuard:    NewLoopGuard(flow.LoopMaxRounds),
		currentStage: router.FirstStage(),
	}
}

// RunResult holds the results of a flow run
type RunResult struct {
	Stages       []StageResult
	Signal       types.Signal
	Usage        types.TokenUsage
	CurrentStage string // for Resume: where to continue from
	RoundNum     int    // for Resume: current round
}

type StageResult struct {
	StageName  string
	TeamName   string
	TeamResult *types.TeamResult
}

// Run executes the flow with given input
func (e *FlowEngine) Run(ctx context.Context, input string) (*RunResult, error) {
	var result RunResult
	currentStage := e.signalRouter.FirstStage()

	if currentStage == "" {
		return nil, fmt.Errorf("flow has no stages")
	}

	for e.loopGuard.CanContinue() {
		select {
		case <-ctx.Done():
			return &result, ctx.Err()
		default:
		}

		// Find current stage config
		var stageConfig *types.FlowStage
		for _, stage := range e.flow.Stages {
			if stage.Name == currentStage {
				s := stage
				stageConfig = &s
				break
			}
		}

		if stageConfig == nil {
			return nil, fmt.Errorf("stage %q not found in flow", currentStage)
		}

		// Get team config
		team, ok := e.teams[stageConfig.Team]
		if !ok {
			return nil, fmt.Errorf("team %q not found for stage %q", stageConfig.Team, currentStage)
		}

		// Run team
		teamResult, err := e.teamRunner.Run(ctx, team, e.agents, input)
		if err != nil {
			return &result, fmt.Errorf("stage %q team %q: %w", currentStage, team.Name, err)
		}

		result.Stages = append(result.Stages, StageResult{
			StageName:  currentStage,
			TeamName:   team.Name,
			TeamResult: teamResult,
		})
		result.Usage.PromptTokens += teamResult.Usage.PromptTokens
		result.Usage.CompletionTokens += teamResult.Usage.CompletionTokens
		result.Usage.TotalTokens += teamResult.Usage.TotalTokens

		// Route to next stage based on signal
		nextStage, action := e.signalRouter.Route(currentStage, teamResult.Signal)

		switch action {
		case types.ActionContinue:
			currentStage = nextStage
			e.loopGuard.Increment()
			input = teamResult.Raw // pass output as input to next stage

		case types.ActionWaitInput:
			result.Signal = types.SignalWaitInput
			result.CurrentStage = currentStage
			result.RoundNum = e.roundNum
			e.currentStage = currentStage
			e.roundNum++
			return &result, nil

		case types.ActionEnd:
			result.Signal = teamResult.Signal
			result.CurrentStage = currentStage
			result.RoundNum = e.roundNum
			e.currentStage = currentStage
			e.roundNum++
			return &result, nil
		}
	}

	result.Signal = types.SignalWaitInput
	result.CurrentStage = e.currentStage
	result.RoundNum = e.roundNum
	return &result, nil
}

// Resume continues a run from where it left off (after wait_input)
func (e *FlowEngine) Resume(ctx context.Context, input string) (*RunResult, error) {
	if e.currentStage == "" {
		return nil, fmt.Errorf("no active run to resume")
	}

	var result RunResult

	for e.loopGuard.CanContinue() {
		select {
		case <-ctx.Done():
			return &result, ctx.Err()
		default:
		}

		var stageConfig *types.FlowStage
		for _, stage := range e.flow.Stages {
			if stage.Name == e.currentStage {
				s := stage
				stageConfig = &s
				break
			}
		}

		if stageConfig == nil {
			return nil, fmt.Errorf("stage %q not found", e.currentStage)
		}

		team, ok := e.teams[stageConfig.Team]
		if !ok {
			return nil, fmt.Errorf("team %q not found", stageConfig.Team)
		}

		teamResult, err := e.teamRunner.Run(ctx, team, e.agents, input)
		if err != nil {
			return &result, fmt.Errorf("stage %q team %q: %w", e.currentStage, team.Name, err)
		}

		result.Stages = append(result.Stages, StageResult{
			StageName:  e.currentStage,
			TeamName:   team.Name,
			TeamResult: teamResult,
		})
		result.Usage.PromptTokens += teamResult.Usage.PromptTokens
		result.Usage.CompletionTokens += teamResult.Usage.CompletionTokens
		result.Usage.TotalTokens += teamResult.Usage.TotalTokens

		nextStage, action := e.signalRouter.Route(e.currentStage, teamResult.Signal)

		switch action {
		case types.ActionContinue:
			e.currentStage = nextStage
			e.loopGuard.Increment()
			input = teamResult.Raw
		case types.ActionWaitInput:
			result.Signal = types.SignalWaitInput
			result.CurrentStage = e.currentStage
			result.RoundNum = e.roundNum
			return &result, nil
		case types.ActionEnd:
			result.Signal = teamResult.Signal
			result.CurrentStage = e.currentStage
			result.RoundNum = e.roundNum
			return &result, nil
		}
	}

	result.Signal = types.SignalWaitInput
	result.CurrentStage = e.currentStage
	result.RoundNum = e.roundNum
	return &result, nil
}
