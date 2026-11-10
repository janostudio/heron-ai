//go:build e2e

package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWrite1CreateAndReadBack covers the write path end to end: the tool creates
// a file, the next round reads it back, and the file is really on disk. Read is
// already covered on its own; the pair is what proves the workspace is shared
// between tools rather than each tool having its own view.
func TestWrite1CreateAndReadBack(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1"})
	e.Mock.script(
		toolStep("Write", map[string]any{"file": "note.txt", "content": "hello-e2e", "mode": "create"}),
		toolStep("Read", map[string]any{"file": "note.txt"}),
		textStep("写完并读回"),
	)

	result := e.runTurn(map[string]any{"input": "写个文件再读回来"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	content, err := os.ReadFile(filepath.Join(e.Root, "note.txt"))
	if err != nil {
		t.Fatalf("Write did not create note.txt: %v", err)
	}
	if string(content) != "hello-e2e" {
		t.Fatalf("note.txt content=%q; want hello-e2e", string(content))
	}

	writeEvents := toolEventsOf(events, "Write")
	if len(writeEvents) < 2 {
		t.Fatalf("expected started+completed for Write; types=%v", typesOf(events))
	}
	requests := e.Mock.requests()
	if len(requests) < 3 {
		t.Fatalf("model called %d times; want 3 (write, read, answer)", len(requests))
	}
	if !strings.Contains(requestText(requests[2]), "hello-e2e") {
		t.Fatalf("the read result never reached the model; messages=%s", e.Mock.summary())
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"bytes": len(content)})
}

// TestWrite2CreateOnExistingFileFails covers the write error path: mode=create
// must refuse an existing file, and the refusal must reach the model. A write
// that silently overwrites destroys work the model believed was preserved.
func TestWrite2CreateOnExistingFileFails(t *testing.T) {
	judge(t, "I-TOOL-3", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-3"})
	e.Mock.script(
		toolStep("Write", map[string]any{"file": "package.json", "content": "{}", "mode": "create"}),
		textStep("文件已存在，没有覆盖"),
	)

	result := e.runTurn(map[string]any{"input": "新建一个已存在的文件"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	if countEvents(events, "session.tool_call.failed") == 0 {
		t.Fatalf("create on an existing file produced no session.tool_call.failed; types=%v", typesOf(events))
	}
	original, err := os.ReadFile(filepath.Join(e.Root, "package.json"))
	if err != nil || !strings.Contains(string(original), "simple-qa-fixture") {
		t.Fatalf("package.json was overwritten by a failed create (err=%v content=%q)", err, truncate(string(original), 80))
	}

	requests := e.Mock.requests()
	if len(requests) < 2 {
		t.Fatalf("model called %d times; the failure should go back to it", len(requests))
	}
	if !strings.Contains(strings.ToLower(requestText(requests[1])), "exist") {
		t.Fatalf("failure reason did not reach the model; messages=%s", e.Mock.summary())
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"failed_events": countEvents(events, "session.tool_call.failed")})
}

// TestSearch1GlobAndGrepReturnRealHits covers the read-tool family: Glob lists
// matching paths and Grep finds content, both from the real workspace. A model
// that gets empty results from these starts guessing paths by hand.
func TestSearch1GlobAndGrepReturnRealHits(t *testing.T) {
	judge(t, "I-TOOL-1", "tool")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-TOOL-1"})
	e.Mock.script(
		toolStep("Glob", map[string]any{"pattern": "*.json"}),
		toolStep("Grep", map[string]any{"pattern": "simple-qa-fixture"}),
		textStep("找到了"),
	)

	result := e.runTurn(map[string]any{"input": "找一下 package.json 里的名字"})
	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, turnTerminated)

	for _, tool := range []string{"Glob", "Grep"} {
		if len(toolEventsOf(events, tool)) < 2 {
			t.Fatalf("%s did not run to completion; types=%v", tool, typesOf(events))
		}
	}

	requests := e.Mock.requests()
	if len(requests) < 3 {
		t.Fatalf("model called %d times; want 3 (glob, grep, answer)", len(requests))
	}
	combined := requestText(requests[len(requests)-1])
	if !strings.Contains(combined, "package.json") {
		t.Fatalf("glob output never reached the model; messages=%s", e.Mock.summary())
	}
	if !strings.Contains(combined, "simple-qa-fixture") {
		t.Fatalf("grep output never reached the model; messages=%s", e.Mock.summary())
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"model_calls": len(requests)})
}

// TestStructured1EnvelopeBecomesReadableReply covers the structured-output path
// (two-phase since 9b0f2a2): the model's JSON envelope is parsed into the
// declared schema, the reply stays human-readable text, and the fields land at
// the top level of the published record — not wrapped in a `parsed` object.
func TestStructured1EnvelopeBecomesReadableReply(t *testing.T) {
	judge(t, "I-CTX-1", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-CTX-1", fixture: "structured"})
	e.Mock.script(textStep(`{"title": "e2e 报告", "count": 3}`))

	result := e.runTurn(map[string]any{"input": "产出一份报告"})
	reply := replyOf(result)
	if reply == "" {
		t.Fatalf("structured turn produced no reply")
	}
	if strings.HasPrefix(strings.TrimSpace(reply), "{") {
		t.Fatalf("reply is the raw JSON envelope, not readable text: %q", reply)
	}

	events := waitForEvent(t, e.Root, result.Session.ID, 20*time.Second, func(events []event) bool {
		return hasEvent(events, "session.record.published") && turnTerminated(events)
	})

	var data map[string]any
	for _, ev := range events {
		if ev.Type != "session.record.published" {
			continue
		}
		payload, _ := ev.Raw["payload"].(map[string]any)
		record, _ := payload["record"].(map[string]any)
		if candidate, ok := record["data"].(map[string]any); ok {
			data = candidate
		}
	}
	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/run", Status: http.StatusOK},
		result.Session.ID, map[string]any{"reply": reply, "record_keys": keysOf(data)})

	if data == nil {
		t.Fatalf("no record data was published for the structured output")
	}
	if _, wrapped := data["parsed"]; wrapped {
		t.Fatalf("record data still wraps the envelope under `parsed`: %v", keysOf(data))
	}
	if got, _ := data["title"].(string); got != "e2e 报告" {
		t.Fatalf("record data lost the declared field: title=%v keys=%v", data["title"], keysOf(data))
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

// TestEntry1TurnAndResumePaths covers the two alternate entry points that share
// the run loop: /api/sessions/turn (continue by query id) and /api/resume
// (continue a waiting session). Both are separate code paths from /api/run, so
// testing only their error codes leaves the entry itself unverified.
func TestEntry1TurnAndResumePaths(t *testing.T) {
	judge(t, "I-RUN-3", "loop")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-RUN-3"})
	e.Mock.script(textStep("第一次答复"), textStep("turn 路径答复"), textStep("resume 路径答复"))

	first := e.runTurn(map[string]any{"input": "第一问"})

	body, status, err := e.do(http.MethodPost, "/api/sessions/turn?session_id="+first.Session.ID, map[string]any{"input": "第二问"})
	if err != nil {
		t.Fatalf("POST /api/sessions/turn: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("turn path: status=%d body=%s", status, strings.TrimSpace(string(body)))
	}
	var viaTurn turnResult
	if err := json.Unmarshal(body, &viaTurn); err != nil {
		t.Fatalf("decode turn: %v", err)
	}
	if viaTurn.Session.ID != first.Session.ID {
		t.Fatalf("turn path created a new session %q; want %q", viaTurn.Session.ID, first.Session.ID)
	}
	if got := replyOf(viaTurn); got != "turn 路径答复" {
		t.Fatalf("turn path reply=%q; want turn 路径答复", got)
	}

	resumeBody, resumeStatus, err := e.do(http.MethodPost, "/api/resume?session_id="+first.Session.ID, map[string]any{"input": "第三问"})
	if err != nil {
		t.Fatalf("POST /api/resume: %v", err)
	}
	if resumeStatus != http.StatusOK {
		t.Fatalf("resume path: status=%d body=%s", resumeStatus, strings.TrimSpace(string(resumeBody)))
	}
	var viaResume turnResult
	if err := json.Unmarshal(resumeBody, &viaResume); err != nil {
		t.Fatalf("decode resume: %v", err)
	}
	if viaResume.Session.ID != first.Session.ID {
		t.Fatalf("resume path moved to session %q; want %q", viaResume.Session.ID, first.Session.ID)
	}
	if got := replyOf(viaResume); got != "resume 路径答复" {
		t.Fatalf("resume path reply=%q; want resume 路径答复", got)
	}

	events := readSession(t, e.Root, first.Session.ID)
	if turns := countEvents(events, "session.turn.started"); turns != 3 {
		t.Fatalf("session has %d turns; want 3 (run + turn + resume)", turns)
	}

	writeTrace(t, e, httpTrace{Method: http.MethodPost, Path: "/api/sessions/turn", Status: status},
		first.Session.ID, map[string]any{"turns": 3, "resume_status": resumeStatus})
}
