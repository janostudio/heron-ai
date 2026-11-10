//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestQueue6RestartKeepsQueuedInputUntilRecovery covers the crash case: a message
// queued behind a running turn must survive the process dying with it.
//
// The sequence is the promise. After a crash the session is `interrupted`, the
// stored message is still there, and nothing runs on its own — a process that
// starts work it never got permission to start after a crash is worse than one
// that waits. Recovery is explicit: only then does the stored message run, and
// only then does the session accept new input again.
func TestQueue6RestartKeepsQueuedInputUntilRecovery(t *testing.T) {
	judge(t, "I-QUEUE-3", "dispatch")

	const slowTurn = 4 * time.Second
	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-QUEUE-3", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待", "慢任务"}, delay: slowTurn, text: "慢答复"},
		step{match: []string{"You are 接待", "排队问题"}, text: "排队答复", repeat: true},
		step{match: []string{"You are 接待"}, text: "恢复后的答复", repeat: true},
	)

	first := e.runTurnAsync(map[string]any{"input": "慢任务"})
	waitForModelRequest(t, e, "You are 接待", 20*time.Second)
	sessionID := latestSessionID(t, e)
	if sessionID == "" {
		t.Fatal("no session was created for the first input")
	}

	second := e.runTurn(map[string]any{"flow_session_id": sessionID, "input": "排队问题"})
	if !second.Queued {
		t.Fatalf("second message was not queued while the first turn ran: reply=%q", second.Reply)
	}

	e.killAndRestart()
	<-first // the killed turn's caller gets an error; reading it keeps the goroutine from leaking

	// The crash leaves the session interrupted and the message stored, not run.
	body, status, err := e.do(http.MethodGet, "/api/status?session_id="+sessionID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("status after restart: status=%d err=%v", status, err)
	}
	if !strings.Contains(string(body), "interrupted") {
		t.Fatalf("session status = %s, want interrupted: a crash must not look like a finished turn", truncate(string(body), 200))
	}

	time.Sleep(2 * time.Second)
	events := readSession(t, e.Root, sessionID)
	if started := countEvents(events, "flow_turn.started"); started != 1 {
		t.Fatalf("flow_turn.started = %d, want 1: the restored process started the queued message without being asked", started)
	}
	if queued := queuedUserMessages(events); queued != 1 {
		t.Fatalf("queued user messages = %d, want 1: the message stored before the crash is gone", queued)
	}

	// New input is refused until the interrupted execution is dealt with: the
	// session cannot answer while its state is unknown.
	newBody, newStatus, err := e.do(http.MethodPost, "/api/run", map[string]any{
		"flow_session_id": sessionID, "input": "重启后新问题",
	})
	if err != nil {
		t.Fatalf("POST /api/run after restart: %v", err)
	}
	if newStatus != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s): a session with unfinished execution must ask for recovery first",
			newStatus, strings.TrimSpace(string(newBody)))
	}
	if !strings.Contains(strings.ToLower(string(newBody)), "recovery") {
		t.Fatalf("body = %q, want it to point at recovery", strings.TrimSpace(string(newBody)))
	}

	// Recovery is what lets the stored message run. Wait for the message itself
	// rather than for a turn count: the number of turns recovery produces is an
	// implementation detail, "the stored question was asked" is the promise.
	recoverBody, recoverStatus, err := e.do(http.MethodPost, "/api/recovery?session_id="+sessionID, map[string]any{
		"action": "coordinate",
	})
	if err != nil || recoverStatus != http.StatusOK {
		t.Fatalf("POST /api/recovery: status=%d err=%v body=%s", recoverStatus, err, strings.TrimSpace(string(recoverBody)))
	}
	waitForModelRequest(t, e, "排队问题", 45*time.Second)

	// And the session is usable again.
	finalBody, finalStatus, err := e.do(http.MethodPost, "/api/run", map[string]any{
		"flow_session_id": sessionID, "input": "恢复后再问",
	})
	if err != nil || finalStatus != http.StatusOK {
		t.Fatalf("POST /api/run after recovery: status=%d err=%v body=%s", finalStatus, err, strings.TrimSpace(string(finalBody)))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/recovery", Status: recoverStatus},
		sessionID, map[string]any{"queued_survived_crash": true, "recovered": true})
}
