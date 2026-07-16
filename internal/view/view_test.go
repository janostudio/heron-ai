package view

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandler_HandleStatus(t *testing.T) {
	h := NewHandler()
	req := httptest.NewRequest("GET", "/status", nil)
	rec := httptest.NewRecorder()

	h.HandleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestHandler_HandleRun(t *testing.T) {
	h := NewHandler()
	body := `{"flow":{"name":"test","stages":[]}}`
	req := httptest.NewRequest("POST", "/run", strings.NewReader(body))
	rec := httptest.NewRecorder()

	h.HandleRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
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
