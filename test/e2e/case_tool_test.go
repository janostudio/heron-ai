//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// toolCallPayload reads the fields a case judges from a tool event.
func toolCallPayload(ev event) (name, callID, scriptID string, step int) {
	payload, _ := ev.Raw["payload"].(map[string]any)
	if payload == nil {
		return "", "", "", 0
	}
	name, _ = payload["tool_name"].(string)
	callID, _ = payload["tool_call_id"].(string)
	scriptID, _ = payload["script_call_id"].(string)
	if value, ok := payload["script_step"].(float64); ok {
		step = int(value)
	}
	return name, callID, scriptID, step
}

// toolEventsOf lists the tool events for one tool name, in order.
func toolEventsOf(events []event, name string) []event {
	var out []event
	for _, ev := range events {
		if !strings.HasPrefix(ev.Type, "session.tool_call.") {
			continue
		}
		if got, _, _, _ := toolCallPayload(ev); got != name {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// requestText flattens one captured model request into searchable text.
func requestText(request mockRequest) string {
	var builder strings.Builder
	for _, message := range request.Messages {
		builder.WriteString(message.textOf())
		builder.WriteString("\n")
	}
	return builder.String()
}

// TestTool1DirectCallRoundTrip covers I-TOOL-1: a tool call has to reach real
// execution and its result has to come back into the conversation. The mock
// asserts on the second request's contents — a reply that merely mentions the
// file proves nothing about whether the engine inflated the result.
func TestTool1DirectCallRoundTrip(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1"})
	e.Mock.script(
		toolStep("Read", map[string]any{"file": "package.json"}),
		textStep("读过 package.json 了"),
	)

	result := e.runTurn(map[string]any{"input": "看看 package.json"})

	events := waitForEvent(t, e.Root, result.Session.ID, 15*time.Second, turnTerminated)
	readEvents := toolEventsOf(events, "Read")
	if len(readEvents) < 2 {
		t.Fatalf("expected started+completed for Read, got %d tool events; types=%v", len(readEvents), typesOf(events))
	}
	if readEvents[0].Type != "session.tool_call.started" {
		t.Fatalf("first Read event is %q; want session.tool_call.started", readEvents[0].Type)
	}
	if readEvents[len(readEvents)-1].Type != "session.tool_call.completed" {
		t.Fatalf("last Read event is %q; want session.tool_call.completed", readEvents[len(readEvents)-1].Type)
	}

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; a tool loop needs 2", len(requests))
	}
	// The fixture's package.json says "simple-qa-fixture"; finding that string
	// in the second request is proof the tool's output was injected rather than
	// summarized away.
	if !strings.Contains(requestText(requests[1]), "simple-qa-fixture") {
		t.Fatalf("second request does not carry the tool result; messages=%s", e.Mock.summary())
	}
	reply := strings.TrimSpace(firstNonEmpty(result.FinalReply, result.Reply))
	if reply != "读过 package.json 了" {
		t.Fatalf("final reply=%q; want the post-tool answer", reply)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{
			"model_calls": len(requests),
			"tool_events": len(readEvents),
		})
}

// TestTool2ScriptInnerDispatch covers I-TOOL-2: the REPL tool's whole point is
// that inner calls are real tool executions, not text. The case proves it two
// ways — the inner calls appear as tool events tagged with the script identity,
// and their return values reach the model through the script's return value.
func TestTool2ScriptInnerDispatch(t *testing.T) {
	judge(t, "I-TOOL-2", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-2", scriptEnabled: true})
	e.Mock.script(
		toolStep("Script", map[string]any{"code": `
			const pkg = tools.Read({file: "package.json"});
			const readme = tools.Read({file: "README.md"});
			return {pkg_len: pkg.content.length, readme_len: readme.content.length};
		`}),
		textStep("两个文件都读了"),
	)

	result := e.runTurn(map[string]any{"input": "用脚本读两个文件"})

	// Script is a reviewed unit by design (ScriptTool.NeedsApproval == true),
	// so the first turn must stop at an approval and hand back a request id.
	// Asserting this is part of the promise: a script that ran without asking
	// would be a hole, not a shortcut.
	if len(result.PendingApprovals) == 0 {
		t.Fatalf("Script ran without asking for approval; result status=%q reply=%q",
			result.Turn.Status, strings.TrimSpace(firstNonEmpty(result.FinalReply, result.Reply)))
	}
	approval := result.PendingApprovals[0]
	if approval.ToolName != "Script" {
		t.Fatalf("approval requested for %q; want Script", approval.ToolName)
	}
	if approval.RequestID == "" {
		t.Fatalf("approval has no request id; the turn cannot be resumed")
	}

	body, status, err := e.do(http.MethodPost, "/api/approvals?session_id="+result.Session.ID, map[string]any{
		"request_id": approval.RequestID,
		"approved":   true,
		"reason":     "e2e approve",
	})
	if err != nil {
		t.Fatalf("POST /api/approvals: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("POST /api/approvals: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, func(events []event) bool {
		return hasEvent(events, "session.approval.resolved") && turnTerminated(events)
	})
	if !hasEvent(events, "session.approval.requested") {
		t.Fatalf("no session.approval.requested event; types=%v", typesOf(events))
	}

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; want 2", len(requests))
	}
	second := requestText(requests[1])
	// Execution is proven by the tool message in the next request: the script's
	// return value only exists if the inner Reads really ran.
	executed := strings.Contains(second, "pkg_len") || strings.Contains(second, "readme_len")

	scriptEvents := toolEventsOf(events, "Script")
	inner := 0
	for _, ev := range events {
		if !strings.HasPrefix(ev.Type, "session.tool_call.") {
			continue
		}
		name, _, scriptID, step := toolCallPayload(ev)
		if name == "Read" && scriptID != "" && step > 0 {
			inner++
		}
	}

	if reply := strings.TrimSpace(firstNonEmpty(result.FinalReply, result.Reply)); reply == "" {
		t.Fatalf("turn produced no reply")
	}

	// Two separate promises, reported separately so a red says which one broke:
	// did the script run, and can the run be seen in the event stream?
	if !executed {
		t.Fatalf("script never executed after approval: the next request carries no script result; messages=%s", e.Mock.summary())
	}
	if len(scriptEvents) < 2 {
		t.Fatalf("script executed but left no tool events after approval (events=%d, inner TaggedReads=%d); a reader cannot tell the script ran. types=%v",
			len(scriptEvents), inner, typesOf(events))
	}
	if inner == 0 {
		t.Fatalf("inner tool calls are not attributable to the script (no tool events carrying script_call_id); events=%v",
			typesOf(events))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{
			"model_calls":  len(requests),
			"inner_calls":  inner,
			"script_calls": len(scriptEvents),
		})
}

// TestTool3FailureIsVisibleToModel covers I-TOOL-3: a failed tool must surface
// as a failure the model can act on. A silent success is worse than an error —
// the model keeps building on a result that does not exist.
func TestTool3FailureIsVisibleToModel(t *testing.T) {
	judge(t, "I-TOOL-3", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-3"})
	e.Mock.script(
		toolStep("Read", map[string]any{"file": "no-such-file.txt"}),
		textStep("那个文件不存在"),
	)

	result := e.runTurn(map[string]any{"input": "读一个不存在的文件"})

	events := waitForEvent(t, e.Root, result.Session.ID, 15*time.Second, turnTerminated)
	failures := 0
	for _, ev := range events {
		if ev.Type == "session.tool_call.failed" {
			failures++
		}
	}
	completed := 0
	for _, ev := range toolEventsOf(events, "Read") {
		if ev.Type == "session.tool_call.completed" {
			completed++
		}
	}
	if failures == 0 {
		t.Fatalf("missing file produced no session.tool_call.failed event; types=%v", typesOf(events))
	}

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; the failure should go back to the model", len(requests))
	}
	second := strings.ToLower(requestText(requests[1]))
	if !strings.Contains(second, "no-such-file") && !strings.Contains(second, "not exist") && !strings.Contains(second, "error") {
		t.Fatalf("second request does not carry the failure reason; messages=%s", e.Mock.summary())
	}

	detail := map[string]any{"model_calls": len(requests), "failed_events": failures, "completed_events": completed}
	data, _ := json.Marshal(detail)
	t.Logf("failure detail: %s", data)
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, detail)
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// TestTool4DeniedApprovalDoesNotExecute covers I-APPROVAL-1's refusal half: a
// denied tool must not run, and the refusal must be explained to the caller.
// "Nothing happened" without a reason is indistinguishable from a dropped
// request.
func TestTool4DeniedApprovalDoesNotExecute(t *testing.T) {
	judge(t, "I-APPROVAL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-APPROVAL-1", scriptEnabled: true})
	e.Mock.script(
		toolStep("Script", map[string]any{"code": `return tools.Read({file: "package.json"}).content.length;`}),
		textStep("被拒绝了"),
	)

	result := e.runTurn(map[string]any{"input": "用脚本读文件"})
	if len(result.PendingApprovals) == 0 {
		t.Fatalf("Script did not ask for approval; status=%q", result.Turn.Status)
	}
	approval := result.PendingApprovals[0]

	body, status, err := e.do(http.MethodPost, "/api/approvals?session_id="+result.Session.ID, map[string]any{
		"request_id": approval.RequestID,
		"approved":   false,
		"reason":     "e2e deny",
	})
	if err != nil {
		t.Fatalf("POST /api/approvals: %v", err)
	}
	denyStatus := status
	denyBody := strings.TrimSpace(string(body))

	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, func(events []event) bool {
		return hasEvent(events, "session.approval.resolved") && turnTerminated(events)
	})

	executed := false
	explained := false
	for _, request := range e.Mock.requests() {
		text := requestText(request)
		if strings.Contains(text, "simple-qa-fixture") {
			executed = true
		}
		lower := strings.ToLower(text)
		if strings.Contains(lower, "denied") || strings.Contains(lower, "reject") || strings.Contains(text, "拒绝") {
			explained = true
		}
	}
	reply, _ := conclusionOf(events)

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/approvals", Status: status},
		result.Session.ID, map[string]any{"executed": executed, "explained": explained, "reply": reply})

	if executed {
		t.Fatalf("denied Script still executed (its output reached the model)")
	}
	if !explained {
		t.Fatalf("refusal never reached the model: no request carries a denial reason (reply=%q)", reply)
	}
	// Denying is a decision the client asked for; it succeeded. A 4xx here
	// labels a completed operation as a malformed request, which makes clients
	// retry a decision they already made.
	if denyStatus != http.StatusOK {
		t.Fatalf("deny returned status=%d body=%q; a denial is a successful decision, not a bad request",
			denyStatus, denyBody)
	}
}

