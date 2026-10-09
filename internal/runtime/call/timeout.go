package call

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DefaultCallTimeout bounds a command/webhook call that declares no timeout,
// when the incoming context carries no deadline either.
//
// A command or webhook with no deadline is the same failure as an unbounded
// tool: if it never returns, the TeamTurn (and the FlowTurn above it) never
// finishes, and the session is stuck in "running" forever. The default is
// generous so it only fires on a real hang.
const DefaultCallTimeout = 15 * time.Minute

func withTimeout(ctx context.Context, callTimeout, specTimeout string) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := strings.TrimSpace(callTimeout)
	if timeout == "" {
		timeout = strings.TrimSpace(specTimeout)
	}
	if timeout == "" {
		// No configured timeout. If the caller already supplied a deadline,
		// respect it; otherwise apply the backstop so the call is bounded.
		if _, hasDeadline := ctx.Deadline(); hasDeadline {
			return ctx, func() {}, nil
		}
		timeoutCtx, cancel := context.WithTimeout(ctx, DefaultCallTimeout)
		return timeoutCtx, cancel, nil
	}
	duration, err := time.ParseDuration(timeout)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid timeout %q: %w", timeout, err)
	}
	if duration <= 0 {
		return nil, nil, fmt.Errorf("timeout must be positive: %s", timeout)
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, duration)
	return timeoutCtx, cancel, nil
}
