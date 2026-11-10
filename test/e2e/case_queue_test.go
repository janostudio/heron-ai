//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestQueue1SecondInputMidTurnDispatchesAnotherTeam covers a combination nothing
// covered yet: the user sends a second message while the first turn is still
// running.
//
// Two promises meet here. The second message must not be refused — it is stored
// and run once the current turn finishes (I-QUEUE-1). And the stored message is a
// real turn, so it can dispatch a different team: one conversation carries two
// teams' work instead of the second question being quietly dropped.
//
// The first turn answers slowly on purpose. Without the delay the second message
// would arrive after the turn ended, run as an ordinary turn, and the queueing
// path would never be exercised.
func TestQueue1SecondInputMidTurnDispatchesAnotherTeam(t *testing.T) {
	judge(t, "I-QUEUE-1", "dispatch")

	const slowTurn = 2 * time.Second
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-QUEUE-1", fixture: "dispatch"})
	e.Mock.script(
		step{
			match: []string{"You are 接待", "第一个问题"},
			delay: slowTurn,
			toolCalls: []mockToolCall{{ID: "call_first", Name: "Team", Args: map[string]any{
				"subagent_type": "worker", "prompt": "办第一件事", "description": "办第一件事",
			}}},
		},
		step{match: []string{"You are 执行者"}, text: "第一件事完成"},
		step{
			match: []string{"You are 接待", "第二个问题"},
			toolCalls: []mockToolCall{{ID: "call_second", Name: "Team", Args: map[string]any{
				"subagent_type": "worker-b", "prompt": "办第二件事", "description": "办第二件事",
			}}},
		},
		step{match: []string{"You are 第二执行者"}, text: "第二件事完成"},
	)

	first := e.runTurnAsync(map[string]any{"input": "第一个问题"})

	// Wait until the first turn is actually talking to the model: only then is the
	// session busy, and only then does the second message exercise the queue.
	waitForModelRequest(t, e, "You are 接待", 20*time.Second)
	sessionID := latestSessionID(t, e)
	if sessionID == "" {
		t.Fatal("no session was created for the first input")
	}

	second := e.runTurn(map[string]any{"flow_session_id": sessionID, "input": "第二个问题"})
	if !second.Queued {
		t.Fatalf("second message was not queued while the first turn ran: reply=%q", second.Reply)
	}
	if !strings.Contains(second.Reply, "已收到") {
		t.Fatalf("queued reply = %q, want it to tell the user the message is kept", second.Reply)
	}

	// Let the first request finish so its goroutine does not leak.
	if outcome := <-first; outcome.Status != http.StatusOK {
		t.Fatalf("first turn: status=%d err=%v body=%s", outcome.Status, outcome.Err, strings.TrimSpace(string(outcome.Body)))
	}

	// The queued message has to become a turn of its own, without another request
	// from the caller: that is the whole point of storing it.
	worker := waitForModelRequest(t, e, "You are 执行者", 60*time.Second)
	workerB := waitForModelRequest(t, e, "You are 第二执行者", 60*time.Second)

	events := waitForEvent(t, e.Root, sessionID, 60*time.Second, func(events []event) bool {
		return countEvents(events, "flow_turn.started") >= 2
	})
	if started := countEvents(events, "flow_turn.started"); started < 2 {
		t.Fatalf("flow_turn.started = %d, want >= 2: the queued input never became a turn", started)
	}
	if !hasEvent(events, "mailbox.message.enqueued") {
		t.Fatal("no mailbox.message.enqueued event: the second input was not stored for the next turn")
	}

	// Two teams, one conversation: the first turn reached worker, the queued turn
	// reached worker-b. The check is on what each dispatch carried, not on what it
	// left out: a later turn legitimately sees the earlier one in its context.
	if len(worker) == 0 || len(workerB) == 0 {
		t.Fatalf("expected both teams to run; worker=%d worker-b=%d", len(worker), len(workerB))
	}
	if !strings.Contains(requestText(worker[0]), "办第一件事") {
		t.Fatalf("worker did not receive the first task: %s", truncate(requestText(worker[0]), 300))
	}
	if !strings.Contains(requestText(workerB[0]), "办第二件事") {
		t.Fatalf("worker-b did not receive the second task: %s", truncate(requestText(workerB[0]), 300))
	}

	queuedLogs := 0
	for _, line := range readProcessLog(t, e.Root) {
		if line.Msg == "user input queued behind a running turn" && line.FlowSessionID == sessionID {
			queuedLogs++
		}
	}
	if queuedLogs == 0 {
		t.Fatal("no 'user input queued behind a running turn' log line: the deferral is invisible in the process log")
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		sessionID, map[string]any{
			"turns": len(events), "worker_calls": len(worker), "worker_b_calls": len(workerB), "queued_logs": queuedLogs,
		})
}

// waitForModelRequest waits until the mock has answered a request whose text
// contains needle. Requests are how a case proves "this agent actually ran"
// without inferring it from the reply.
func waitForModelRequest(t *testing.T, e *env, needle string, timeout time.Duration) []mockRequest {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := requestsOf(e.Mock.requests(), needle); len(got) > 0 {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("no model request matched %q within %s", needle, timeout)
	return nil
}

// latestSessionID reads the session the engine just created from its own index.
// Each case runs in its own workspace, so the index has one entry.
func latestSessionID(t *testing.T, e *env) string {
	t.Helper()
	body, status, err := e.do(http.MethodGet, "/api/sessions", nil)
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var index struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(body, &index); err != nil {
		t.Fatalf("decode session index: %v", err)
	}
	if len(index.Sessions) == 0 {
		return ""
	}
	return index.Sessions[0].ID
}
