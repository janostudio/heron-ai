package agent

import (
	"context"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type HookFunc func(ctx context.Context, payload types.HookPayload) error

type HookExecutor struct {
	hooks map[string][]HookFunc
}

func NewHookExecutor() *HookExecutor {
	return &HookExecutor{hooks: make(map[string][]HookFunc)}
}

func (h *HookExecutor) Register(event string, fn HookFunc) {
	h.hooks[event] = append(h.hooks[event], fn)
}

func (h *HookExecutor) Execute(ctx context.Context, event string, payload types.HookPayload) error {
	for _, fn := range h.hooks[event] {
		if err := fn(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

// Event constants
const (
	HookOnStart     = "on_start"
	HookOnEnd       = "on_end"
	HookOnToolStart = "on_tool_start"
	HookOnToolEnd   = "on_tool_end"
	HookOnHandoff   = "on_handoff"
	HookOnError     = "on_error"
)
