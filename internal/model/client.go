package model

import (
	"net"
	"net/http"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Model requests must never hang unbounded: a stalled provider connection would
// leave the AgentTurn unfinished and the session stuck in "running" (the same
// class of failure as ISSUE-bash-timeout-grandchild-hang, but on the LLM path
// rather than the tool path). The default http.DefaultClient has no timeouts at
// all (Timeout 0, no dial/header limits), so both providers would inherit an
// unbounded connection.
//
// These bounds are deliberately transport-level rather than a single
// http.Client.Timeout: responses are streamed (SSE), and a whole-request
// timeout would cap a legitimately long stream. Instead:
//
//   - DialContextTimeout bounds establishing the TCP connection.
//   - ResponseHeaderTimeout bounds the wait for the first response byte, which
//     is what a "provider accepted the request then went silent" stall looks
//     like.
//   - IdleConnTimeout / TLSHandshakeTimeout bound connection reuse and setup.
//
// The overall call still relies on the caller's context deadline (the agent
// loop), which is where a per-turn cap belongs.
const (
	modelDialTimeout      = 30 * time.Second
	modelTLSHandshake     = 30 * time.Second
	modelResponseHeader   = 5 * time.Minute
	modelIdleConnTimeout  = 90 * time.Second
	modelExpectContinue   = 1 * time.Second
	modelMaxIdleConns     = 100
	modelMaxIdleConnsHost = 20
)

// defaultModelClient is the shared HTTP client for model providers. It is
// process-global and safe for concurrent use, so a new provider does not spin
// up its own connection pool.
var defaultModelClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   modelDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          modelMaxIdleConns,
		MaxIdleConnsPerHost:   modelMaxIdleConnsHost,
		IdleConnTimeout:       modelIdleConnTimeout,
		TLSHandshakeTimeout:   modelTLSHandshake,
		ExpectContinueTimeout: modelExpectContinue,
		ResponseHeaderTimeout: modelResponseHeader,
	},
}

// classifyRejection builds the structured error for a non-2xx provider response
// and carries the Retry-After hint so the agent loop can wait as long as the
// provider asked instead of its own (shorter) backoff.
func classifyRejection(statusCode int, errorType, errorCode string, body []byte, header http.Header) *types.ProviderError {
	providerErr := types.ClassifyProviderErrorWithCode(statusCode, errorType, errorCode, body)
	providerErr.RetryAfter = types.ParseRetryAfter(header.Get("Retry-After"))
	return providerErr
}
