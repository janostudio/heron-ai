package view

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleRunTooLargeBodyIs413 pins the difference between "your JSON is
// malformed" and "your JSON is too big".
//
// Without the distinction a capped body surfaces as a decode error and is
// answered 400, which sends the client off to fix a payload that is well formed
// and will never fit.
func TestHandleRunTooLargeBodyIs413(t *testing.T) {
	rt := &mockRuntime{}
	h := NewRuntimeHandler(rt)

	body := `{"input":"` + strings.Repeat("x", 1024) + `"}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(body))
	// Stand in for the server's cap without building a 32 MiB payload.
	req.Body = http.MaxBytesReader(rr, req.Body, 16)

	h.HandleRun(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if got := rr.Body.String(); !strings.Contains(got, "limit") {
		t.Fatalf("body = %q, want it to state the limit", got)
	}
	if rt.startCalls != 0 || rt.handleCalls != 0 {
		t.Fatalf("a refused body must not reach the runtime (start=%d handle=%d)", rt.startCalls, rt.handleCalls)
	}
}

// TestHandleTurnTooLargeBodyIs413 covers the other buffered entry point: the cap
// is enforced by the server middleware, but every handler that decodes a body
// has to answer the same way or the contract depends on which path was used.
func TestHandleTurnTooLargeBodyIs413(t *testing.T) {
	rt := &mockRuntime{}
	h := NewRuntimeHandler(rt)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sessions/turn?session_id=fs_x",
		strings.NewReader(`{"input":"`+strings.Repeat("x", 1024)+`"}`))
	req.Body = http.MaxBytesReader(rr, req.Body, 16)

	h.HandleTurn(rr, req)

	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusRequestEntityTooLarge, rr.Body.String())
	}
	if rt.handleCalls != 0 {
		t.Fatalf("handleCalls = %d, want 0", rt.handleCalls)
	}
}
