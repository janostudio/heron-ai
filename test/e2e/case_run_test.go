//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRun1SingleTurnProducesTerminalResult covers I-RUN-1: one request produces
// one turn that reaches a state a client can act on, and the reply is the
// model's answer — not a status blob, not empty.
func TestRun1SingleTurnProducesTerminalResult(t *testing.T) {
	judge(t, "I-RUN-1", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-1"})
	e.Mock.script(textStep("e2e-answer-1"))

	started := time.Now()
	body, status, err := e.do(http.MethodPost, "/api/run", map[string]any{"input": "第一个问题"})
	if err != nil {
		t.Fatalf("POST /api/run: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s", status, strings.TrimSpace(string(body)))
	}

	var result turnResult
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v body=%s", err, strings.TrimSpace(string(body)))
	}
	if result.Session.ID == "" {
		t.Fatalf("no session id in response: %s", strings.TrimSpace(string(body)))
	}
	writeTrace(t, e, httpTrace{
		Method: http.MethodPost, Path: "/api/run", Status: status,
		DurationMS: time.Since(started).Milliseconds(), BodyLen: len(body),
	}, result.Session.ID, nil)

	reply := strings.TrimSpace(result.FinalReply)
	if reply == "" {
		reply = strings.TrimSpace(result.Reply)
	}
	if reply == "" {
		t.Fatalf("empty reply; result=%s", strings.TrimSpace(string(body)))
	}
	if reply != "e2e-answer-1" {
		t.Fatalf("reply=%q; want the model's answer %q", reply, "e2e-answer-1")
	}

	events := waitForEvent(t, e.Root, result.Session.ID, 10*time.Second, turnTerminated)
	if !hasEvent(events, "session.agent.model_response") {
		t.Fatalf("no session.agent.model_response event; types=%v", typesOf(events))
	}
	if status := finalTurnStatus(events); status == "" {
		t.Fatalf("turn never reached a terminal status; types=%v", typesOf(events))
	}
}

// TestRun2RejectsEmptyInputWithoutExecuting covers I-RUN-2: a request with no
// content must not start a turn. "Rejected" and "created a session that then
// failed" are different outcomes for the caller.
func TestRun2RejectsEmptyInputWithoutExecuting(t *testing.T) {
	judge(t, "I-RUN-2", "http")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-2"})
	before := sessionDirs(t, e.Root)

	body, status, err := e.do(http.MethodPost, "/api/run", nil)
	if err != nil {
		t.Fatalf("POST /api/run: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("empty body: status=%d body=%s; want 400", status, strings.TrimSpace(string(body)))
	}

	malformed := []byte(`{"input": `)
	body, status, err = e.doRaw(http.MethodPost, "/api/run", "application/json", malformed)
	if err != nil {
		t.Fatalf("POST malformed: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Fatalf("malformed body: status=%d body=%s; want 400", status, strings.TrimSpace(string(body)))
	}

	if after := sessionDirs(t, e.Root); len(after) != len(before) {
		t.Fatalf("rejected requests created sessions: before=%v after=%v", before, after)
	}
	if calls := e.Mock.callCount(); calls != 0 {
		t.Fatalf("rejected requests reached the model %d times; want 0", calls)
	}
}

// TestRun3SecondTurnSeesFirstInput covers I-RUN-3 at the level the fixture
// actually promises. The flow declares `inputs.user_message: true`, so a
// continuation must carry the earlier user input; whether the earlier *answer*
// is also visible depends on the flow declaring records as inputs, which this
// fixture does not — that promise is I-CTX-1 and needs a dispatch fixture.
//
// Scoping the assertion to the configured inputs is not a relaxation: asserting
// answer carry-over here would fail for a configuration reason and teach the
// wrong lesson.
func TestRun3SecondTurnSeesFirstInput(t *testing.T) {
	judge(t, "I-RUN-3", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-3"})
	e.Mock.script(textStep("第一轮答案"), textStep("第二轮答案"))

	first := e.runTurn(map[string]any{"input": "第一轮问题"})
	second := e.runTurn(map[string]any{"flow_session_id": first.Session.ID, "input": "第二轮问题"})

	if second.Session.ID != first.Session.ID {
		t.Fatalf("continuation created a new session: first=%s second=%s", first.Session.ID, second.Session.ID)
	}

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; want 2", len(requests))
	}
	secondRequest := requests[len(requests)-1]
	var history strings.Builder
	for _, message := range secondRequest.Messages {
		history.WriteString(message.textOf())
		history.WriteString("\n")
	}
	text := history.String()
	if !strings.Contains(text, "第一轮问题") {
		t.Fatalf("second request lost the first turn's input; messages=%s", e.Mock.summary())
	}
	if strings.TrimSpace(second.FinalReply) == "" && strings.TrimSpace(second.Reply) == "" {
		t.Fatalf("second turn produced no reply")
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		second.Session.ID, map[string]any{
			"model_calls":     len(requests),
			"answers_visible": strings.Contains(text, "第一轮答案"),
			"note":            "answer carry-over is I-CTX-1, judged with a record-declaring fixture",
		})
}

// TestRun4GetReadsIndexWithoutExecuting covers I-RUN-4: GET is a read. The same
// path serves the session index and the turn API, so the method is what decides
// meaning — and a read must not start an agent turn.
func TestRun4GetReadsIndexWithoutExecuting(t *testing.T) {
	judge(t, "I-RUN-4", "http")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-4"})
	before := sessionDirs(t, e.Root)

	body, status, err := e.do(http.MethodGet, "/api/sessions", nil)
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s; want 200", status, strings.TrimSpace(string(body)))
	}
	if !json.Valid(body) {
		t.Fatalf("GET /api/sessions returned non-JSON: %s", strings.TrimSpace(string(body)))
	}
	if after := sessionDirs(t, e.Root); len(after) != len(before) {
		t.Fatalf("GET created sessions: before=%v after=%v", before, after)
	}
	if calls := e.Mock.callCount(); calls != 0 {
		t.Fatalf("GET reached the model %d times; want 0", calls)
	}
}
