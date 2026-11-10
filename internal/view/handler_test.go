package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ===== requestContextBlocks =====

func TestRequestContextBlocks(t *testing.T) {
	tests := []struct {
		name       string
		req        flowInputRequest
		wantBlocks int
		wantText   string
		wantParts  int
		wantErr    bool
	}{
		{
			name:       "empty request returns nil",
			req:        flowInputRequest{},
			wantBlocks: 0,
		},
		{
			name:       "string content only",
			req:        flowInputRequest{Content: json.RawMessage(`"hello world"`)},
			wantBlocks: 1,
			wantText:   "hello world",
		},
		{
			name:       "content envelope with content field",
			req:        flowInputRequest{Content: json.RawMessage(`{"content":"nested text"}`)},
			wantBlocks: 1,
			wantText:   "nested text",
		},
		{
			name:       "content blocks array with text",
			req:        flowInputRequest{Content: json.RawMessage(`[{"type":"text","text":"part1"},{"type":"text","text":"part2"}]`)},
			wantBlocks: 1,
			wantText:   "part1part2",
		},
		{
			name:       "image content block produces part",
			req:        flowInputRequest{Content: json.RawMessage(`[{"type":"image","source":{"type":"url","url":"http://example.com/a.png"}}]`)},
			wantBlocks: 1,
			wantParts:  1,
		},
		{
			// The agent takes its input text from the block, so a request that only
			// has attachments must carry the words too — otherwise "看图" reaches
			// the model as an empty message with a picture attached.
			name: "attachments keep the text that came with them",
			req: flowInputRequest{
				Input:       "看图",
				Attachments: []types.MediaAttachment{{SourceType: "base64", DataBase64: "aGk=", Kind: "image"}},
			},
			wantBlocks: 1,
			wantText:   "看图",
			wantParts:  1,
		},
		{
			name:    "invalid content returns error",
			req:     flowInputRequest{Content: json.RawMessage(`{"type":"bogus"}`)},
			wantErr: true,
		},
		{
			name: "attachment with base64 infers source type",
			req: flowInputRequest{Attachments: []types.MediaAttachment{
				{DataBase64: "aGVsbG8=", Kind: "file"},
			}},
			wantBlocks: 1,
			wantParts:  1,
		},
		{
			name: "attachment with path infers source type",
			req: flowInputRequest{Attachments: []types.MediaAttachment{
				{Path: "/tmp/x.txt"},
			}},
			wantBlocks: 1,
			wantParts:  1,
		},
		{
			name: "attachment with url infers source type",
			req: flowInputRequest{Attachments: []types.MediaAttachment{
				{URL: "http://example.com/x.txt"},
			}},
			wantBlocks: 1,
			wantParts:  1,
		},
		{
			name: "attachment with explicit source type preserved",
			req: flowInputRequest{Attachments: []types.MediaAttachment{
				{SourceType: "stored", Path: "/x"},
			}},
			wantBlocks: 1,
			wantParts:  1,
		},
		{
			name: "attachment empty kind defaults to file",
			req: flowInputRequest{Attachments: []types.MediaAttachment{
				{URL: "http://example.com/x.txt"},
			}},
			wantBlocks: 1,
			wantParts:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks, err := requestContextBlocks(tt.req)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(blocks) != tt.wantBlocks {
				t.Fatalf("block count = %d, want %d", len(blocks), tt.wantBlocks)
			}
			if tt.wantBlocks == 0 {
				return
			}
			b := blocks[0]
			if b.Text != tt.wantText {
				t.Fatalf("text = %q, want %q", b.Text, tt.wantText)
			}
			if len(b.Parts) != tt.wantParts {
				t.Fatalf("parts = %d, want %d", len(b.Parts), tt.wantParts)
			}
			if b.Kind != "input" {
				t.Fatalf("kind = %q, want input", b.Kind)
			}
			if b.Source != "http" {
				t.Fatalf("source = %q, want http", b.Source)
			}
		})
	}
}

