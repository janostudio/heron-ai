package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type HookFunc func(ctx context.Context, payload types.HookPayload) error

// DefaultHookTimeout bounds a configured hook command that declares no
// timeout of its own. A hook command is a shell command from agent config
// running inside the turn: left unbounded it would hold the turn open for as
// long as the command chooses to.
const DefaultHookTimeout = 30 * time.Second

// knownHookEvents is the closed set of events an `agent.Hooks` entry may bind
// to. It is the same five constants below and nothing else: an unknown event
// name is a typo in configuration, and dropping it silently would leave the
// author believing a guard runs when none does.
var knownHookEvents = []string{HookOnStart, HookOnEnd, HookOnToolStart, HookOnToolEnd, HookOnError}

// CommandHookRunner runs one configured hook command under a timeout, given
// the payload of the event that triggered it.
type CommandHookRunner func(ctx context.Context, payload types.HookPayload, command string, timeout time.Duration) error

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

// WithCommandHooks returns an executor carrying the hooks already registered
// on h plus one command hook per entry in cfg, in configuration order.
//
// This is what makes the `hooks:` block of an agent definition take effect:
// without it nothing reads types.AgentConfig.Hooks and the block is inert.
// It is deliberately a copy rather than a Register into h — h is the
// loop-wide executor shared by every agent, and registering per-agent hooks
// into it would leak one agent's hooks into every other agent's turns and
// accumulate a copy per turn.
//
// Configuration problems are returned as errors, never dropped: an unknown
// event name or an unparsable timeout is a typo the operator must see.
func (h *HookExecutor) WithCommandHooks(cfg []types.HookConfig, run CommandHookRunner) (*HookExecutor, error) {
	if len(cfg) == 0 {
		return h, nil
	}
	if run == nil {
		run = RunHookCommand
	}
	derived := NewHookExecutor()
	if h != nil {
		h.mu.RLock()
		for event, fns := range h.hooks {
			derived.hooks[event] = append(derived.hooks[event], fns...)
		}
		h.mu.RUnlock()
	}
	for _, hook := range cfg {
		event := strings.TrimSpace(hook.Event)
		if !isKnownHookEvent(event) {
			return nil, fmt.Errorf("unknown hook event %q: want one of %s", hook.Event, strings.Join(knownHookEvents, ", "))
		}
		command := strings.TrimSpace(hook.Command)
		if command == "" {
			return nil, fmt.Errorf("hook %q has an empty command", event)
		}
		timeout := DefaultHookTimeout
		if strings.TrimSpace(hook.Timeout) != "" {
			parsed, err := time.ParseDuration(hook.Timeout)
			if err != nil {
				return nil, fmt.Errorf("hook %q has an invalid timeout %q: %w", event, hook.Timeout, err)
			}
			if parsed <= 0 {
				return nil, fmt.Errorf("hook %q has a non-positive timeout %q", event, hook.Timeout)
			}
			timeout = parsed
		}
		derived.Register(event, func(ctx context.Context, payload types.HookPayload) error {
			return run(ctx, payload, command, timeout)
		})
	}
	return derived, nil
}

func isKnownHookEvent(event string) bool {
	for _, known := range knownHookEvents {
		if event == known {
			return true
		}
	}
	return false
}

// RunHookCommand runs one configured hook command with /bin/sh under timeout.
// A non-zero exit and a timeout are both errors: every caller that can act on
// an error does (on_start and on_tool_start abort the step), so a failing
// guard behaves like a failing guard instead of a silent no-op.
//
// The command runs on the engine host, not through the workspace backend, so
// an agent on a remote workspace still gets host-local hooks. Hook commands
// are operator configuration, not model-authored input.
func RunHookCommand(ctx context.Context, payload types.HookPayload, command string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), hookEnv(payload)...)
	out, err := cmd.CombinedOutput()
	if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("hook command timed out after %s: %s", timeout, command)
	}
	if err != nil {
		trimmed := strings.TrimSpace(string(out))
		if trimmed == "" {
			return fmt.Errorf("hook command failed: %w: %s", err, command)
		}
		return fmt.Errorf("hook command failed: %w: %s: %s", err, command, trimmed)
	}
	return nil
}

// hookEnv exposes to the command the fields the payload reliably carries.
// Flow-level fields of types.HookPayload are not populated by the agent
// runtime, so they are deliberately not exported rather than exported empty.
func hookEnv(payload types.HookPayload) []string {
	return []string{
		"HERON_HOOK_EVENT=" + payload.Event,
		"HERON_AGENT_ID=" + payload.AgentID,
		"HERON_TOOL_NAME=" + payload.ToolName,
		"HERON_ROUND=" + strconv.Itoa(payload.Round),
	}
}

// hookExecutorKey carries the executor resolved for this turn. It is read by
// the hook helpers in runtime.go, which otherwise only have the loop-wide
// executor and no access to the agent config the hooks were declared on.
type hookExecutorKey struct{}

// WithHookExecutor returns ctx carrying the executor resolved for this turn.
func WithHookExecutor(ctx context.Context, h *HookExecutor) context.Context {
	return context.WithValue(ctx, hookExecutorKey{}, h)
}

// hookExecutorFrom returns the turn's executor, falling back to fallback when
// the turn did not resolve one (no `hooks:` configured).
func hookExecutorFrom(ctx context.Context, fallback *HookExecutor) *HookExecutor {
	if h, ok := ctx.Value(hookExecutorKey{}).(*HookExecutor); ok && h != nil {
		return h
	}
	return fallback
}