// TestTool5ApprovalGateStopsTheWholeBatch covers I-APPROVAL-1's batch rule: when
// one call in a batch needs approval, the rest of the batch must not run before
// the decision. Partial execution is the dangerous shape — the model asked for
// two effects and got one, with no signal about which.
func TestTool5ApprovalGateStopsTheWholeBatch(t *testing.T) {
	judge(t, "I-APPROVAL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-APPROVAL-1", scriptEnabled: true})
	e.Mock.script(
		step{toolCalls: []mockToolCall{
			{ID: "call_script_1", Name: "Script", Args: map[string]any{"code": `return 1;`}},
			{ID: "call_read_1", Name: "Read", Args: map[string]any{"file": "package.json"}},
		}},
		textStep("批处理之后的答复"),
	)

	result := e.runTurn(map[string]any{"input": "同时跑脚本和读文件"})
	if len(result.PendingApprovals) != 1 {
		t.Fatalf("expected exactly one pending approval, got %d", len(result.PendingApprovals))
	}

	// Before the decision: the sibling Read must not have run. Its output would
	// show up in the transcript if it had.
	for _, request := range e.Mock.requests() {
		if strings.Contains(requestText(request), "simple-qa-fixture") {
			t.Fatalf("sibling tool ran before the approval decision; messages=%s", e.Mock.summary())
		}
	}

	approval := result.PendingApprovals[0]
	body, status, err := e.do(http.MethodPost, "/api/approvals?session_id="+result.Session.ID, map[string]any{
		"request_id": approval.RequestID,
		"approved":   true,
	})
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("approve: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, func(events []event) bool {
		return hasEvent(events, "session.approval.resolved") && turnTerminated(events)
	})

	// After the decision: both calls carry results, because the assistant
	// message already declared two tool calls and providers pair them strictly.
	last := e.Mock.requests()
	toolMessages := 0
	if len(last) > 0 {
		for _, message := range last[len(last)-1].Messages {
			if message.Role == "tool" {
				toolMessages++
			}
		}
	}
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/approvals", Status: status},
		result.Session.ID, map[string]any{"tool_messages": toolMessages, "events": len(events)})

	if toolMessages < 2 {
		t.Fatalf("after the approval only %d tool results reached the model; both declared calls need one", toolMessages)
	}
}
