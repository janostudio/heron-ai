package call

import (
	"context"
	"fmt"
	"strings"
	"time"
)

func withTimeout(ctx context.Context, callTimeout, specTimeout string) (context.Context, context.CancelFunc, error) {
	timeout := strings.TrimSpace(callTimeout)
	if timeout == "" {
		timeout = strings.TrimSpace(specTimeout)
	}
	if timeout == "" {
		return ctx, func() {}, nil
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
