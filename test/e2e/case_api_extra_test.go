//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestResult2MidStateIsNotAConclusion covers I-RESULT-1 for the state that
// matters most: right after a handoff, polling must show a turn that has no
// conclusion yet. A polling client that gets the previous turn's answer here
// renders a conclusion for work that has not started.
func TestResult2MidStateIsNotAConclusion(t *testing.T) {
	judge(t, "I-RESULT-1", "state")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RESULT-1", fixture: "dispatch"})
	e.Mock.script(dispatchScript(600*time.Millisecond, true)...)

	result := e.runTurn(map[string]any{"input": "当前版本是多少"})
	if reply := replyOf(result); reply != "" {
		t.Fatalf("handoff turn already has a conclusion %q", reply)
	}

	body, status, err := e.do(http.MethodGet, "/api/result?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("GET /api/result: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("GET /api/result: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var polled turnResult
	if err := json.Unmarshal(body, &polled); err != nil {
		t.Fatalf("decode result: %v body=%s", err, strings.TrimSpace(string(body)))
	}
	polledReply := strings.TrimSpace(firstNonEmpty(polled.FinalReply, polled.Reply))
	if strings.Contains(polledReply, "结论") || strings.Contains(polledReply, "worker") {
		t.Fatalf("polled result reports the downstream conclusion %q while the worker is still running", polledReply)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/result", Status: status},
		result.Session.ID, map[string]any{"polled_reply": polledReply, "polled_status": polled.Session.Status})
}

// TestRun5ConcurrentSessionsStayIsolated covers I-RUN-1 under concurrency: two
// sessions in flight at once must not see each other's events or answers. This
// is the failure that shows up as "the other user's answer appeared in my
// conversation".
func TestRun5ConcurrentSessionsStayIsolated(t *testing.T) {
	judge(t, "I-RUN-1", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-1"})
	e.Mock.script(
		step{match: []string{"第一个问题"}, text: "答复-甲", delay: 150 * time.Millisecond},
		step{match: []string{"第二个问题"}, text: "答复-乙", delay: 150 * time.Millisecond},
	)

	firstCh := e.runTurnAsync(map[string]any{"input": "第一个问题"})
	secondCh := e.runTurnAsync(map[string]any{"input": "第二个问题"})
	first, second := <-firstCh, <-secondCh
	if first.Err != nil || first.Status != http.StatusOK || second.Err != nil || second.Status != http.StatusOK {
		t.Fatalf("concurrent turns failed: first=%v/%v second=%v/%v", first.Status, first.Err, second.Status, second.Err)
	}
	if first.Result.Session.ID == second.Result.Session.ID {
		t.Fatalf("两个并发请求落进同一个会话 %s", first.Result.Session.ID)
	}
	if got := replyOf(first.Result); got != "答复-甲" {
		t.Fatalf("session A reply=%q; want 答复-甲 (answers must not cross sessions)", got)
	}
	if got := replyOf(second.Result); got != "答复-乙" {
		t.Fatalf("session B reply=%q; want 答复-乙", got)
	}

	for _, sessionID := range []string{first.Result.Session.ID, second.Result.Session.ID} {
		events := readSession(t, e.Root, sessionID)
		for _, ev := range events {
			if other := ev.sessionID(); other != "" && other != sessionID {
				t.Fatalf("session %s contains an event belonging to %s", sessionID, other)
			}
		}
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		first.Result.Session.ID, map[string]any{"second_session": second.Result.Session.ID})
}

// TestCancel2CancelIsIdempotent covers I-CANCEL-1's repeat half: a second cancel
// is a no-op, not an error. Clients retry; a 5xx here turns a harmless retry
// into a visible failure.
func TestCancel2CancelIsIdempotent(t *testing.T) {
	judge(t, "I-CANCEL-1", "cancel")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CANCEL-1"})
	e.Mock.script(textStep("答复"))

	result := e.runTurn(map[string]any{"input": "做一个普通回合"})

	firstBody, firstStatus, err := e.do(http.MethodPost, "/api/cancel?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	secondBody, secondStatus, err := e.do(http.MethodPost, "/api/cancel?session_id="+result.Session.ID, nil)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/cancel", Status: secondStatus},
		result.Session.ID, map[string]any{"first": firstStatus, "second": secondStatus})

	if firstStatus >= 500 || secondStatus >= 500 {
		t.Fatalf("cancel returned 5xx (first=%d %s, second=%d %s)", firstStatus, strings.TrimSpace(string(firstBody)), secondStatus, strings.TrimSpace(string(secondBody)))
	}
	if secondStatus != firstStatus {
		t.Fatalf("repeated cancel answered %d then %d; the second call is a no-op, not a different outcome", firstStatus, secondStatus)
	}
}

// TestErr3ErrorBodyExplainsProblem covers I-ERR-2: an error body has to say what
// went wrong. "EOF" alone sends the caller to the source; naming the field or
// the parse failure lets them fix it.
func TestErr3ErrorBodyExplainsProblem(t *testing.T) {
	judge(t, "I-ERR-2", "http")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-ERR-2"})

	cases := []struct {
		name        string
		contentType string
		body        []byte
	}{
		{name: "malformed_json", contentType: "application/json", body: []byte(`{"input": `)},
		{name: "wrong_content_type", contentType: "text/plain", body: []byte(`not json at all`)},
		// `{}` is a valid request now (it opens an empty session); whitespace
		// input is the one that still has to explain itself.
		{name: "whitespace_input", contentType: "application/json", body: []byte(`{"input": "   "}`)},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			body, status, err := e.doRaw(http.MethodPost, "/api/run", tc.contentType, tc.body)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if status < 400 || status >= 500 {
				t.Fatalf("%s: status=%d body=%s; want a 4xx", tc.name, status, strings.TrimSpace(string(body)))
			}
			text := strings.TrimSpace(string(body))
			if text == "" {
				t.Fatalf("%s: empty error body", tc.name)
			}
			if strings.Contains(text, "goroutine") || strings.Contains(text, ".go:") {
				t.Fatalf("%s: error body leaks internals: %q", tc.name, truncate(text, 120))
			}
			if len(text) < 4 {
				t.Fatalf("%s: error body %q says nothing", tc.name, text)
			}
		})
	}
}

