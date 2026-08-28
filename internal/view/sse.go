package view

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type SSEWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
}

func NewSSEWriter(w http.ResponseWriter) (*SSEWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("streaming not supported")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
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

func (s *SSEWriter) WriteSessionEvent(event types.SessionEvent) error {
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

func (s *SSEWriter) WriteCallChunk(callID, content string) error {
	return s.WriteEvent(types.SSEEvent{
		Type:    "call_output",
		CallID:  callID,
		Content: content,
	})
}
