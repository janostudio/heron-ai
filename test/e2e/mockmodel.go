//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockModel is a scripted OpenAI-compatible endpoint. A case decides what the
// model says by queuing steps; the mock records every request so a case can
// assert on what the engine actually sent (tool results echoed back, context
// carried over) instead of inferring it from the reply text.
type mockModel struct {
	URL string

	srv *httptest.Server

	mu    sync.Mutex
	reqs  []mockRequest
	steps []step
	step  int
}

type mockRequest struct {
	Index      int
	StartedAt  time.Time
	FinishedAt time.Time
	Model      string
	Stream     bool
	Messages   []mockMessage
	Tools      []string
	Raw        json.RawMessage
}

type mockMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
	Name       string          `json:"name"`
}

// textOf flattens content whether the provider sent a string or an array of
// parts; the engine uses both shapes depending on provenance.
func (m mockMessage) textOf() string {
	if len(m.Content) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(m.Content, &text) == nil {
		return text
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.Text)
		}
		return b.String()
	}
	return string(m.Content)
}

// step is one scripted model response.
//
// match, when set, selects the step by request content instead of arrival
// order. Dispatch cases need it: the caller's follow-up round and the
// dispatched team's first round run concurrently, so "next step in the list"
// binds responses to the wrong agent on some runs.
type step struct {
	match        []string
	text         string
	toolCalls    []mockToolCall
	delay        time.Duration
	stream       bool
	emptyChoices int // chunks with an empty choices array (usage-only frames)
	rejectParam  string
	// repeat keeps a matching step available, which is what a conclusion
	// template needs: the woken turn asks the same agent again.
	repeat bool
	used   bool
}

type mockToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

func textStep(text string) step { return step{text: text} }

func toolStep(name string, args map[string]any) step {
	return step{toolCalls: []mockToolCall{{ID: "call_e2e_1", Name: name, Args: args}}}
}

func newMockModel(t *testing.T) *mockModel {
	t.Helper()
	m := &mockModel{}
	m.srv = httptest.NewServer(http.HandlerFunc(m.handle))
	m.URL = m.srv.URL
	t.Cleanup(m.srv.Close)
	return m
}

// script replaces the response sequence for this mock.
func (m *mockModel) script(steps ...step) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for index := range steps {
		steps[index].used = false
	}
	m.steps = steps
	m.step = 0
}

// pickLocked chooses the response for one request: a matching step wins, and
// an unmatched step is consumed in order. Callers hold m.mu.
func (m *mockModel) pickLocked(req mockRequest) step {
	text := ""
	for _, message := range req.Messages {
		text += message.textOf() + "\n"
	}

	for index := range m.steps {
		candidate := &m.steps[index]
		if len(candidate.match) == 0 || candidate.used {
			continue
		}
		matched := true
		for _, needle := range candidate.match {
			if !strings.Contains(text, needle) {
				matched = false
				break
			}
		}
		if matched {
			if !candidate.repeat {
				candidate.used = true
			}
			return *candidate
		}
	}
	for index := range m.steps {
		candidate := &m.steps[index]
		if len(candidate.match) > 0 || candidate.used {
			continue
		}
		candidate.used = true
		return *candidate
	}
	return step{text: "e2e-answer"}
}