// TestSessions2IndexAndRecordAreReadable covers I-RUN-4's read half: the session
// index lists what exists, and a session's record can be read back. These are
// the two endpoints a debugging client uses, so they must agree with the files.
func TestSessions2IndexAndRecordAreReadable(t *testing.T) {
	judge(t, "I-RUN-4", "http")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-4"})
	e.Mock.script(textStep("答复"))
	result := e.runTurn(map[string]any{"input": "随便问一句"})

	indexBody, indexStatus, err := e.do(http.MethodGet, "/api/sessions", nil)
	if err != nil {
		t.Fatalf("GET /api/sessions: %v", err)
	}
	if indexStatus != http.StatusOK {
		t.Fatalf("GET /api/sessions: status=%d body=%s", indexStatus, strings.TrimSpace(string(indexBody)))
	}
	if !strings.Contains(string(indexBody), result.Session.ID) {
		t.Fatalf("session index does not list the session that just ran: %s", truncate(string(indexBody), 200))
	}

	recordBody, recordStatus, err := e.do(http.MethodGet, "/api/sessions/"+result.Session.ID+"/record", nil)
	if err != nil {
		t.Fatalf("GET record: %v", err)
	}
	if recordStatus != http.StatusOK {
		t.Fatalf("GET record: status=%d body=%s", recordStatus, strings.TrimSpace(string(recordBody)))
	}
	if !json.Valid(recordBody) {
		t.Fatalf("record is not JSON: %s", truncate(string(recordBody), 200))
	}

	missingBody, missingStatus, err := e.do(http.MethodGet, "/api/sessions/fs_does_not_exist/record", nil)
	if err != nil {
		t.Fatalf("GET missing record: %v", err)
	}
	if missingStatus != http.StatusNotFound {
		t.Fatalf("unknown record: status=%d body=%s; want 404", missingStatus, strings.TrimSpace(string(missingBody)))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/sessions/{id}/record", Status: recordStatus},
		result.Session.ID, map[string]any{"index_status": indexStatus, "missing_status": missingStatus})
}
