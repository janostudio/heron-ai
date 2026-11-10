//go:build e2e

package e2e

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestErr1MissingParameterIs400 covers I-ERR-1's first half: a missing parameter
// is a request problem, so 400. The body must name the parameter (I-ERR-2) —
// "EOF" or "bad request" leaves the caller guessing which field is required.
func TestErr1MissingParameterIs400(t *testing.T) {
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-ERR-1"})

	cases := []struct {
		name   string
		method string
		path   string
		param  string
		body   any
	}{
		{name: "turn", method: http.MethodPost, path: "/api/sessions/turn", param: "session_id", body: map[string]any{"input": "x"}},
		{name: "status", method: http.MethodGet, path: "/api/status", param: "session_id"},
		{name: "result", method: http.MethodGet, path: "/api/result", param: "session_id"},
		{name: "stream", method: http.MethodGet, path: "/api/stream", param: "session_id"},
		{name: "resume", method: http.MethodPost, path: "/api/resume", param: "session_id", body: map[string]any{"input": "x"}},
		{name: "approvals", method: http.MethodPost, path: "/api/approvals", param: "session_id", body: map[string]any{"request_id": "r1"}},
		{name: "cancel", method: http.MethodPost, path: "/api/cancel", param: "session_id"},
		{name: "recovery_status", method: http.MethodGet, path: "/api/recovery/status", param: "session_id"},
		{name: "tasks", method: http.MethodGet, path: "/api/tasks", param: "task_id"},
		{name: "tasks_cancel", method: http.MethodPost, path: "/api/tasks/cancel", param: "task_id"},
		{name: "tasks_stream", method: http.MethodGet, path: "/api/tasks/stream", param: "task_id"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			judge(t, "I-ERR-1", "http")
			body, status, err := e.do(tc.method, tc.path, tc.body)
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			if status != http.StatusBadRequest {
				t.Fatalf("%s without %s: status=%d body=%s; want 400", tc.path, tc.param, status, strings.TrimSpace(string(body)))
			}
			if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(tc.param)) {
				t.Fatalf("%s: body %q does not name the missing parameter %q (I-ERR-2)", tc.path, strings.TrimSpace(string(body)), tc.param)
			}
		})
	}
}

// TestErr2UnknownResourceIs404 covers the second half of I-ERR-1: a parameter
// that is present but names nothing is not a bad request — it is a missing
// resource. Collapsing the two into 400 makes clients treat "my session
// expired" as "my request was malformed", which is the wrong retry decision.
func TestErr2UnknownResourceIs404(t *testing.T) {
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-ERR-1"})

	cases := []struct {
		name    string
		method  string
		path    string
		body    any
		subject string // the id the 404 has to quote back (I-ERR-2)
	}{
		{name: "turn", method: http.MethodPost, path: "/api/sessions/turn?session_id=fs_unknown", body: map[string]any{"input": "x"}, subject: "fs_unknown"},
		// The other public turn entry. It used to answer 400 here, and a client
		// reading that retries a session that no longer exists.
		{name: "run_body", method: http.MethodPost, path: "/api/run", body: map[string]any{"flow_session_id": "fs_unknown", "input": "x"}, subject: "fs_unknown"},
		{name: "run_query", method: http.MethodPost, path: "/api/run?flow_session_id=fs_unknown", body: map[string]any{"input": "x"}, subject: "fs_unknown"},
		{name: "status", method: http.MethodGet, path: "/api/status?session_id=fs_unknown", subject: "fs_unknown"},
		{name: "result", method: http.MethodGet, path: "/api/result?session_id=fs_unknown", subject: "fs_unknown"},
		{name: "stream", method: http.MethodGet, path: "/api/stream?session_id=fs_unknown", subject: "fs_unknown"},
		{name: "resume", method: http.MethodPost, path: "/api/resume?session_id=fs_unknown", body: map[string]any{"input": "x"}, subject: "fs_unknown"},
		{name: "cancel", method: http.MethodPost, path: "/api/cancel?session_id=fs_unknown", subject: "fs_unknown"},
		{name: "approvals", method: http.MethodPost, path: "/api/approvals?session_id=fs_unknown", body: map[string]any{"request_id": "req_unknown", "approved": true}, subject: "fs_unknown"},
		{name: "recovery_status", method: http.MethodGet, path: "/api/recovery/status?session_id=fs_unknown", subject: "fs_unknown"},
		{name: "recovery", method: http.MethodPost, path: "/api/recovery?session_id=fs_unknown", body: map[string]any{}, subject: "fs_unknown"},
		{name: "tasks", method: http.MethodGet, path: "/api/tasks?task_id=t_unknown", subject: "t_unknown"},
		{name: "tasks_stream", method: http.MethodGet, path: "/api/tasks/stream?task_id=t_unknown", subject: "t_unknown"},
		{name: "tasks_cancel", method: http.MethodPost, path: "/api/tasks/cancel?task_id=t_unknown", subject: "t_unknown"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			judge(t, "I-ERR-1", "http")
			body, status, err := e.do(tc.method, tc.path, tc.body)
			if err != nil {
				t.Fatalf("%s: %v", tc.path, err)
			}
			if status != http.StatusNotFound {
				t.Fatalf("%s: status=%d body=%s; want 404 for a missing resource", tc.path, status, strings.TrimSpace(string(body)))
			}
			if strings.TrimSpace(string(body)) == "" {
				t.Fatalf("%s: empty 404 body does not say what was not found (I-ERR-2)", tc.path)
			}
			if !strings.Contains(string(body), tc.subject) {
				t.Fatalf("%s: body %q does not quote the missing id %q (I-ERR-2)", tc.path, strings.TrimSpace(string(body)), tc.subject)
			}
		})
	}
}

