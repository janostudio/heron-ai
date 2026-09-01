package model

import (
	"context"
	"errors"
	"log/slog"
	"net"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// defaultCooldownSeconds is the cooldown applied when a model profile does not
// declare cooldown_seconds.
const defaultCooldownSeconds = 600

// withModel stamps the actual model name onto a successful response so the
// agent layer can record which provider really served the request after a
// fallback.
func withModel(resp *types.ChatResponse, modelName string) *types.ChatResponse {
	if resp != nil {
		resp.Model = modelName
	}
	return resp
}

// isTimeoutError reports whether err represents a timeout (context deadline or
// net.Error.Timeout) rather than an immediate transport failure.
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// logFallback records a fallback transition for observability. slog writes to
// stderr by default, so it does not pollute stdout results.
func logFallback(ctx context.Context, from string, pe *types.ProviderError, to string) {
	slog.WarnContext(ctx, "model fallback triggered",
		"from_model", from,
		"to_model", to,
		"kind", string(pe.Kind),
		"status_code", pe.StatusCode,
	)
}

// nextModel returns the model that follows from in chain, or "" when from is
// the last element.
func nextModel(chain []string, from string) string {
	for i, name := range chain {
		if name == from && i+1 < len(chain) {
			return chain[i+1]
		}
	}
	return ""
}
