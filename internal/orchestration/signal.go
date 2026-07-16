package orchestration

import (
	"github.com/heron-ai/heron-engine/pkg/types"
)

type SignalRouter struct {
	flow types.FlowConfig
}

func NewSignalRouter(flow types.FlowConfig) *SignalRouter {
	return &SignalRouter{flow: flow}
}

// Route determines the next stage based on the current stage and signal
func (r *SignalRouter) Route(currentStage string, signal types.Signal) (nextStage string, action types.RunAction) {
	// Find current stage index
	currentIndex := -1
	for i, stage := range r.flow.Stages {
		if stage.Name == currentStage {
			currentIndex = i
			break
		}
	}

	if currentIndex < 0 {
		return "", types.ActionEnd
	}

	current := r.flow.Stages[currentIndex]

	switch signal {
	case types.SignalContinue:
		// Check on_signal mapping
		if next := current.OnSignal.Continue; next != nil {
			return *next, types.ActionContinue
		}
		// Default: go to next stage
		nextIndex := currentIndex + 1
		if nextIndex >= len(r.flow.Stages) {
			// Wrap around for loop mode
			if r.flow.LoopMaxRounds > 0 {
				return r.flow.Stages[0].Name, types.ActionContinue
			}
			return "", types.ActionEnd
		}
		return r.flow.Stages[nextIndex].Name, types.ActionContinue

	case types.SignalWaitInput:
		// Check on_signal mapping
		if next := current.OnSignal.WaitInput; next != nil {
			return *next, types.ActionWaitInput
		}
		return "", types.ActionWaitInput

	case types.SignalGoalAchieved, types.SignalGoalFailed, types.SignalGoalImpossible:
		return "", types.ActionEnd

	default:
		// Unknown signal, end
		return "", types.ActionEnd
	}
}

// FirstStage returns the name of the first stage
func (r *SignalRouter) FirstStage() string {
	if len(r.flow.Stages) > 0 {
		return r.flow.Stages[0].Name
	}
	return ""
}
