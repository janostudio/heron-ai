//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type sseFrame struct {
	ID    int64
	Event string
	Data  string
}

// stream is a live SSE subscription that keeps every frame it has seen.
//
// It records the frames rather than handing them over one by one because a case
// asserts on the whole sequence (ids increasing, no duplicates on resume, last
// frame terminal), and the stream is a durable reader: frames written before
// the subscription are replayed, so the reader and the turn can be started in
// either order.
type stream struct {
	mu     sync.Mutex
	items  []sseFrame
	stop   func()
	closed bool
}

func startStream(t *testing.T, e *env, sessionID string, lastEventID int64) *stream {
	t.Helper()
	path := "/api/stream?session_id=" + sessionID
	if lastEventID > 0 {
		path += "&last_event_id=" + strconv.FormatInt(lastEventID, 10)
	}
	return startStreamPathWithID(t, e, path, lastEventID)
}

// startStreamPath subscribes to an arbitrary SSE endpoint (task stream included).
func startStreamPath(t *testing.T, e *env, path string) *stream {
	t.Helper()
	return startStreamPathWithID(t, e, path, 0)
}

func startStreamPathWithID(t *testing.T, e *env, path string, lastEventID int64) *stream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.BaseURL+path, nil)
	if err != nil {
		cancel()
		t.Fatalf("build stream request: %v", err)
	}
	if e.Token != "" {
		req.Header.Set("Authorization", "Bearer "+e.Token)
	}
	if lastEventID > 0 {
		req.Header.Set("Last-Event-ID", strconv.FormatInt(lastEventID, 10))
	}

	s := &stream{stop: func() { cancel() }}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("open stream: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		cancel()
		t.Fatalf("stream status=%d; want 200", resp.StatusCode)
	}
	s.stop = func() {
		cancel()
		_ = resp.Body.Close()
	}
	t.Cleanup(s.stop)

	go func() {
		defer func() { _ = resp.Body.Close() }()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		var current sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id:"):
				current.ID, _ = strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "id:")), 10, 64)
			case strings.HasPrefix(line, "event:"):
				current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				current.Data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case line == "":
				if current.Data != "" {
					s.mu.Lock()
					s.items = append(s.items, current)
					s.mu.Unlock()
				}
				current = sseFrame{}
			}
		}
		// The reader reaching EOF is the evidence that the server closed the
		// response. Recording it here is the only way a case can tell "the
		// server finished" apart from "the client gave up".
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}()
	return s
}

// waitClosed blocks until the response body ends, and reports whether it did.
func (s *stream) waitClosed(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		done := s.closed
		s.mu.Unlock()
		if done {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func (s *stream) frames() []sseFrame {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]sseFrame, len(s.items))
	copy(out, s.items)
	return out
}

// waitUntil blocks until one frame satisfies the predicate.
func (s *stream) waitUntil(timeout time.Duration, predicate func(sseFrame) bool) []sseFrame {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		for _, frame := range s.frames() {
			if predicate(frame) {
				return s.frames()
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.frames()
}

// waitForTerminal blocks until the stream has delivered a turn-terminal frame
// or the deadline passes, and returns everything seen so far.
func (s *stream) waitForTerminal(timeout time.Duration) []sseFrame {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		seen := s.frames()
		if len(seen) > 0 && isTerminalFrame(seen[len(seen)-1]) {
			return seen
		}
		time.Sleep(20 * time.Millisecond)
	}
	return s.frames()
}

func isTerminalFrame(frame sseFrame) bool {
	eventType := frameTypeOf(frame.Data)
	return eventType == "session.turn.completed" || (strings.HasPrefix(eventType, "session.turn.waiting") || eventType == "session.turn.idle")
}

// frameTypeOf extracts the event type of a session frame payload.
func frameTypeOf(data string) string {
	var payload struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(data), &payload)
	return payload.Type
}

// TestStream1FramesAreOrderedAndReachTerminal covers I-STREAM-1: every frame
// carries a sequence number, the numbers only move forward, and the stream
// carries the terminal event. Without the sequence there is no resume; without
// the terminal event a subscriber waits forever.
func TestStream1FramesAreOrderedAndReachTerminal(t *testing.T) {
	judge(t, "I-STREAM-1", "stream")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STREAM-1"})
	e.Mock.script(textStep("答案"))

	result := e.runTurn(map[string]any{"input": "问题"})
	subscription := startStream(t, e, result.Session.ID, 0)
	seen := subscription.waitForTerminal(10 * time.Second)

	if len(seen) == 0 {
		t.Fatalf("stream delivered no frames for session %s", result.Session.ID)
	}
	var previous int64
	for _, frame := range seen {
		if frame.ID == 0 {
			t.Fatalf("frame without an id cannot be resumed from: %+v", frame)
		}
		if frame.ID <= previous {
			t.Fatalf("frame ids are not increasing: %d after %d", frame.ID, previous)
		}
		previous = frame.ID
	}
	last := frameTypeOf(seen[len(seen)-1].Data)
	if !isTerminalFrame(seen[len(seen)-1]) {
		t.Fatalf("stream ended without a terminal frame; last type=%q frames=%d", last, len(seen))
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/stream", Status: http.StatusOK},
		result.Session.ID, map[string]any{"frames": len(seen), "last_type": last})
}

