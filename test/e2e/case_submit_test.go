//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestSubmit1AcceptsBeforeTheTurnFinishes covers the contract that makes the
// asynchronous endpoint worth having: the acceptance comes back immediately, and
// the turn runs on its own, reporting through the event log.
//
// A synchronous POST here would hold the connection for the whole model call,
// which is the state a proxy is most likely to cut.
func TestSubmit1AcceptsBeforeTheTurnFinishes(t *testing.T) {
	judge(t, "I-SUBMIT-1", "dispatch")

	const slowTurn = 1500 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-SUBMIT-1", fixture: "dispatch"})
	e.Mock.script(step{
		match: []string{"You are 接待", "慢一点"},
		delay: slowTurn,
		text:  "好",
	})

	// Open an empty session first: the endpoint names one, so it must exist.
	body, status, err := e.do(http.MethodPost, "/api/sessions", map[string]any{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("create session: status=%d err=%v body=%s", status, err, truncate(string(body), 200))
	}
	var created turnResult
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created session: %v body=%s", err, truncate(string(body), 200))
	}
	sessionID := created.Session.ID
	if sessionID == "" {
		t.Fatal("created session has no id")
	}

	start := time.Now()
	acceptedBody, acceptedStatus, err := e.do(
		http.MethodPost, "/api/sessions/"+sessionID+"/events", map[string]any{"input": "慢一点"},
	)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if acceptedStatus != http.StatusAccepted {
		t.Fatalf("submit: status=%d body=%s", acceptedStatus, truncate(string(acceptedBody), 300))
	}
	// Generous bound: the point is "not a whole model call", not a latency SLA.
	if elapsed > slowTurn/2 {
		t.Fatalf("acceptance took %v while the turn takes %v: the endpoint waited for the turn", elapsed, slowTurn)
	}

	var accepted map[string]any
	if err := json.Unmarshal(acceptedBody, &accepted); err != nil {
		t.Fatalf("decode acceptance: %v body=%s", err, truncate(string(acceptedBody), 200))
	}
	if accepted["status"] != "accepted" {
		t.Fatalf("acceptance status = %v, want accepted", accepted["status"])
	}
	if accepted["session_id"] != sessionID {
		t.Fatalf("acceptance session_id = %v, want %q", accepted["session_id"], sessionID)
	}

	// The turn must actually run, and end on its own.
	events := waitForEvent(t, e.Root, sessionID, 60*time.Second, func(events []event) bool {
		return countEvents(events, "session.turn.started") >= 1 &&
			(countEvents(events, "session.turn.idle") >= 1 || countEvents(events, "session.turn.completed") >= 1)
	})
	if countEvents(events, "session.turn.started") == 0 {
		t.Fatalf("accepted input never started a turn: %v", typesOf(events))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/sessions/{id}/events", Status: acceptedStatus},
		sessionID, map[string]any{"elapsed_ms": elapsed.Milliseconds(), "turn_slow_ms": slowTurn.Milliseconds()})
}

// TestSubmit2SecondInputIsQueuedNotRefused covers the busy case: a second
// message while the first turn runs is accepted and queued, and the queue drains
// when that turn ends.
func TestSubmit2SecondInputIsQueuedNotRefused(t *testing.T) {
	judge(t, "I-SUBMIT-2", "dispatch")

	const slowTurn = 1500 * time.Millisecond
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-SUBMIT-2", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待", "第一句"}, delay: slowTurn, text: "答复一"},
		step{match: []string{"You are 接待", "第二句"}, text: "答复二"},
	)

	body, status, err := e.do(http.MethodPost, "/api/sessions", map[string]any{})
	if err != nil || status != http.StatusOK {
		t.Fatalf("create session: status=%d err=%v", status, err)
	}
	var created turnResult
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode created session: %v", err)
	}
	sessionID := created.Session.ID

	if _, first, err := e.do(http.MethodPost, "/api/sessions/"+sessionID+"/events", map[string]any{"input": "第一句"}); err != nil || first != http.StatusAccepted {
		t.Fatalf("first submit: status=%d err=%v", first, err)
	}
	// Second submit must be accepted, not refused.
	secondBody, secondStatus, err := e.do(
		http.MethodPost, "/api/sessions/"+sessionID+"/events", map[string]any{"input": "第二句"},
	)
	if err != nil {
		t.Fatalf("second submit: %v", err)
	}
	if secondStatus != http.StatusAccepted {
		t.Fatalf("second submit while busy: status=%d body=%s", secondStatus, truncate(string(secondBody), 300))
	}

	// Both turns must run, in order.
	events := waitForEvent(t, e.Root, sessionID, 60*time.Second, func(events []event) bool {
		ended := 0
		for _, ev := range events {
			switch ev.Type {
			case "session.turn.idle", "session.turn.completed":
				ended++
			}
		}
		return ended >= 2
	})
	if started := countEvents(events, "session.turn.started"); started < 2 {
		t.Fatalf("session.turn.started = %d, want 2 (the accepted one plus the queued one); types=%v",
			started, typesOf(events))
	}

	var order []string
	for _, req := range e.Mock.requests() {
		text := requestText(req)
		for _, input := range []string{"第一句", "第二句"} {
			if strings.Contains(text, input) {
				order = appendUnique(order, input)
			}
		}
	}
	if len(order) != 2 || order[0] != "第一句" || order[1] != "第二句" {
		t.Fatalf("queued input ran as %v, want [第一句 第二句]", order)
	}
}
