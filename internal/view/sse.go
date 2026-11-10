package view

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

// sseRetryMillis is the reconnection delay announced to EventSource clients.
// The value is the browser default; stating it explicitly makes the interval
// part of the contract instead of an implementation detail the client guesses.
const sseRetryMillis = 3000

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Nginx buffers proxied responses by default, which turns a live stream
	// into batches delivered when the buffer fills or the stream ends.
	w.Header().Set("X-Accel-Buffering", "no")

	// The retry directive must precede the first event: a client that
	// reconnects mid-stream has already read it by then. Writing it before the
	// first flush also gets the response headers to the client immediately.
	if _, err := fmt.Fprintf(w, "retry: %d\n\n", sseRetryMillis); err != nil {
		return nil, err
	}
	flusher.Flush()

	return &SSEWriter{w: w, flusher: flusher}, nil
}

func (s *SSEWriter) WriteEvent(event types.SSEEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	_, err = s.w.Write([]byte("data: " + string(data) + "\n\n"))
	if err != nil {
		return err
	}

	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteSessionEvent(event storage.SessionEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "id: %d\ndata: %s\n\n", event.Seq, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteTask(task types.ToolTask) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "event: task\ndata: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteChunk(content string) error {
	return s.WriteEvent(types.SSEEvent{
		Type:    "content",
		Content: content,
	})
}

// WriteComment emits an SSE comment frame.
//
// It exists for heartbeat, not for data: a comment is the only frame a client
// is required to ignore, so it keeps a long-lived stream from being closed by
// an intermediary that treats an idle connection as dead without inventing an
// event type that every consumer would then have to filter out.
func (s *SSEWriter) WriteComment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *SSEWriter) WriteCallChunk(callID, content string) error {
	return s.WriteEvent(types.SSEEvent{
		Type:    "call_output",
		CallID:  callID,
		Content: content,
	})
}
