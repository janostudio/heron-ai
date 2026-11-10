//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCancel1TurnStopsAndSessionStaysUsable covers I-CANCEL-1: cancelling a turn
// ends that turn, produces no conclusion, and leaves the process able to serve
// the next one. A cancel that kills the session is only half a cancel.
func TestCancel1TurnStopsAndSessionStaysUsable(t *testing.T) {
	judge(t, "I-CANCEL-1", "cancel")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CANCEL-1"})
	e.Mock.script(
		step{text: "很慢的答复", delay: 5 * time.Second},
		textStep("取消之后的答复"),
	)

	turn := e.runTurnAsync(map[string]any{"input": "跑一个慢任务"})
	// Wait for the turn to exist before cancelling, otherwise the cancel races
	// session creation and proves nothing.
	var sessionID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && sessionID == "" {
		if dirs := sessionDirs(t, e.Root); len(dirs) > 0 {
			sessionID = dirs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatalf("turn never created a session")
	}

	body, status, err := e.do(http.MethodPost, "/api/cancel?session_id="+sessionID, nil)
	if err != nil {
		t.Fatalf("POST /api/cancel: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("cancel: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	outcome := <-turn
	if outcome.Err == nil && outcome.Status == http.StatusOK {
		if reply := replyOf(outcome.Result); strings.Contains(reply, "很慢的答复") {
			t.Fatalf("cancelled turn still produced its answer %q", reply)
		}
	}

	// The engine must still serve: a new turn on a fresh session.
	e.Mock.script(textStep("取消之后的答复"))
	next := e.runTurn(map[string]any{"input": "取消后再问一句"})
	if got := replyOf(next); got != "取消之后的答复" {
		t.Fatalf("engine unusable after cancel: reply=%q", got)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/cancel", Status: status},
		sessionID, map[string]any{"next_reply": replyOf(next)})
}

// TestRecover1InterruptedTurnIsIdentifiable covers I-RECOVERY-1: after a crash
// the engine must be able to say which turn was interrupted. Without that, a
// client cannot tell "still running" from "died with the process".
func TestRecover1InterruptedTurnIsIdentifiable(t *testing.T) {
	judge(t, "I-RECOVERY-1", "recovery")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RECOVERY-1"})
	e.Mock.script(step{text: "很慢的答复", delay: 10 * time.Second})

	_ = e.runTurnAsync(map[string]any{"input": "跑一个会被打断的任务"})

	var sessionID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && sessionID == "" {
		if dirs := sessionDirs(t, e.Root); len(dirs) > 0 {
			sessionID = dirs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatalf("turn never created a session")
	}
	// Wait until the turn actually started, so the crash lands mid-turn.
	waitForEvent(t, e.Root, sessionID, 10*time.Second, func(events []event) bool {
		return hasEvent(events, "session.turn.started")
	})

	e.killAndRestart()

	body, status, err := e.do(http.MethodGet, "/api/recovery/status?session_id="+sessionID, nil)
	if err != nil {
		t.Fatalf("GET /api/recovery/status: %v", err)
	}
	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/recovery/status", Status: status},
		sessionID, map[string]any{"body": strings.TrimSpace(string(body))})

	if status != http.StatusOK {
		t.Fatalf("recovery status: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	if !mentionsInterruption(body) {
		t.Fatalf("recovery status does not describe the interrupted work: %s", strings.TrimSpace(string(body)))
	}
}

// TestRecover2InterruptedWorkIsNotReportedAsSuccess covers I-RECOVERY-2: the
// interrupted execution must not end as a success. A turn that "completed" only
// because the process died is the most expensive kind of wrong answer.
func TestRecover2InterruptedWorkIsNotReportedAsSuccess(t *testing.T) {
	judge(t, "I-RECOVERY-2", "recovery")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RECOVERY-2"})
	e.Mock.script(step{text: "很慢的答复", delay: 10 * time.Second})

	_ = e.runTurnAsync(map[string]any{"input": "跑一个会被打断的任务"})

	var sessionID string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && sessionID == "" {
		if dirs := sessionDirs(t, e.Root); len(dirs) > 0 {
			sessionID = dirs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	if sessionID == "" {
		t.Fatalf("turn never created a session")
	}
	waitForEvent(t, e.Root, sessionID, 10*time.Second, func(events []event) bool {
		return hasEvent(events, "session.turn.started")
	})

	e.killAndRestart()

	// Recovery is explicit in this engine: inspect reports what was left
	// unfinished instead of pretending the turn completed.
	body, status, err := e.do(http.MethodPost, "/api/recovery?session_id="+sessionID, map[string]any{"action": "inspect"})
	if err != nil {
		t.Fatalf("POST /api/recovery: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("recovery: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	if !mentionsInterruption(body) {
		t.Fatalf("recovery did not report the unfinished execution: %s", strings.TrimSpace(string(body)))
	}

	events := readSession(t, e.Root, sessionID)
	reply, conclusions := conclusionOf(events)
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/recovery", Status: status},
		sessionID, map[string]any{"conclusions": conclusions, "reply": reply, "events": len(events)})

	if conclusions > 0 && strings.Contains(reply, "很慢的答复") {
		t.Fatalf("interrupted work is reported as a finished conclusion: %q", reply)
	}
}

// mentionsInterruption reports whether a recovery payload describes pending or
// interrupted work, without pinning the field names.
func mentionsInterruption(body []byte) bool {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		return false
	}
	for _, value := range payload {
		switch typed := value.(type) {
		case float64:
			if typed > 0 {
				return true
			}
		case []any:
			if len(typed) > 0 {
				return true
			}
		case string:
			if strings.TrimSpace(typed) != "" {
				return true
			}
		case bool:
			if typed {
				return true
			}
		}
	}
	return false
}
