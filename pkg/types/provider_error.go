package types

import (
	"fmt"
	"net/http"
	"strings"
)

// ProviderErrorKind classifies a provider failure for fallback decisions.
type ProviderErrorKind string

const (
	KindRateLimit   ProviderErrorKind = "rate_limit"   // 429 / quota / billing / rate_limit_exceeded
	KindServerError ProviderErrorKind = "server_error" // 5xx / 529 overloaded
	KindTimeout     ProviderErrorKind = "timeout"      // connect/read timeout, stream stall
	KindNetwork     ProviderErrorKind = "network"      // DNS / connection refused / EOF / reset
	KindBadRequest  ProviderErrorKind = "bad_request"  // 400 parameter error (client bug)
	KindAuth        ProviderErrorKind = "auth"         // 401/403 (credential problem)
	KindUnknown     ProviderErrorKind = "unknown"
)

// ProviderError is a structured error returned by a provider's HTTP layer. It
// preserves the original message so string-matching helpers (for example
// isUnsupportedParameterError) keep working.
type ProviderError struct {
	Kind       ProviderErrorKind
	StatusCode int    // HTTP status code, 0 for non-HTTP failures
	Message    string // original error text
	Err        error  // underlying error (optional, surfaced via Unwrap)
}

func (e *ProviderError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s (http %d): %s", e.Kind, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Retryable reports whether the error is worth switching to a fallback model.
func (e *ProviderError) Retryable() bool {
	switch e.Kind {
	case KindRateLimit, KindServerError, KindTimeout, KindNetwork:
		return true
	default:
		return false
	}
}

// ClassifyProviderError maps an HTTP status code plus the provider's error.type
// string (from the response body) to a ProviderError. The body error.type
// takes priority over the status code because gateways such as Anthropic
// encode rate-limit/overloaded conditions there. The returned error preserves
// the raw body text as Message; callers may override Message with a richer
// formatted string.
func ClassifyProviderError(statusCode int, errorType string, body []byte) *ProviderError {
	kind := classifyKind(statusCode, errorType)
	message := strings.TrimSpace(string(body))
	return &ProviderError{Kind: kind, StatusCode: statusCode, Message: message}
}

// classifyProviderError is kept as an unexported alias for tests within the
// package that need a stable symbol.
func classifyProviderError(statusCode int, errorType string, body []byte) *ProviderError {
	return ClassifyProviderError(statusCode, errorType, body)
}

func classifyKind(statusCode int, errorType string) ProviderErrorKind {
	lower := strings.ToLower(strings.TrimSpace(errorType))
	switch lower {
	case "rate_limit_error", "rate_limit_exceeded", "rate_limited", "rate limit", "insufficient_quota", "quota_exceeded", "billing_error":
		return KindRateLimit
	case "overloaded_error", "overloaded", "server_error", "internal_error", "api_error", "service_unavailable":
		return KindServerError
	case "timeout", "timeout_error":
		return KindTimeout
	case "invalid_request_error", "bad_request", "invalid_request":
		return KindBadRequest
	case "authentication_error", "permission_error", "unauthorized", "forbidden":
		return KindAuth
	}

	switch {
	case statusCode == http.StatusTooManyRequests: // 429
		return KindRateLimit
	case statusCode == http.StatusUnauthorized, statusCode == http.StatusForbidden: // 401/403
		return KindAuth
	case statusCode == http.StatusBadRequest: // 400
		return KindBadRequest
	case statusCode == 529: // overloaded
		return KindServerError
	case statusCode >= 500: // 5xx
		return KindServerError
	default:
		return KindUnknown
	}
}

// NewProviderNetworkError wraps a transport-level error (DNS, connection
// refused, EOF, reset) as a network failure.
func NewProviderNetworkError(err error) *ProviderError {
	return &ProviderError{Kind: KindNetwork, Message: err.Error(), Err: err}
}

// NewProviderTimeoutError wraps a timeout (context.DeadlineExceeded,
// net.Error.Timeout) as a timeout failure.
func NewProviderTimeoutError(err error) *ProviderError {
	return &ProviderError{Kind: KindTimeout, Message: err.Error(), Err: err}
}
