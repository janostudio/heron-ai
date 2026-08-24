package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type Handler struct {
	runtime  types.FlowRuntime
	sessions storage.SessionWriter
}

func NewHandler() *Handler {
	return &Handler{runtime: &unconfiguredRuntime{}}
}

type unconfiguredRuntime struct{}

func (u *unconfiguredRuntime) Start(context.Context, types.StartFlowRequest) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, fmt.Errorf("FlowRuntime is not configured")
}
func (u *unconfiguredRuntime) HandleInput(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, fmt.Errorf("FlowRuntime is not configured")
}
func (u *unconfiguredRuntime) Resume(context.Context, string, string) (types.FlowTurnResult, error) {
	return types.FlowTurnResult{}, fmt.Errorf("FlowRuntime is not configured")
}
func (u *unconfiguredRuntime) Cancel(context.Context, string) error {
	return fmt.Errorf("FlowRuntime is not configured")
}
func (u *unconfiguredRuntime) Status(context.Context, string) (types.FlowSession, error) {
	return types.FlowSession{}, fmt.Errorf("FlowRuntime is not configured")
}

func NewRuntimeHandler(runtime types.FlowRuntime) *Handler {
	return &Handler{runtime: runtime}
}

func NewRuntimeHandlerWithSessions(runtime types.FlowRuntime, sessions storage.SessionWriter) *Handler {
	return &Handler{runtime: runtime, sessions: sessions}
}

type flowInputRequest struct {
	FlowID        string `json:"flow_id,omitempty"`
	FlowSessionID string `json:"flow_session_id,omitempty"`
	Input         string `json:"input,omitempty"`
	Message       string `json:"message,omitempty"`
}

func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	if h.runtime == nil {
		http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
		return
	}

	var req flowInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Input == "" {
		req.Input = req.Message
	}
	if strings.TrimSpace(req.Input) == "" {
		http.Error(w, "input or message is required", http.StatusBadRequest)
		return
	}

	var result types.FlowTurnResult
	var err error
	if strings.TrimSpace(req.FlowSessionID) != "" {
		result, err = h.runtime.HandleInput(r.Context(), req.FlowSessionID, req.Input)
	} else {
		result, err = h.runtime.Start(r.Context(), types.StartFlowRequest{
			FlowID: req.FlowID,
			Input:  req.Input,
		})
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

func (h *Handler) HandleTurn(w http.ResponseWriter, r *http.Request) {
	if h.runtime == nil {
		http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	var req flowInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Input == "" {
		req.Input = req.Message
	}
	if strings.TrimSpace(req.Input) == "" {
		http.Error(w, "input or message is required", http.StatusBadRequest)
		return
	}
	result, err := h.runtime.HandleInput(r.Context(), sessionID, req.Input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if h.runtime != nil {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		session, err := h.runtime.Status(r.Context(), sessionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(session)
		return
	}

	http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
}

func (h *Handler) HandleStream(w http.ResponseWriter, r *http.Request) {
	if h.sessions == nil {
		http.Error(w, "SessionWriter is not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	replay, err := h.sessions.Replay(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	lastID := int64(0)
	if value := r.Header.Get("Last-Event-ID"); value != "" {
		_, _ = fmt.Sscanf(value, "%d", &lastID)
	}
	for _, event := range replay.Events {
		if event.Seq <= lastID {
			continue
		}
		if err := writer.WriteSessionEvent(event); err != nil {
			return
		}
	}
}

func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	if h.runtime != nil {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		var req flowInputRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if req.Input == "" {
			req.Input = req.Message
		}
		result, err := h.runtime.Resume(r.Context(), sessionID, req.Input)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
		return
	}

	http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	if h.runtime != nil {
		sessionID := r.URL.Query().Get("session_id")
		if sessionID == "" {
			http.Error(w, "session_id is required", http.StatusBadRequest)
			return
		}
		if err := h.runtime.Cancel(r.Context(), sessionID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":     "cancelled",
			"session_id": sessionID,
		})
		return
	}

	http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
}