// TestStream2ResumeDoesNotRepeatFrames covers I-STREAM-2: resuming from a
// sequence number must not re-deliver what the client already has, and must
// still deliver what comes after it. Duplicates are not cosmetic — a client
// that renders every frame shows the answer twice.
//
// The resume has to happen while there is still something to come: resuming at
// the end of a finished session legitimately yields nothing, so the case starts
// the second turn first and resumes against a stream that is still producing.
func TestStream2ResumeDoesNotRepeatFrames(t *testing.T) {
	judge(t, "I-STREAM-2", "stream")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STREAM-2"})
	e.Mock.script(textStep("第一轮答案"), step{text: "第二轮答案", delay: 300 * time.Millisecond})

	first := e.runTurn(map[string]any{"input": "第一轮问题"})

	initial := startStream(t, e, first.Session.ID, 0)
	seen := initial.waitForTerminal(10 * time.Second)
	if len(seen) < 2 {
		t.Fatalf("expected at least two frames from the first turn; got %d", len(seen))
	}
	lastSeen := seen[len(seen)-1].ID
	initial.stop()

	// Second turn in the background: its events are what the resumed stream
	// should deliver.
	turn := e.runTurnAsync(map[string]any{"flow_session_id": first.Session.ID, "input": "第二轮问题"})
	resumed := startStream(t, e, first.Session.ID, lastSeen)
	frames := resumed.waitForTerminal(15 * time.Second)

	outcome := <-turn
	if outcome.Err != nil {
		t.Fatalf("second turn failed: %v", outcome.Err)
	}
	if outcome.Status != http.StatusOK {
		t.Fatalf("second turn status=%d body=%s", outcome.Status, strings.TrimSpace(string(outcome.Body)))
	}

	if len(frames) == 0 {
		t.Fatalf("resume from %d delivered no frames while a turn was still running", lastSeen)
	}
	for _, frame := range frames {
		if frame.ID <= lastSeen {
			t.Fatalf("resume from %d re-delivered frame %d", lastSeen, frame.ID)
		}
	}
	if !isTerminalFrame(frames[len(frames)-1]) {
		t.Fatalf("resumed stream never reached the second turn's terminal frame; last=%q", frameTypeOf(frames[len(frames)-1].Data))
	}
}

// TestStream4FinishedSessionClosesWithLatestTurn covers I-STREAM-4: a
// non-following subscriber gets the newest turn and is then released.
//
// Two separate failures hide here. A stream that never closes leaves the client
// guessing when a turn ended, and one that stops at the first terminal event in
// the file replays an old answer — both look like "the stream works" until the
// client renders the wrong turn.
func TestStream4FinishedSessionClosesWithLatestTurn(t *testing.T) {
	judge(t, "I-STREAM-4", "stream")

	e := newEnv(t, envOptions{caseName: t.Name(), intent: "I-STREAM-4"})
	e.Mock.script(textStep("第一轮答案"), textStep("第二轮答案"))

	first := e.runTurn(map[string]any{"input": "第一轮问题"})
	second := e.runTurn(map[string]any{"flow_session_id": first.Session.ID, "input": "第二轮问题"})

	subscription := startStream(t, e, first.Session.ID, 0)
	closed := subscription.waitClosed(10 * time.Second)
	frames := subscription.frames()

	lastTurn := ""
	lastType := ""
	if len(frames) > 0 {
		// type and flow_turn_id sit on the event header, not inside payload.
		var header struct {
			Type       string `json:"type"`
			FlowTurnID string `json:"flow_turn_id"`
		}
		if err := json.Unmarshal([]byte(frames[len(frames)-1].Data), &header); err == nil {
			lastTurn = header.FlowTurnID
			lastType = header.Type
		}
	}

	writeTrace(t, e, httpTrace{Method: http.MethodGet, Path: "/api/stream", Status: http.StatusOK},
		first.Session.ID, map[string]any{"closed": closed, "frames": len(frames), "last_turn": lastTurn, "last_type": lastType})

	if !closed {
		t.Fatalf("subscription on a finished session never closed after 10s (%d frames); a client has no way to know the turn ended", len(frames))
	}
	if lastTurn != second.Turn.ID {
		t.Fatalf("stream ended on turn %q, want the newest turn %q (an old turn replayed as if it were the answer)", lastTurn, second.Turn.ID)
	}
	if !strings.HasPrefix(lastType, "session.turn.") {
		t.Fatalf("last frame is %q; a turn stream must end on a turn event", lastType)
	}
}
