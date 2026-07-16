package view

import (
	"encoding/json"
	"net/http"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type Handler struct {
	// Placeholder for orchestration integration
}

func NewHandler() *Handler {
	return &Handler{}
}

func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	var req types.RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "running",
		"message": "Run started",
	})
}

func (h *Handler) HandleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "ok",
	})
}

func (h *Handler) HandleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	// Send initial event
	event := types.SSEEvent{Type: "connected"}
	data, _ := json.Marshal(event)
	w.Write([]byte("data: " + string(data) + "\n\n"))
	flusher.Flush()
}

func (h *Handler) HandleResume(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "resumed",
	})
}

func (h *Handler) HandleCancel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "cancelled",
	})
}
