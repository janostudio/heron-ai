package model

import (
	"context"
	"fmt"
	"time"
)

type RetryHandler struct {
	maxRetries int
	backoff    time.Duration
}

func NewRetryHandler(maxRetries int, backoff time.Duration) *RetryHandler {
	if maxRetries <= 0 {
		maxRetries = 3
	}
	if backoff <= 0 {
		backoff = time.Second
	}
	return &RetryHandler{
		maxRetries: maxRetries,
		backoff:    backoff,
	}
}

func (h *RetryHandler) Retry(ctx context.Context, fn func() error) error {
	var lastErr error
	for i := 0; i < h.maxRetries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		err := fn()
		if err == nil {
			return nil
		}

		lastErr = err

		if i < h.maxRetries-1 {
			backoff := h.backoff * time.Duration(1<<uint(i))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
		}
	}
	return fmt.Errorf("retry exhausted after %d attempts: %w", h.maxRetries, lastErr)
}