func TestRequestContextBlocksAttachmentSourceInference(t *testing.T) {
	// Verify the inferred source type on each attachment branch.
	req := flowInputRequest{Attachments: []types.MediaAttachment{
		{DataBase64: "aGVsbG8="},
		{Path: "/tmp/a.txt"},
		{URL: "http://example.com/b.txt"},
		{SourceType: "stored"},
	}}
	blocks, err := requestContextBlocks(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	parts := blocks[0].Parts
	if len(parts) != 4 {
		t.Fatalf("parts = %d, want 4", len(parts))
	}
	wantTypes := []string{"base64", "path", "url", "stored"}
	for i, want := range wantTypes {
		if parts[i].Media == nil {
			t.Fatalf("parts[%d].Media is nil", i)
		}
		if parts[i].Media.SourceType != want {
			t.Fatalf("parts[%d].SourceType = %q, want %q", i, parts[i].Media.SourceType, want)
		}
		if parts[i].Type != "file" {
			t.Fatalf("parts[%d].Type = %q, want file", i, parts[i].Type)
		}
	}
}

// ===== contentBlockText =====

func TestContentBlockText(t *testing.T) {
	tests := []struct {
		name   string
		blocks []types.ContextBlock
		want   string
	}{
		{"empty", nil, ""},
		{"single text", []types.ContextBlock{{Text: "a"}}, "a"},
		{"multiple joined", []types.ContextBlock{{Text: "a"}, {Text: "b"}, {Text: "c"}}, "a\n\nb\n\nc"},
		{"skips blank", []types.ContextBlock{{Text: "a"}, {Text: "   "}, {Text: "b"}}, "a\n\nb"},
		{"whitespace only skipped", []types.ContextBlock{{Text: "  \n "}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contentBlockText(tt.blocks); got != tt.want {
				t.Fatalf("contentBlockText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ===== mock runtime =====

type mockRuntime struct {
	startCalls    int
	handleCalls   int
	resumeCalls   int
	cancelCalls   int
	statusCalls   int
	lastInput     string
	lastSessionID string
	lastBlocks    []types.ContextBlock
	result        types.FlowTurnResult
	err           error
	statusResult  types.FlowSession
	statusErr     error
	rich          bool
}

func (m *mockRuntime) Start(ctx context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	m.startCalls++
	m.lastInput = req.Input
	m.lastBlocks = req.ContextBlocks
	return m.result, m.err
}
func (m *mockRuntime) HandleInput(ctx context.Context, sessionID, input string) (types.FlowTurnResult, error) {
	m.handleCalls++
	m.lastSessionID = sessionID
	m.lastInput = input
	return m.result, m.err
}
func (m *mockRuntime) Resume(ctx context.Context, sessionID, input string) (types.FlowTurnResult, error) {
	m.resumeCalls++
	m.lastInput = input
	return m.result, m.err
}
func (m *mockRuntime) Cancel(ctx context.Context, sessionID string) error {
	m.cancelCalls++
	return m.err
}
func (m *mockRuntime) Status(ctx context.Context, sessionID string) (types.FlowSession, error) {
	m.statusCalls++
	return m.statusResult, m.statusErr
}
func (m *mockRuntime) HandleInputWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	m.handleCalls++
	m.lastSessionID = sessionID
	m.lastInput = input
	m.lastBlocks = blocks
	return m.result, m.err
}
func (m *mockRuntime) ResumeWithContext(ctx context.Context, sessionID, input string, blocks []types.ContextBlock) (types.FlowTurnResult, error) {
	m.resumeCalls++
	m.lastInput = input
	m.lastBlocks = blocks
	return m.result, m.err
}

// ===== HandleResult =====

func TestHandleResult(t *testing.T) {
	t.Run("missing session_id", func(t *testing.T) {
		h := NewRuntimeHandler(&mockRuntime{})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("unreadable state maps to 500, not to a gone session", func(t *testing.T) {
		// The failure this guards: an internal error reported as 404 tells the
		// client the session no longer exists, so it drops work a retry could
		// still reach. Only "not found" may answer 404.
		rt := &mockRuntime{statusErr: context.DeadlineExceeded}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result?session_id=s1", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
		if body := rr.Body.String(); !strings.Contains(body, "s1") {
			t.Fatalf("body = %q, want it to name the session", body)
		}
	})

	t.Run("success wraps session in FlowTurnResult", func(t *testing.T) {
		rt := &mockRuntime{statusResult: types.FlowSession{ID: "s1", FlowID: "f1", Status: types.SessionIdle}}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result?session_id=s1", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		var out types.FlowTurnResult
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out.Session.ID != "s1" {
			t.Fatalf("session id = %q, want s1", out.Session.ID)
		}
	})

	t.Run("nil runtime returns 503", func(t *testing.T) {
		h := &Handler{} // runtime explicitly nil
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result?session_id=s1", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("unconfigured runtime maps to 500", func(t *testing.T) {
		h := NewHandler() // unconfiguredRuntime returns error from Status
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result?session_id=s1", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
		}
	})

	t.Run("missing session maps to 404 and names it", func(t *testing.T) {
		rt := &mockRuntime{statusErr: fmt.Errorf("read state: %w", storage.ErrNotFound)}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/result?session_id=s1", nil)
		h.HandleResult(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
		}
		if body := rr.Body.String(); !strings.Contains(body, `"s1"`) {
			t.Fatalf("body = %q, want a 404 that quotes the session id", body)
		}
	})
}

// ===== HandleResume =====

func TestHandleResume(t *testing.T) {
	t.Run("nil runtime returns 503", func(t *testing.T) {
		h := &Handler{} // runtime explicitly nil
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume?session_id=s1", strings.NewReader(`{"input":"hi"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("unconfigured runtime resume error returns 400", func(t *testing.T) {
		h := NewHandler() // unconfiguredRuntime returns error from Resume
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume?session_id=s1", strings.NewReader(`{"input":"hi"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("missing session_id", func(t *testing.T) {
		h := NewRuntimeHandler(&mockRuntime{})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume", strings.NewReader(`{"input":"hi"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("success uses plain Resume", func(t *testing.T) {
		rt := &mockRuntime{result: types.FlowTurnResult{Reply: "ok"}}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume?session_id=s1", strings.NewReader(`{"input":"hi"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		if rt.resumeCalls != 1 {
			t.Fatalf("resumeCalls = %d, want 1", rt.resumeCalls)
		}
		if rt.lastInput != "hi" {
			t.Fatalf("lastInput = %q, want hi", rt.lastInput)
		}
	})

	t.Run("rich runtime uses ResumeWithContext", func(t *testing.T) {
		rt := &mockRuntime{result: types.FlowTurnResult{Reply: "ok"}, rich: true}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume?session_id=s1", strings.NewReader(`{"content":"resume text"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		if rt.lastBlocks == nil {
			t.Fatalf("expected context blocks to be passed")
		}
		if rt.lastInput != "resume text" {
			t.Fatalf("lastInput = %q, want resume text", rt.lastInput)
		}
	})

	t.Run("runtime error returns 400", func(t *testing.T) {
		rt := &mockRuntime{err: context.DeadlineExceeded}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/resume?session_id=s1", strings.NewReader(`{"input":"hi"}`))
		h.HandleResume(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})
}

// ===== HandleCancel =====

func TestHandleCancel(t *testing.T) {
	t.Run("nil runtime returns 503", func(t *testing.T) {
		h := &Handler{} // runtime explicitly nil
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/cancel?session_id=s1", nil)
		h.HandleCancel(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("missing session_id", func(t *testing.T) {
		h := NewRuntimeHandler(&mockRuntime{})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/cancel", nil)
		h.HandleCancel(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("success returns cancelled", func(t *testing.T) {
		rt := &mockRuntime{}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/cancel?session_id=s1", nil)
		h.HandleCancel(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		var out map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if out["status"] != "cancelled" {
			t.Fatalf("status = %q, want cancelled", out["status"])
		}
		if rt.cancelCalls != 1 {
			t.Fatalf("cancelCalls = %d, want 1", rt.cancelCalls)
		}
	})

	t.Run("cancel error returns 400", func(t *testing.T) {
		rt := &mockRuntime{err: context.DeadlineExceeded}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/cancel?session_id=s1", nil)
		h.HandleCancel(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})
}

// ===== HandleRun (start path) =====

func TestHandleRun(t *testing.T) {
	t.Run("nil runtime returns 503", func(t *testing.T) {
		h := &Handler{} // runtime explicitly nil
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"input":"hi"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusServiceUnavailable)
		}
	})

	t.Run("bad json returns 400", func(t *testing.T) {
		h := NewRuntimeHandler(&mockRuntime{})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{invalid`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("empty body returns 400", func(t *testing.T) {
		h := NewRuntimeHandler(&mockRuntime{})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("message fallback to input", func(t *testing.T) {
		rt := &mockRuntime{result: types.FlowTurnResult{Reply: "ok"}}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run", strings.NewReader(`{"message":"from message"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
		}
		if rt.startCalls != 1 {
			t.Fatalf("startCalls = %d, want 1", rt.startCalls)
		}
		if rt.lastInput != "from message" {
			t.Fatalf("lastInput = %q, want from message", rt.lastInput)
		}
	})

	// The three subtests below close the gap E-01 left open: this handler is the
	// other public turn entry, and it answered 400 "file not found" for a session
	// that no longer exists. A client reading that fixes its request instead of
	// starting a new session.
	t.Run("unknown session in body maps to 404 and names it", func(t *testing.T) {
		rt := &mockRuntime{err: fmt.Errorf("read state: %w", storage.ErrNotFound)}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run",
			strings.NewReader(`{"flow_session_id":"fs_gone","input":"hi"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusNotFound, rr.Body.String())
		}
		if body := rr.Body.String(); !strings.Contains(body, `"fs_gone"`) {
			t.Fatalf("body = %q, want a 404 that quotes the session id", body)
		}
		if rt.startCalls != 0 {
			t.Fatalf("startCalls = %d, want 0: an unknown id must not start a session", rt.startCalls)
		}
	})

	t.Run("query flow_session_id continues that session", func(t *testing.T) {
		rt := &mockRuntime{result: types.FlowTurnResult{Reply: "ok"}}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run?flow_session_id=fs_x",
			strings.NewReader(`{"input":"hi"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rt.startCalls != 0 || rt.handleCalls != 1 {
			t.Fatalf("startCalls=%d handleCalls=%d, want 0/1", rt.startCalls, rt.handleCalls)
		}
		if rt.lastSessionID != "fs_x" {
			t.Fatalf("lastSessionID = %q, want fs_x", rt.lastSessionID)
		}
	})

	t.Run("unknown session in query is 404, not a new session", func(t *testing.T) {
		rt := &mockRuntime{err: fmt.Errorf("read state: %w", storage.ErrNotFound)}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run?session_id=fs_gone",
			strings.NewReader(`{"input":"hi"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusNotFound, rr.Body.String())
		}
		if rt.startCalls != 0 {
			t.Fatalf("startCalls = %d, want 0: a query id that is gone must not silently start", rt.startCalls)
		}
	})

	t.Run("body wins over query when both name a session", func(t *testing.T) {
		// Both locations are accepted, so the precedence has to be pinned: a
		// client that sends only one never notices, and a client that sends both
		// must not have the engine pick the one it did not mean.
		rt := &mockRuntime{result: types.FlowTurnResult{Reply: "ok"}}
		h := NewRuntimeHandler(rt)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/run?flow_session_id=fs_query",
			strings.NewReader(`{"flow_session_id":"fs_body","input":"hi"}`))
		h.HandleRun(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (body=%s)", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rt.lastSessionID != "fs_body" {
			t.Fatalf("lastSessionID = %q, want fs_body (the body names the session)", rt.lastSessionID)
		}
	})
}