func (m *mockModel) handle(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)

	var payload struct {
		Model    string        `json:"model"`
		Stream   bool          `json:"stream"`
		Messages []mockMessage `json:"messages"`
		Tools    []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	_ = json.Unmarshal(raw, &payload)

	req := mockRequest{Model: payload.Model, Stream: payload.Stream, Messages: payload.Messages, Raw: raw, StartedAt: time.Now()}
	for _, tool := range payload.Tools {
		req.Tools = append(req.Tools, tool.Function.Name)
	}

	m.mu.Lock()
	req.Index = len(m.reqs)
	m.reqs = append(m.reqs, req)
	current := m.pickLocked(req)
	m.mu.Unlock()

	finish := func() {
		m.mu.Lock()
		m.reqs[req.Index].FinishedAt = time.Now()
		m.mu.Unlock()
	}
	defer finish()

	if current.rejectParam != "" {
		// Mirrors a gateway rejecting an unsupported field: the engine is
		// expected to retry without it (I-loop-3's degradation branch).
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"unsupported parameter: ` + current.rejectParam + `","type":"invalid_request_error"}}`))
		return
	}
	if current.delay > 0 {
		// Honour cancellation: a cancelled turn hangs up, and a handler that keeps
		// sleeping holds the connection open until httptest.Server.Close waits for
		// it. That turns every cancel case into a five-second case and hides how
		// fast the cancel actually was.
		select {
		case <-r.Context().Done():
			return
		case <-time.After(current.delay):
		}
	}

	if current.stream {
		m.writeStream(w, current)
		return
	}
	m.writeJSON(w, current)
}

func (m *mockModel) writeJSON(w http.ResponseWriter, current step) {
	message := map[string]any{"role": "assistant", "content": current.text}
	finish := "stop"
	if len(current.toolCalls) > 0 {
		message["content"] = ""
		message["tool_calls"] = wireToolCalls(current.toolCalls)
		finish = "tool_calls"
	}
	body := map[string]any{
		"id":      "chatcmpl-e2e",
		"object":  "chat.completion",
		"created": 0,
		"model":   "mock",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
		"usage": map[string]any{
			"prompt_tokens":     11,
			"completion_tokens": 7,
			"total_tokens":      18,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (m *mockModel) writeStream(w http.ResponseWriter, current step) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	send := func(payload any) {
		data, _ := json.Marshal(payload)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}

	if len(current.toolCalls) > 0 {
		call := current.toolCalls[0]
		args, _ := json.Marshal(call.Args)
		send(map[string]any{"choices": []any{map[string]any{
			"delta": map[string]any{"tool_calls": []any{map[string]any{
				"index": 0, "id": call.ID, "type": "function",
				"function": map[string]any{"name": call.Name, "arguments": string(args)},
			}}},
			"finish_reason": "tool_calls",
		}}})
	} else {
		for _, chunk := range splitChunks(current.text) {
			send(map[string]any{"choices": []any{map[string]any{
				"delta":         map[string]any{"content": chunk},
				"finish_reason": "",
			}}})
		}
		send(map[string]any{"choices": []any{map[string]any{
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}}})
	}

	// Usage-only frames: choices empty, usage present. The engine has to keep
	// reading after them (I-loop-3).
	for i := 0; i < current.emptyChoices; i++ {
		send(map[string]any{"choices": []any{}, "usage": map[string]any{
			"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
		}})
	}
	send(map[string]any{"choices": []any{}, "usage": map[string]any{
		"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18,
	}})
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func wireToolCalls(calls []mockToolCall) []any {
	var out []any
	for _, call := range calls {
		args, _ := json.Marshal(call.Args)
		out = append(out, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": string(args),
			},
		})
	}
	return out
}

// splitChunks splits on runes, not bytes: a chunk boundary inside a multibyte
// character corrupts the text the engine reassembles, and the failure looks
// like a model problem rather than a mock one.
func splitChunks(text string) []string {
	if text == "" {
		return nil
	}
	const size = 8
	runes := []rune(text)
	var chunks []string
	for len(runes) > size {
		chunks = append(chunks, string(runes[:size]))
		runes = runes[size:]
	}
	return append(chunks, string(runes))
}

// requests returns what the engine sent so far.
func (m *mockModel) requests() []mockRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]mockRequest, len(m.reqs))
	copy(out, m.reqs)
	return out
}

func (m *mockModel) callCount() int {
	return len(m.requests())
}

// summary is what lands in the artifacts directory on failure: enough to see
// which turn the engine was on and what it had in context.
func (m *mockModel) summary() string {
	reqs := m.requests()
	var b strings.Builder
	fmt.Fprintf(&b, "requests: %d\n", len(reqs))
	for _, req := range reqs {
		fmt.Fprintf(&b, "\n--- request %d model=%s stream=%v tools=%v\n", req.Index, req.Model, req.Stream, req.Tools)
		for _, msg := range req.Messages {
			fmt.Fprintf(&b, "  [%s] %s\n", msg.Role, truncate(msg.textOf(), 300))
		}
	}
	return b.String()
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}
