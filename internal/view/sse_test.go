package view

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestNewSSEWriter(t *testing.T) {
	t.Run("sets headers and succeeds", func(t *testing.T) {
		rr := httptest.NewRecorder()
		w, err := NewSSEWriter(rr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if w == nil {
			t.Fatal("writer is nil")
		}
		if got := rr.Header().Get("Content-Type"); got != "text/event-stream" {
			t.Fatalf("Content-Type = %q, want text/event-stream", got)
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
			t.Fatalf("Cache-Control = %q, want no-cache", got)
		}
		if got := rr.Header().Get("Connection"); got != "keep-alive" {
			t.Fatalf("Connection = %q, want keep-alive", got)
		}
		// A buffering reverse proxy delivers an SSE stream as batches, so the
		// header that disables it is part of the transport contract.
		if got := rr.Header().Get("X-Accel-Buffering"); got != "no" {
			t.Fatalf("X-Accel-Buffering = %q, want no", got)
		}
		if got := rr.Body.String(); !strings.HasPrefix(got, "retry: ") {
			t.Fatalf("stream must open with a retry directive, got %q", got)
		}
	})
}

// firstDataLine returns the payload of the first data frame. The stream opens
// with a retry directive, so the first line of the body is not the payload.
func firstDataLine(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if payload, ok := strings.CutPrefix(line, "data: "); ok {
			return payload
		}
	}
	t.Fatalf("no data frame in body: %q", body)
	return ""
}

func TestWriteEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	w, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := types.SSEEvent{Seq: 1, Type: "content", Content: "hello", CallID: "c1"}
	if err := w.WriteEvent(ev); err != nil {
		t.Fatalf("WriteEvent: %v", err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "data: ") {
		t.Fatalf("missing data prefix: %q", body)
	}
	if !strings.HasSuffix(body, "\n\n") {
		t.Fatalf("missing trailing newlines: %q", body)
	}
	var decoded types.SSEEvent
	raw := firstDataLine(t, body)
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if decoded.Type != "content" || decoded.Content != "hello" || decoded.CallID != "c1" {
		t.Fatalf("decoded = %+v", decoded)
	}
}

func TestWriteSessionEvent(t *testing.T) {
	rr := httptest.NewRecorder()
	w, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ev := storage.SessionEvent{EventHeader: types.EventHeader{Seq: 42, Type: "session.turn.completed", FlowSessionID: "s1"}}
	if err := w.WriteSessionEvent(ev); err != nil {
		t.Fatalf("WriteSessionEvent: %v", err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "id: 42\n") {
		t.Fatalf("missing id line: %q", body)
	}
	if !strings.Contains(body, "data: ") {
		t.Fatalf("missing data line: %q", body)
	}
}

func TestWriteTask(t *testing.T) {
	rr := httptest.NewRecorder()
	w, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	task := types.ToolTask{ID: "t1", Status: types.ToolTaskRunning, ToolName: "search"}
	if err := w.WriteTask(task); err != nil {
		t.Fatalf("WriteTask: %v", err)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: task\n") {
		t.Fatalf("missing event line: %q", body)
	}
	if !strings.Contains(body, "data: ") {
		t.Fatalf("missing data line: %q", body)
	}
}

func TestWriteChunk(t *testing.T) {
	rr := httptest.NewRecorder()
	w, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := w.WriteChunk("partial"); err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}
	body := rr.Body.String()
	line := firstDataLine(t, body)
	var ev types.SSEEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "content" || ev.Content != "partial" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestWriteCallChunk(t *testing.T) {
	rr := httptest.NewRecorder()
	w, err := NewSSEWriter(rr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := w.WriteCallChunk("call1", "chunk"); err != nil {
		t.Fatalf("WriteCallChunk: %v", err)
	}
	body := rr.Body.String()
	line := firstDataLine(t, body)
	var ev types.SSEEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Type != "call_output" || ev.CallID != "call1" || ev.Content != "chunk" {
		t.Fatalf("event = %+v", ev)
	}
}
