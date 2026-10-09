package agent

import (
	"bytes"
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

// hookTerminateGrace is how long a timed-out hook gets to exit on SIGTERM
// before its process group is killed. Short on purpose: the hook has already
// spent the budget the operator declared, and this runs inside a turn.
const hookTerminateGrace = 500 * time.Millisecond

// RunHookCommand runs one configured hook command with /bin/sh under timeout.
// A non-zero exit and a timeout are both errors: every caller that can act on
// an error does (on_start and on_tool_start abort the step), so a failing
// guard behaves like a failing guard instead of a silent no-op.
//
// The command runs on the engine host, not through the workspace backend, so
// an agent on a remote workspace still gets host-local hooks. Hook commands
// are operator configuration, not model-authored input.
//
// On timeout the command's whole process group is signalled (SIGTERM, then
// SIGKILL), not just the shell: the shell may fork, and a surviving child
// would keep the captured output pipe open.
func RunHookCommand(ctx context.Context, payload types.HookPayload, command string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = DefaultHookTimeout
	}
	if ctx == nil {
		ctx = context.Background()
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), hookEnv(payload)...)
	// Own process group, so stopHookCommand can signal the shell and whatever
	// it forked. A hook command is a shell command and the shell may fork; a
	// forked child inherits the captured stdout, and killing only the shell
	// leaves that child holding the pipe — cmd.Wait then blocks until the
	// child exits on its own and the timeout bounds nothing.
	setHookProcessGroup(cmd)
	// Last resort for a child that left the group (setsid) and still holds the
	// pipe: once the shell is gone, Wait gives up on the pipe rather than
	// blocking for as long as that child lives.
	cmd.WaitDelay = hookTerminateGrace

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("hook command failed to start: %w: %s", err, command)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return hookCommandResult(err, out.String(), command)
	case <-ctx.Done():
		stopHookCommand(cmd, done)
		return fmt.Errorf("hook command canceled: %w: %s", ctx.Err(), command)
	case <-timer.C:
		stopHookCommand(cmd, done)
		return fmt.Errorf("hook command timed out after %s: %s", timeout, command)
	}
}

// stopHookCommand ends a hook command and waits for the process group to be
// gone, so a timed-out hook leaves nothing behind. Both waits are bounded:
// WaitDelay releases the pipe if a child outside the group still holds it.
func stopHookCommand(cmd *exec.Cmd, done <-chan error) {
	_ = terminateHookProcess(cmd)
	select {
	case <-done:
		return
	case <-time.After(hookTerminateGrace):
	}
	_ = killHookProcess(cmd)
	select {
	case <-done:
	case <-time.After(hookTerminateGrace):
	}
}

// hookCommandResult turns a completed hook command into an error or nil.
func hookCommandResult(err error, out, command string) error {
	// ErrWaitDelay means the command itself exited 0 but something it spawned
	// still holds the pipe. The hook ran; that is not a hook failure.
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return fmt.Errorf("hook command failed: %w: %s", err, command)
	}
	return fmt.Errorf("hook command failed: %w: %s: %s", err, command, trimmed)
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
