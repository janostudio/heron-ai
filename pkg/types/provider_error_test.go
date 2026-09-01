package types

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func TestClassifyProviderError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		errorType  string
		body       string
		wantKind   ProviderErrorKind
	}{
		{"429 rate limit", 429, "", `{"error":{"message":"too many"}}`, KindRateLimit},
		{"401 auth", 401, "", "unauthorized", KindAuth},
		{"403 auth", 403, "", "forbidden", KindAuth},
		{"400 bad request", 400, "", "bad request", KindBadRequest},
		{"500 server error", 500, "", "boom", KindServerError},
		{"502 server error", 502, "", "boom", KindServerError},
		{"503 server error", 503, "", "boom", KindServerError},
		{"529 overloaded", 529, "", "overloaded", KindServerError},
		{"unknown status", 418, "", "teapot", KindUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pe := classifyProviderError(tt.statusCode, tt.errorType, []byte(tt.body))
			if pe.Kind != tt.wantKind {
				t.Fatalf("kind = %q, want %q", pe.Kind, tt.wantKind)
			}
			if pe.StatusCode != tt.statusCode {
				t.Fatalf("status = %d, want %d", pe.StatusCode, tt.statusCode)
			}
		})
	}
}

func TestClassifyProviderErrorBodyTypePriority(t *testing.T) {
	// Body error.type must override the status code.
	pe := classifyProviderError(200, "rate_limit_error", []byte("{}"))
	if pe.Kind != KindRateLimit {
		t.Fatalf("kind = %q, want rate_limit (body type should win over status)", pe.Kind)
	}

	pe = classifyProviderError(500, "overloaded_error", []byte("{}"))
	if pe.Kind != KindServerError {
		t.Fatalf("kind = %q, want server_error", pe.Kind)
	}

	// 401 status with body type "rate_limit_error" -> rate_limit wins.
	pe = classifyProviderError(401, "rate_limit_error", []byte("{}"))
	if pe.Kind != KindRateLimit {
		t.Fatalf("kind = %q, want rate_limit", pe.Kind)
	}
}

func TestProviderErrorRetryable(t *testing.T) {
	tests := []struct {
		kind ProviderErrorKind
		want bool
	}{
		{KindRateLimit, true},
		{KindServerError, true},
		{KindTimeout, true},
		{KindNetwork, true},
		{KindAuth, false},
		{KindBadRequest, false},
		{KindUnknown, false},
	}
	for _, tt := range tests {
		pe := &ProviderError{Kind: tt.kind, Message: "x"}
		if got := pe.Retryable(); got != tt.want {
			t.Errorf("Retryable(%s) = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestProviderErrorErrorContainsOriginalMessage(t *testing.T) {
	// String-matching helpers (e.g. isUnsupportedParameterError) rely on the
	// original message being present in Error().
	msg := "invalid_request_error: Unsupported parameter: 'top_k' is not supported"
	pe := &ProviderError{Kind: KindBadRequest, StatusCode: 400, Message: msg}
	if !strings.Contains(pe.Error(), msg) {
		t.Fatalf("Error() = %q, want to contain original message %q", pe.Error(), msg)
	}
	if !strings.Contains(strings.ToLower(pe.Error()), "unsupported") {
		t.Fatalf("Error() = %q, want lowercase 'unsupported' substring preserved", pe.Error())
	}

	// Non-HTTP errors (status 0) also preserve the message.
	netPE := NewProviderNetworkError(errors.New("dial tcp: connection refused"))
	if !strings.Contains(netPE.Error(), "connection refused") {
		t.Fatalf("Error() = %q, want connection refused message", netPE.Error())
	}
	if netPE.Kind != KindNetwork {
		t.Fatalf("kind = %q, want network", netPE.Kind)
	}
}

func TestProviderErrorUnwrap(t *testing.T) {
	underlying := &net.OpError{Err: context.DeadlineExceeded}
	pe := NewProviderTimeoutError(underlying)
	if !errors.Is(pe, underlying) {
		t.Fatalf("errors.Is(pe, underlying) = false, want true")
	}
	if pe.Kind != KindTimeout {
		t.Fatalf("kind = %q, want timeout", pe.Kind)
	}
}

func TestNewProviderTimeoutError(t *testing.T) {
	deadlineErr := context.DeadlineExceeded
	pe := NewProviderTimeoutError(deadlineErr)
	if pe.Kind != KindTimeout {
		t.Fatalf("kind = %q, want timeout", pe.Kind)
	}
	if !errors.Is(pe, deadlineErr) {
		t.Fatalf("errors.Is(pe, deadlineErr) = false, want true")
	}
}

var _ = time.Now // keep time import if unused elsewhere in future edits
