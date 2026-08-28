package agent

import (
	"context"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type HookFunc func(ctx context.Context, payload types.HookPayload) error

type HookExecutor struct {
	mu    sync.RWMutex
	hooks map[string][]HookFunc
}

func NewHookExecutor() *HookExecutor {
	return &HookExecutor{hooks: make(map[string][]HookFunc)}
}

func (h *HookExecutor) Register(event string, fn HookFunc) {
	if h == nil || fn == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hooks[event] = append(h.hooks[event], fn)
}

func (h *HookExecutor) Execute(ctx context.Context, event string, payload types.HookPayload) error {
	if h == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	h.mu.RLock()
	hooks := append([]HookFunc(nil), h.hooks[event]...)
	h.mu.RUnlock()

	payload.Event = event
	for _, fn := range hooks {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := fn(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

func (h *HookExecutor) ExecuteBestEffort(ctx context.Context, event string, payload types.HookPayload) error {
	return h.Execute(ctx, event, payload)
}

// Event constants
const (
	HookOnStart     = "on_start"
	HookOnEnd       = "on_end"
	HookOnToolStart = "on_tool_start"
	HookOnToolEnd   = "on_tool_end"
	HookOnError     = "on_error"
)
