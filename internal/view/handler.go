package view

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/media"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type Handler struct {
	runtime  types.FlowRuntime
	sessions storage.SessionWriter
	tasks    types.ToolTaskStore
	cancel   types.ToolTaskCanceller
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

// NewRuntimeHandlerWithSessionsAndTasks wires the Flow APIs together with
// durable Tool task inspection/control. Task endpoints intentionally depend
// only on the public task interfaces, not on the Agent implementation.
func NewRuntimeHandlerWithSessionsAndTasks(
	runtime types.FlowRuntime,
	sessions storage.SessionWriter,
	tasks types.ToolTaskStore,
	canceller types.ToolTaskCanceller,
) *Handler {
	return &Handler{
		runtime:  runtime,
		sessions: sessions,
		tasks:    tasks,
		cancel:   canceller,
	}
}

type flowInputRequest struct {
	FlowID        string                  `json:"flow_id,omitempty"`
	FlowSessionID string                  `json:"flow_session_id,omitempty"`
	Input         string                  `json:"input,omitempty"`
	Message       string                  `json:"message,omitempty"`
	Content       json.RawMessage         `json:"content,omitempty"`
	Attachments   []types.MediaAttachment `json:"attachments,omitempty"`
}

type approvalRequest struct {
	types.HITLResponse
	ApprovalID string `json:"approval_id,omitempty"`
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
	blocks, err := requestContextBlocks(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		if len(blocks) == 0 {
			http.Error(w, "input, message, content, or attachments is required", http.StatusBadRequest)
			return
		}
		req.Input = contentBlockText(blocks)
	}
	var result types.FlowTurnResult
	if strings.TrimSpace(req.FlowSessionID) != "" {
		if rich, ok := h.runtime.(types.RichFlowRuntime); ok {
			result, err = rich.HandleInputWithContext(r.Context(), req.FlowSessionID, req.Input, blocks)
		} else {
			result, err = h.runtime.HandleInput(r.Context(), req.FlowSessionID, req.Input)
		}
	} else {
		result, err = h.runtime.Start(r.Context(), types.StartFlowRequest{
			FlowID: req.FlowID, Input: req.Input, ContextBlocks: blocks,
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
	blocks, err := requestContextBlocks(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Input) == "" && len(blocks) == 0 {
		http.Error(w, "input, message, content, or attachments is required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Input) == "" {
		req.Input = contentBlockText(blocks)
	}
	var result types.FlowTurnResult
	if rich, ok := h.runtime.(types.RichFlowRuntime); ok {
		result, err = rich.HandleInputWithContext(r.Context(), sessionID, req.Input, blocks)
	} else {
		result, err = h.runtime.HandleInput(r.Context(), sessionID, req.Input)
	}
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

// HandleResult returns the durable current FlowSession state. It is a small
// polling endpoint used by machine transports after a waiting Tool/approval
// wake-up; the full turn result is returned by /api/run or /api/approvals.
func (h *Handler) HandleResult(w http.ResponseWriter, r *http.Request) {
	if h.runtime == nil {
		http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if strings.TrimSpace(sessionID) == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	session, err := h.runtime.Status(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, types.FlowTurnResult{Session: session})
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
	streamCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	lastID := int64(0)
	if value := r.Header.Get("Last-Event-ID"); value != "" {
		_, _ = fmt.Sscanf(value, "%d", &lastID)
	}

	events, err := h.sessions.Subscribe(streamCtx, sessionID, lastID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for event := range events {
		if err := writer.WriteSessionEvent(event); err != nil {
			return
		}
		if event.Type == types.EventFlowTurnCompleted {
			return
		}
	}
}

func (h *Handler) HandleRecoveryStatus(w http.ResponseWriter, r *http.Request) {
	recovery, ok := h.runtime.(types.RecoveryRuntime)
	if !ok {
		http.Error(w, "RecoveryRuntime is not configured", http.StatusNotImplemented)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	status, err := recovery.RecoveryStatus(r.Context(), sessionID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, status)
}

func (h *Handler) HandleRecover(w http.ResponseWriter, r *http.Request) {
	recovery, ok := h.runtime.(types.RecoveryRuntime)
	if !ok {
		http.Error(w, "RecoveryRuntime is not configured", http.StatusNotImplemented)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	var req types.RecoveryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	result, err := recovery.Recover(r.Context(), sessionID, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
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
		blocks, err := requestContextBlocks(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Input) == "" {
			req.Input = contentBlockText(blocks)
		}
		var result types.FlowTurnResult
		if rich, ok := h.runtime.(types.RichFlowRuntime); ok {
			result, err = rich.ResumeWithContext(r.Context(), sessionID, req.Input, blocks)
		} else {
			result, err = h.runtime.Resume(r.Context(), sessionID, req.Input)
		}
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

// HandleApproval resumes a durable Agent Tool approval. The optional
// interface keeps existing FlowRuntime implementations source-compatible.
func (h *Handler) HandleApproval(w http.ResponseWriter, r *http.Request) {
	runtime, ok := h.runtime.(types.ApprovalFlowRuntime)
	if !ok {
		http.Error(w, "approval resume is not configured", http.StatusNotImplemented)
		return
	}
	sessionID := r.URL.Query().Get("session_id")
	if sessionID == "" {
		http.Error(w, "session_id is required", http.StatusBadRequest)
		return
	}
	var req approvalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	requestID := req.RequestID
	if requestID == "" {
		requestID = req.ApprovalID
	}
	if strings.TrimSpace(requestID) == "" {
		http.Error(w, "approval_id is required", http.StatusBadRequest)
		return
	}
	req.RequestID = requestID
	var result types.FlowTurnResult
	var err error
	if auditable, ok := h.runtime.(types.AuditableApprovalFlowRuntime); ok {
		if req.DecidedAt.IsZero() {
			req.DecidedAt = time.Now().UTC()
		}
		if req.Channel == "" {
			req.Channel = "http"
		}
		result, err = auditable.ResumeApprovalWithResponse(r.Context(), sessionID, req.HITLResponse)
	} else {
		result, err = runtime.ResumeApproval(
			r.Context(), sessionID, req.RequestID, req.Approved, req.Reason,
		)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func requestContextBlocks(req flowInputRequest) ([]types.ContextBlock, error) {
	var text string
	var parts []types.ContentPart
	if len(req.Content) > 0 {
		parsedText, parsedParts, err := media.ParseMessageContent(req.Content)
		if err != nil {
			return nil, err
		}
		text = parsedText
		parts = append(parts, parsedParts...)
	}
	if len(req.Attachments) > 0 {
		for _, attachment := range req.Attachments {
			if strings.TrimSpace(attachment.SourceType) == "" {
				if attachment.DataBase64 != "" {
					attachment.SourceType = "base64"
				} else if attachment.Path != "" {
					attachment.SourceType = "path"
				} else if attachment.URL != "" {
					attachment.SourceType = "url"
				}
			}
			kind := attachment.Kind
			if kind == "" {
				kind = "file"
			}
			parts = append(parts, types.ContentPart{Type: kind, Media: &attachment})
		}
	}
	if strings.TrimSpace(text) == "" && len(parts) == 0 {
		return nil, nil
	}
	return []types.ContextBlock{{
		Kind: "input", Text: text, Parts: parts, Source: "http",
		Stability: "dynamic", Priority: 80, Compressible: true,
	}}, nil
}

func contentBlockText(blocks []types.ContextBlock) string {
	var parts []string
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
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

func taskIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if taskID := strings.TrimSpace(r.URL.Query().Get("task_id")); taskID != "" {
		return taskID
	}
	var body struct {
		TaskID string `json:"task_id"`
	}
	if r.Body == nil {
		return ""
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	return strings.TrimSpace(body.TaskID)
}

// HandleTaskStatus returns the durable state of one asynchronous Tool task.
func (h *Handler) HandleTaskStatus(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		http.Error(w, "ToolTaskStore is not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := taskIDFromRequest(r)
	if taskID == "" {
		http.Error(w, "task_id is required", http.StatusBadRequest)
		return
	}
	task, err := h.tasks.Load(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, task)
}

// HandleTaskCancel requests cancellation and is idempotent for terminal
// tasks. The task runner owns the actual context cancellation.
func (h *Handler) HandleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if h.cancel == nil {
		http.Error(w, "ToolTaskCanceller is not configured", http.StatusServiceUnavailable)
		return
	}
	taskID := taskIDFromRequest(r)
	if taskID == "" {
		http.Error(w, "task_id is required", http.StatusBadRequest)
		return
	}
	if err := h.cancel.Cancel(r.Context(), taskID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	status := types.ToolTaskStatus(types.ToolTaskCancelled)
	if h.tasks != nil {
		if task, err := h.tasks.Load(r.Context(), taskID); err == nil {
			status = task.Status
		}
	}
	writeJSON(w, map[string]any{
		"task_id": taskID,
		"status":  status,
	})
}

// HandleTaskStream publishes the current task and subsequent durable
// progress/final-state updates as Server-Sent Events.
func (h *Handler) HandleTaskStream(w http.ResponseWriter, r *http.Request) {
	if h.tasks == nil {
		http.Error(w, "ToolTaskStore is not configured", http.StatusServiceUnavailable)
		return
	}
	subscriber, ok := h.tasks.(types.ToolTaskSubscriber)
	if !ok {
		http.Error(w, "ToolTaskStore does not support subscriptions", http.StatusNotImplemented)
		return
	}
	taskID := taskIDFromRequest(r)
	if taskID == "" {
		http.Error(w, "task_id is required", http.StatusBadRequest)
		return
	}
	events, err := subscriber.Subscribe(r.Context(), taskID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	for task := range events {
		if err := writer.WriteTask(task); err != nil {
			return
		}
		if task.Status == types.ToolTaskCompleted ||
			task.Status == types.ToolTaskFailed ||
			task.Status == types.ToolTaskCancelled {
			return
		}
	}
}
