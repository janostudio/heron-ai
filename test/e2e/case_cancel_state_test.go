//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestCancel3CancelLeavesNoUnfinishedExecution covers what a user means by
// "cancel": stop this turn, keep the conversation usable.
//
// The failure this catches is the opposite of a leak: the turn stops, but the
// ledger never records that the TeamTurn (and the call inside it) ended. The
// recovery check then reports unfinished execution, and the next message is
// refused with "use recovery status first" — every cancel looks like a crash to
// the person who pressed the button.
//
// Cancel ends the running turn, not the queue: a message the user sent before
// pressing cancel still runs afterwards. Replacing the earlier
// "queued input must not run after cancel" case, which pinned the crash-like
// behaviour this test rejects.
func TestCancel3CancelLeavesNoUnfinishedExecution(t *testing.T) {
	judge(t, "I-CANCEL-2", "cancel")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CANCEL-2", fixture: "dispatch"})
	e.Mock.script(
		step{match: []string{"You are 接待", "慢任务"}, delay: 3 * time.Second, text: "慢答复"},
		step{match: []string{"You are 接待", "排队问题"}, text: "排队答复", repeat: true},
		step{match: []string{"You are 接待"}, text: "答复", repeat: true},
	)

	first := e.runTurnAsync(map[string]any{"input": "慢任务"})
	waitForModelRequest(t, e, "You are 接待", 20*time.Second)
	sessionID := latestSessionID(t, e)
	if sessionID == "" {
		t.Fatal("no session was created for the first input")
	}
	// A message queued behind the turn that is about to be cancelled.
	if queued := e.runTurn(map[string]any{"flow_session_id": sessionID, "input": "排队问题"}); !queued.Queued {
		t.Fatalf("second message was not queued: reply=%q", queued.Reply)
	}

	if _, status, err := e.do(http.MethodPost, "/api/cancel?session_id="+sessionID, nil); err != nil || status != http.StatusOK {
		t.Fatalf("cancel: status=%d err=%v", status, err)
	}
	<-first
	time.Sleep(500 * time.Millisecond)

	// Nothing may be left looking unfinished.
	body, status, err := e.do(http.MethodGet, "/api/recovery/status?session_id="+sessionID, nil)
	if err != nil || status != http.StatusOK {
		t.Fatalf("recovery status: status=%d err=%v", status, err)
	}
	var recovery struct {
		Interrupted []map[string]any `json:"interrupted"`
	}
	if err := json.Unmarshal(body, &recovery); err != nil {
		t.Fatalf("decode recovery status: %v body=%s", err, truncate(string(body), 300))
	}
	if len(recovery.Interrupted) > 0 {
		t.Fatalf("cancelled session reports %d unfinished execution(s): %s", len(recovery.Interrupted), truncate(string(body), 400))
	}

	// And the conversation continues without a recovery detour.
	newBody, newStatus, err := e.do(http.MethodPost, "/api/run", map[string]any{
		"flow_session_id": sessionID, "input": "取消后再问",
	})
	if err != nil {
		t.Fatalf("POST /api/run after cancel: %v", err)
	}
	if newStatus != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s): a cancelled session must accept the next message", newStatus, strings.TrimSpace(string(newBody)))
	}

	// The message queued behind the cancelled turn is not lost: it runs once the
	// session is serving again.
	waitForModelRequest(t, e, "排队问题", 40*time.Second)

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/cancel", Status: http.StatusOK},
		sessionID, map[string]any{"unfinished_after_cancel": len(recovery.Interrupted)})
}
