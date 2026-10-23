package types

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	Code       string // provider error.code, when the body carries one
	Message    string // original error text
	Err        error  // underlying error (optional, surfaced via Unwrap)
	// RetryAfter is the delay the provider asked for via the Retry-After
	// response header (429 and some 503s). Zero means the header was absent or
	// unusable, and the caller falls back to its own backoff.
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("%s (http %d): %s", e.Kind, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Message)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// maxRetryAfterSeconds bounds the delta-seconds form of Retry-After. Real
// providers ask for seconds to minutes; anything larger is a broken or hostile
// header, and multiplying it by time.Second would overflow time.Duration.
const maxRetryAfterSeconds = 24 * 60 * 60

// ParseRetryAfter converts a Retry-After header value into a delay. Both forms
// defined by RFC 9110 are accepted: delta-seconds ("120") and an HTTP-date. An
// absent, malformed, or already-elapsed value yields 0 so callers keep their own
// backoff instead of retrying immediately on a bad header.
func ParseRetryAfter(value string) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(trimmed); err == nil {
		if seconds <= 0 {
			return 0
		}
		if int64(seconds) > maxRetryAfterSeconds {
			return maxRetryAfterSeconds * time.Second
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(trimmed); err == nil {
		if wait := time.Until(when); wait > 0 {
			return wait
		}
	}
	return 0
}

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
	return ClassifyProviderErrorWithCode(statusCode, errorType, "", body)
}

// ClassifyProviderErrorWithCode also records the provider's error.code. OpenAI
// compatible gateways put the machine-readable reason there
// (context_length_exceeded, insufficient_quota, model_not_found, ...), which is
// more reliable than matching the message text.
func ClassifyProviderErrorWithCode(statusCode int, errorType, errorCode string, body []byte) *ProviderError {
	kind := classifyKind(statusCode, errorType, errorCode)
	message := strings.TrimSpace(string(body))
	return &ProviderError{Kind: kind, StatusCode: statusCode, Code: strings.TrimSpace(errorCode), Message: message}
}

// IsContextLimit reports a request rejected for exceeding the context window.
// Providers signal it through a code, a status, or free text; the code and
// status are checked because the text differs per gateway.
func (e *ProviderError) IsContextLimit() bool {
	if e == nil {
		return false
	}
	switch strings.ToLower(e.Code) {
	case "context_length_exceeded", "context_window_exceeded", "input_too_long":
		return true
	}
	return e.StatusCode == http.StatusRequestEntityTooLarge
}

// IsUnsupportedParameter reports a request rejected because a parameter is not
// recognised, so the caller can retry without it.
func (e *ProviderError) IsUnsupportedParameter() bool {
	if e == nil {
		return false
	}
	switch strings.ToLower(e.Code) {
	case "unsupported_parameter", "unknown_parameter", "unrecognized_parameter", "invalid_parameter":
		return true
	}
	return false
}

// classifyProviderError is kept as an unexported alias for tests within the
// package that need a stable symbol.
func classifyProviderError(statusCode int, errorType string, body []byte) *ProviderError {
	return ClassifyProviderError(statusCode, errorType, body)
}

func classifyKind(statusCode int, errorType, errorCode string) ProviderErrorKind {
	// error.code is the machine-readable form; prefer it when present.
	switch strings.ToLower(strings.TrimSpace(errorCode)) {
	case "insufficient_quota", "billing_hard_limit_reached", "rate_limit_exceeded":
		return KindRateLimit
	case "model_not_found", "invalid_model", "context_length_exceeded":
		return KindBadRequest
	case "invalid_api_key", "invalid_organization":
		return KindAuth
	}
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
