//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestAuth1RefusesExposedBindWithoutToken covers I-AUTH-1: binding outside
// loopback without a token is not a warning, it is a refusal. The engine can
// run Bash on the host, so an unauthenticated listener is remote execution.
func TestAuth1RefusesExposedBindWithoutToken(t *testing.T) {
	judge(t, "I-AUTH-1", "startup")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-AUTH-1", bindHost: "0.0.0.0", noToken: true})
	// newEnv returns without waiting when this env exists only to observe the
	// refusal; the process must have exited on its own.
	done := make(chan error, 1)
	go func() { done <- e.cmd.Wait() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("engine exited 0 after being asked to serve 0.0.0.0 without a token; expected refusal")
		}
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 0 {
			t.Fatalf("engine exited 0; expected non-zero exit code")
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("engine kept running on 0.0.0.0 without a token; expected refusal")
	}
}

// TestAuth2RejectsUnauthorizedRequests covers I-AUTH-2: with a token configured,
// a missing or wrong token is rejected and — the part that matters — produces no
// execution at all. A 401 that still ran a turn would be worse than no auth.
func TestAuth2RejectsUnauthorizedRequests(t *testing.T) {
	judge(t, "I-AUTH-2", "guard")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-AUTH-2"})
	before := sessionDirs(t, e.Root)

	for _, credential := range []struct {
		name  string
		token string
	}{
		{name: "missing", token: ""},
		{name: "wrong", token: "not-the-token"},
	} {
		body, status, err := e.doWithToken("POST", "/api/run", credential.token, map[string]any{"input": "unauthorized"})
		if err != nil {
			t.Fatalf("%s token: request failed: %v", credential.name, err)
		}
		if status != 401 {
			t.Fatalf("%s token: status=%d body=%s; want 401", credential.name, status, strings.TrimSpace(string(body)))
		}
	}

	if after := sessionDirs(t, e.Root); len(after) != len(before) {
		t.Fatalf("unauthenticated requests created sessions: before=%v after=%v", before, after)
	}
	if calls := e.Mock.callCount(); calls != 0 {
		t.Fatalf("unauthenticated requests reached the model %d times; want 0", calls)
	}

	body, status, err := e.do("POST", "/api/run", map[string]any{"input": "authorized"})
	if err != nil {
		t.Fatalf("authorized request failed: %v", err)
	}
	if status != 200 {
		t.Fatalf("authorized request: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
}

// sessionDirs lists session directories; used to prove that a rejected request
// left no trace.
func sessionDirs(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, ".agents", "data", "sessions"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	return names
}

// TestAuth3HealthRequiresToken covers I-AUTH-3: the readiness endpoint is not a
// public window into a deployment.
//
// It reports how many sessions loaded and whether wake-ups are pending. On a
// non-loopback bind that is a stranger reading your activity, and it was the
// one /api route that answered without a credential.
func TestAuth3HealthRequiresToken(t *testing.T) {
	judge(t, "I-AUTH-3", "auth")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-AUTH-3"})

	body, status, err := e.doWithToken(http.MethodGet, "/api/health", "", nil)
	if err != nil {
		t.Fatalf("GET /api/health without token: %v", err)
	}
	unauthorized := strings.TrimSpace(string(body))

	authed, authedStatus, err := e.do(http.MethodGet, "/api/health", nil)
	if err != nil {
		t.Fatalf("GET /api/health with token: %v", err)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/health", Status: status},
		"", map[string]any{"unauthorized_body": unauthorized, "authorized_status": authedStatus})

	if status != http.StatusUnauthorized {
		t.Fatalf("GET /api/health without token = %d, want 401; an unauthenticated caller must not read deployment state", status)
	}
	if strings.Contains(unauthorized, "sessions_loaded") || strings.Contains(unauthorized, "pending_wakeups") {
		t.Fatalf("401 body leaks deployment state: %q", unauthorized)
	}
	if authedStatus != http.StatusOK {
		t.Fatalf("GET /api/health with token = %d, want 200 (body=%s)", authedStatus, strings.TrimSpace(string(authed)))
	}
}
