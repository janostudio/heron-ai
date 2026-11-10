//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestQueue5InputDuringApprovalIsRefusedNotQueued covers the other waiting
// state: a session parked on an approval.
//
// Queueing is for "the turn is running, your message is next". An approval is
// different — the turn cannot proceed without a decision, so storing the message
// would promise a follow-up that nothing will ever start. The message has to be
// refused with a reason the client can act on, and the session must stay usable:
// approving afterwards still has to finish the turn.
func TestQueue5InputDuringApprovalIsRefusedNotQueued(t *testing.T) {
	judge(t, "I-QUEUE-2", "dispatch")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-QUEUE-2", scriptEnabled: true})
	e.Mock.script(
		toolStep("Script", map[string]any{"code": `
			const pkg = tools.Read({file: "package.json"});
			return {len: pkg.content.length};
		`}),
		textStep("读完了"),
	)

	result := e.runTurn(map[string]any{"input": "用脚本读文件"})
	if len(result.PendingApprovals) == 0 {
		t.Fatalf("Script ran without asking for approval; status=%q", result.Turn.Status)
	}
	approval := result.PendingApprovals[0]

	body, status, err := e.do(http.MethodPost, "/api/run", map[string]any{
		"flow_session_id": result.Session.ID,
		"input":           "顺便问一句",
	})
	if err != nil {
		t.Fatalf("POST /api/run during approval: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body=%s): a message sent while an approval is pending must be refused, not queued",
			status, strings.TrimSpace(string(body)))
	}
	if !strings.Contains(string(body), "waiting_approval") {
		t.Fatalf("body = %q, want it to name the waiting state so the client knows to answer the approval", strings.TrimSpace(string(body)))
	}

	// Nothing ran and nothing was stored: the session still holds exactly the
	// one turn it started with, and the mailbox has no user message waiting.
	events := readSession(t, e.Root, result.Session.ID)
	if started := countEvents(events, "session.turn.started"); started != 1 {
		t.Fatalf("session.turn.started = %d, want 1: the refused message started a turn", started)
	}
	if queued := queuedUserMessages(events); queued != 0 {
		t.Fatalf("queued user messages = %d, want 0: a refused message must not be parked in the mailbox", queued)
	}

	// The session is still usable: approving finishes the turn.
	approveBody, approveStatus, err := e.do(http.MethodPost, "/api/approvals?session_id="+result.Session.ID, map[string]any{
		"request_id": approval.RequestID,
		"approved":   true,
		"reason":     "e2e approve",
	})
	if err != nil || approveStatus != http.StatusOK {
		t.Fatalf("POST /api/approvals: status=%d err=%v body=%s", approveStatus, err, strings.TrimSpace(string(approveBody)))
	}
	waitForEvent(t, e.Root, result.Session.ID, 25*time.Second, func(events []event) bool {
		return hasEvent(events, "session.approval.resolved") && turnTerminated(events)
	})

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: status},
		result.Session.ID, map[string]any{"refused_during_approval": true})
}

// queuedUserMessages counts user messages stored for a later turn.
func queuedUserMessages(events []event) int {
	count := 0
	for _, ev := range events {
		if ev.Type != "session.mailbox.message.enqueued" {
			continue
		}
		payload, _ := ev.Raw["payload"].(map[string]any)
		message, _ := payload["message"].(map[string]any)
		if message == nil {
			continue
		}
		if kind, _ := message["kind"].(string); kind != "user" {
			continue
		}
		count++
	}
	return count
}