// TestErr4WrongMethodIs405 covers I-ERR-3: a path answers only the methods it
// declares.
//
// The failure this guards is not cosmetic. Every route here used to accept any
// method, so `GET /api/cancel?session_id=…` cancelled a session and
// `GET /api/sessions/turn` started a turn — a read request with a write effect,
// which no client-side retry policy can be right about.
func TestErr4WrongMethodIs405(t *testing.T) {
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-ERR-3"})

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "turn_via_get", method: http.MethodGet, path: "/api/sessions/turn?session_id=fs_x"},
		{name: "resume_via_get", method: http.MethodGet, path: "/api/resume?session_id=fs_x"},
		{name: "approvals_via_get", method: http.MethodGet, path: "/api/approvals?session_id=fs_x"},
		{name: "cancel_via_get", method: http.MethodGet, path: "/api/cancel?session_id=fs_x"},
		{name: "recovery_via_get", method: http.MethodGet, path: "/api/recovery?session_id=fs_x"},
		{name: "tasks_cancel_via_get", method: http.MethodGet, path: "/api/tasks/cancel?task_id=t_x"},
		{name: "status_via_post", method: http.MethodPost, path: "/api/status?session_id=fs_x"},
		{name: "tasks_via_post", method: http.MethodPost, path: "/api/tasks?task_id=t_x"},
		{name: "health_via_post", method: http.MethodPost, path: "/api/health"},
		{name: "run_via_put", method: http.MethodPut, path: "/api/run"},
		{name: "sessions_via_delete", method: http.MethodDelete, path: "/api/sessions"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			judge(t, "I-ERR-3", "http")
			body, status, err := e.do(tc.method, tc.path, nil)
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.path, err)
			}
			if status != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d, want 405 (body=%s)", tc.method, tc.path, status, strings.TrimSpace(string(body)))
			}
			if strings.TrimSpace(string(body)) == "" {
				t.Fatalf("%s %s: empty 405 body does not say which method is allowed", tc.method, tc.path)
			}
		})
	}

	// The declared method still works, and the response carries Allow so a client
	// can discover the contract instead of guessing.
	allowed, status, err := e.do(http.MethodGet, "/api/health", nil)
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/health = %d, want 200", status)
	}
	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/health", Status: status},
		"", map[string]any{"body_len": len(allowed)})
}

// TestErr5TooLargeBodyIs413 covers I-ERR-4: the server caps how much of one
// request body it will buffer, and a body over the cap is refused as too large.
//
// Answering 400 instead would tell the client its JSON was malformed, so it
// validates a payload that is well formed and will never fit. The cap is what
// keeps a single caller from making the engine allocate without bound.
func TestErr5TooLargeBodyIs413(t *testing.T) {
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-ERR-4"})

	// Well-formed JSON, one kilobyte over the server's cap.
	const capBytes = 32 << 20
	payload := `{"input":"` + strings.Repeat("x", capBytes) + `"}`

	body, status, err := e.doRaw(http.MethodPost, "/api/run", "application/json", []byte(payload))
	if err != nil {
		t.Fatalf("POST /api/run: %v", err)
	}
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body=%s)", status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(strings.ToLower(string(body)), "limit") {
		t.Fatalf("body %q does not state the limit", strings.TrimSpace(string(body)))
	}
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: status},
		"", map[string]any{"body_len": len(payload)})

	// A chunked request carries no Content-Length, so the up-front check cannot
	// run: the cap is enforced while reading instead, and the handler still has to
	// answer 413 rather than 400. Without this the two paths could disagree and
	// only one of them would be honest.
	t.Run("chunked body is refused while reading", func(t *testing.T) {
		req, reqErr := http.NewRequest(http.MethodPost, e.BaseURL+"/api/run",
			io.LimitReader(bytes.NewReader([]byte(payload)), int64(len(payload))))
		if reqErr != nil {
			t.Fatalf("build request: %v", reqErr)
		}
		req.ContentLength = -1 // chunked: nothing to check before reading
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e.Token)

		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("POST /api/run (chunked): %v", doErr)
		}
		defer resp.Body.Close()
		chunkedBody, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("chunked status = %d, want 413 (body=%s)", resp.StatusCode, strings.TrimSpace(string(chunkedBody)))
		}
	})
}
