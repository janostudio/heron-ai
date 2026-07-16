package agent

import (
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type HandoffRouter struct {
	agents map[string]types.AgentConfig
}

func NewHandoffRouter(agents map[string]types.AgentConfig) *HandoffRouter {
	return &HandoffRouter{agents: agents}
}

func (h *HandoffRouter) GetAgent(name string) (types.AgentConfig, error) {
	agent, ok := h.agents[name]
	if !ok {
		return types.AgentConfig{}, fmt.Errorf("handoff target %q not found", name)
	}
	return agent, nil
}

func (h *HandoffRouter) CanHandoff(fromAgent string, toAgent string) bool {
	if fromAgent == toAgent {
		return false
	}
	agent, ok := h.agents[fromAgent]
	if !ok {
		return false
	}
	for _, target := range agent.Handoffs {
		if target == toAgent {
			return true
		}
	}
	return false
}

func (h *HandoffRouter) BuildContext(task string, input string, history []types.Message) types.HandoffContext {
	return types.HandoffContext{
		Task:    task,
		Input:   input,
		History: history,
	}
}
