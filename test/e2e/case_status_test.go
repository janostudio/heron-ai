//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestStatus1ReportsIdleWhenTurnWaitsForInput covers I-STATUS-1 in its simplest
// form: after a turn the session waits for the user, and the status endpoint
// says so. The interesting half of this promise (waiting on a tool must not be
// reported as waiting on a person) needs a dispatch, which this stage does not
// yet drive; it is asserted here against the turn's own terminal event so the
// two sources are compared rather than each trusted alone.
func TestStatus1ReportsIdleWhenTurnWaitsForInput(t *testing.T) {
	judge(t, "I-STATUS-1", "state")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STATUS-1"})
	e.Mock.script(textStep("答案"))

	result := e.runTurn(map[string]any{"input": "问题"})

	body, status, err := e.do(http.MethodGet, "/api/status?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/status: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var session struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("decode status: %v body=%s", err, strings.TrimSpace(string(body)))
	}
	if session.ID != result.Session.ID {
		t.Fatalf("status returned session %q; want %q", session.ID, result.Session.ID)
	}

	events := readSession(t, e.Root, result.Session.ID)
	turnStatus := finalTurnStatus(events)
	if turnStatus == "" {
		t.Fatalf("turn has no terminal event; types=%v", typesOf(events))
	}
	if !strings.Contains(turnStatus, "waiting_input") {
		t.Fatalf("turn ended as %q; test assumes an interactive fixture that waits for the user", turnStatus)
	}
	if session.Status != "idle" {
		t.Fatalf("status endpoint says %q while the turn ended as %q", session.Status, turnStatus)
	}
}

// TestStatus2UnknownSessionIs404 covers I-STATUS-2 and the decided error
// semantics (I-ERR-1): a missing resource is 404, never a fabricated state and
// never 400.
func TestStatus2UnknownSessionIs404(t *testing.T) {
	judge(t, "I-STATUS-2", "http")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STATUS-2"})

	body, status, err := e.do(http.MethodGet, "/api/status?session_id=fs_does_not_exist", nil)
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	if status != http.StatusNotFound {
		t.Fatalf("status=%d body=%s; want 404 for an unknown session", status, strings.TrimSpace(string(body)))
	}
	if strings.TrimSpace(string(body)) == "" {
		t.Fatalf("404 with an empty body does not say what was not found (I-ERR-2)")
	}
}

// TestResult1AgreesWithStatus covers I-RESULT-1: a polling client and a push
// client must not disagree about the same session. Two endpoints returning two
// different states is how "it worked in the log but not for the user" starts.
func TestResult1AgreesWithStatus(t *testing.T) {
	judge(t, "I-RESULT-1", "state")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RESULT-1"})
	e.Mock.script(textStep("答案"))

	result := e.runTurn(map[string]any{"input": "问题"})

	statusBody, statusCode, err := e.do(http.MethodGet, "/api/status?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("GET /api/status: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("GET /api/status: status=%d body=%s", statusCode, strings.TrimSpace(string(statusBody)))
	}

	resultBody, resultCode, err := e.do(http.MethodGet, "/api/result?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("GET /api/result: %v", err)
	}
	if resultCode != http.StatusOK {
		t.Fatalf("GET /api/result: status=%d body=%s", resultCode, strings.TrimSpace(string(resultBody)))
	}

	var fromResult turnResult
	if err := json.Unmarshal(resultBody, &fromResult); err != nil {
		t.Fatalf("decode result: %v body=%s", err, strings.TrimSpace(string(resultBody)))
	}
	var fromStatus struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(statusBody, &fromStatus); err != nil {
		t.Fatalf("decode status: %v body=%s", err, strings.TrimSpace(string(statusBody)))
	}

	if fromResult.Session.ID != fromStatus.ID {
		t.Fatalf("result reports session %q, status reports %q", fromResult.Session.ID, fromStatus.ID)
	}
	if fromResult.Session.Status != fromStatus.Status {
		t.Fatalf("result says %q, status says %q", fromResult.Session.Status, fromStatus.Status)
	}
}
