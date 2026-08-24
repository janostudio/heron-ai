package view

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestHandler_HandleStatus(t *testing.T) {
	h := NewHandler()
	req := httptest.NewRequest("GET", "/status", nil)
	rec := httptest.NewRecorder()

	h.HandleStatus(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_HandleRun(t *testing.T) {
	h := NewHandler()
	body := `{"message":"hello"}`
	req := httptest.NewRequest("POST", "/run", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestSSEWriter_WriteEvent(t *testing.T) {
	rec := httptest.NewRecorder()
	writer, err := NewSSEWriter(rec)
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}

	err = writer.WriteChunk("hello")
	if err != nil {
		t.Fatalf("WriteChunk: %v", err)
	}

	body := rec.Body.String()
	if body == "" {
		t.Error("expected non-empty body")
	}
}

type handlerFlowRuntime struct {
	started bool
	handled bool
}

func (r *handlerFlowRuntime) Start(_ context.Context, req types.StartFlowRequest) (types.FlowTurnResult, error) {
	r.started = true
	return types.FlowTurnResult{
		Session: types.FlowSession{ID: "fs-1", FlowID: req.FlowID},
		Turn:    types.FlowTurn{Input: req.Input},
	}, nil
}

func (r *handlerFlowRuntime) HandleInput(_ context.Context, sessionID, input string) (types.FlowTurnResult, error) {
	r.handled = true
	return types.FlowTurnResult{
		Session: types.FlowSession{ID: sessionID},
		Turn:    types.FlowTurn{Input: input},
	}, nil
}

func (r *handlerFlowRuntime) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, nil
}
func (r *handlerFlowRuntime) Cancel(context.Context, string) error { return nil }
func (r *handlerFlowRuntime) Status(context.Context, string) (types.FlowSession, error) {
	return types.FlowSession{}, nil
}

func TestHandler_HandleRunWithFlowSessionIDUsesHandleInput(t *testing.T) {
	runtime := &handlerFlowRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(`{"flow_session_id":"fs-1","message":"continue"}`))
	rec := httptest.NewRecorder()

	handler.HandleRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !runtime.handled || runtime.started {
		t.Fatalf("expected HandleInput only, started=%v handled=%v", runtime.started, runtime.handled)
	}
}

func TestHandler_HandleTurn(t *testing.T) {
	runtime := &handlerFlowRuntime{}
	handler := NewRuntimeHandler(runtime)
	req := httptest.NewRequest(http.MethodPost, "/turn?session_id=fs-1", strings.NewReader(`{"input":"next"}`))
	rec := httptest.NewRecorder()

	handler.HandleTurn(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result types.FlowTurnResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if result.Turn.Input != "next" || !runtime.handled {
		t.Fatalf("unexpected result %#v, handled=%v", result, runtime.handled)
	}
}

func TestHandler_HandleStreamReplaysFromLastEventID(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	for _, eventType := range []string{"one", "two", "three"} {
		if _, err := writer.Append(context.Background(), "fs-1", types.SessionEvent{Type: eventType}); err != nil {
			t.Fatal(err)
		}
	}
	handler := NewRuntimeHandlerWithSessions(&handlerFlowRuntime{}, writer)
	req := httptest.NewRequest(http.MethodGet, "/stream?session_id=fs-1", nil)
	req.Header.Set("Last-Event-ID", "1")
	rec := httptest.NewRecorder()

	handler.HandleStream(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"type":"one"`) || !strings.Contains(body, `"type":"two"`) || !strings.Contains(body, `"type":"three"`) {
		t.Fatalf("unexpected SSE replay: %s", body)
	}
	if !strings.Contains(body, "id: 2") || !strings.Contains(body, "id: 3") {
		t.Fatalf("missing event ids: %s", body)
	}
}
