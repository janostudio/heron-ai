package view

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/agent"
	"github.com/heron-ai/heron-engine/internal/media"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/workspace"
	"github.com/heron-ai/heron-engine/pkg/types"
)

type Handler struct {
	runtime  types.FlowRuntime
	sessions storage.SessionWriter
	tasks    types.ToolTaskStore
	cancel   types.ToolTaskCanceller
	// files is the read-only history source the web record view replays. It is
	// nil for handlers built without one; those answer 503 rather than panic.
	files storage.FileStore
	// build names the binary that is serving. Without it a deployment cannot be
	// identified from the outside: two restarts look identical, and a rollback
	// that silently kept the new binary looks like a rollback that worked.
	build BuildInfo
	// streamHeartbeat overrides the followed-stream heartbeat interval. Zero
	// means the package default; tests set a small value rather than waiting
	// fifteen seconds to observe one frame.
	streamHeartbeat time.Duration
}

// BuildInfo identifies a running binary.
type BuildInfo struct {
	Version string
	Commit  string
}

// SetBuildInfo lets the entrypoint publish what it was built from.
func (h *Handler) SetBuildInfo(info BuildInfo) {
	if h == nil {
		return
	}
	h.build = info
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

// HandleRun serves the /api/sessions path. The method decides what it means:
// GET reads the index, POST creates a session. One path, because a page and a
// machine client should not need different bases for the same resource.
func (h *Handler) HandleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		h.HandleSessionList(w, r)
		return
	}
	if h.runtime == nil {
		http.Error(w, "FlowRuntime is not configured", http.StatusServiceUnavailable)
		return
	}

	var req flowInputRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDecodeError(w, err)
		return
	}
	if req.Input == "" {
		req.Input = req.Message
	}
	// The session to continue may be named in the body or in the query. Every
	// other turn endpoint takes it from the query, so a client that follows that
	// habit must not silently get a new session: "continue this" and "start a new
	// one" are different intentions, and only one of them is recoverable.
	if strings.TrimSpace(req.FlowSessionID) == "" {
		req.FlowSessionID = strings.TrimSpace(r.URL.Query().Get("flow_session_id"))
	}
	if strings.TrimSpace(req.FlowSessionID) == "" {
		req.FlowSessionID = strings.TrimSpace(r.URL.Query().Get("session_id"))
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
		// Continuing a named session is the same class of request as
		// /api/sessions/turn: a session that is gone has to answer 404, or the
		// client keeps retrying a request it can never satisfy. Only the branch
		// that creates a session has no id to name, and there 400 stays right.
		if strings.TrimSpace(req.FlowSessionID) != "" {
			writeRuntimeError(w, about("session", req.FlowSessionID, err))
			return
		}
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
		writeDecodeError(w, err)
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
		writeRuntimeError(w, about("session", sessionID, err))
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
			writeReadError(w, about("session", sessionID, err), "session "+strconv.Quote(sessionID))
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
		writeReadError(w, about("session", sessionID, err), "session "+strconv.Quote(sessionID))
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

	// follow keeps the stream open after a turn ends. A caller that wants to
	// see the turns this session starts on its own — a scheduled wake-up — has
	// no request in flight to subscribe with, so without this the stream would
	// close before the wake-up ever fired. Without the parameter the old
	// behaviour is unchanged: one turn's events, then close.
	follow := r.URL.Query().Get("follow") == "1"

	events, err := h.sessions.Subscribe(streamCtx, sessionID, lastID)
	if err != nil {
		writeReadError(w, about("session", sessionID, err), "event stream for session "+strconv.Quote(sessionID))
		return
	}
	writer, err := NewSSEWriter(w)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A followed stream never ends on a turn boundary, so it also has to keep
	// the connection warm: an idle connection is what an intermediary closes,
	// and the wake-up this mode exists to deliver arrives during that idle gap.
	if follow {
		interval := h.streamHeartbeat
		if interval <= 0 {
			interval = streamHeartbeatInterval
		}
		followStream(streamCtx, writer, events, interval)
		return
	}
	// Replaying a session that already ended has to deliver its *latest* turn,
	// not stop at the first terminal event in the file: a subscriber that sees
	// turn one only will re-render an old answer and never notice the turn it
	// actually asked about. The boundary below says where the recorded history
	// ends and whether that end is already settled.
	replayEnd, replaySettled := h.streamReplayBoundary(streamCtx, sessionID)
	for event := range events {
		if err := writer.WriteSessionEvent(event); err != nil {
			return
		}
		if event.Seq <= replayEnd {
			// Still replaying what was already recorded. Only the end of the
			// history can finish the response, and only when it is settled.
			if event.Seq == replayEnd && replaySettled {
				return
			}
			continue
		}
		if turnStreamEnded(event) {
			return
		}
	}
}

// streamReplayBoundary reports the last recorded sequence number and whether
// the session's newest event already closes the turn.
func (h *Handler) streamReplayBoundary(ctx context.Context, sessionID string) (int64, bool) {
	replay, err := h.sessions.Replay(ctx, sessionID)
	if err != nil || replay == nil || len(replay.Events) == 0 {
		return 0, false
	}
	return replay.LastSeq, turnStreamEnded(replay.Events[len(replay.Events)-1])
}

// turnStreamEnded reports whether this event is the last one a non-following
// subscriber needs.
//
// "Terminal" here means terminal for the turn, not for the session. An
// interactive turn ends waiting_input, so a subscriber that only stops on
// completed holds the connection open for ever: the client has to decide when
// to stop reading on its own, and every ordinary question leaks a connection
// until something upstream times it out.
//
// An intermediate turn is the exception. It also ends waiting_input, but it is a
// progress report and the conclusion it reports on has not been produced yet;
// closing there hands the client the handoff and drops the answer.
func turnStreamEnded(event storage.SessionEvent) bool {
	intermediate, _ := event.Payload["intermediate"].(bool)
	switch event.Type {
	case types.EventFlowTurnCompleted:
		return true
	case types.EventFlowTurnWaitingInput:
		return !intermediate
	case types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
		return true
	}
	return false
}

// streamHeartbeatInterval bounds how long a followed stream stays silent.
//
// A followed stream can legitimately idle for minutes between turns, and an
// intermediary that treats an idle connection as dead will close it. A comment
// frame is the only SSE frame clients are required to ignore, so it keeps the
// connection warm without adding an event type every consumer must filter.
const streamHeartbeatInterval = 15 * time.Second

// followStream serves a stream that outlives the current turn.
func followStream(ctx context.Context, writer *SSEWriter, events <-chan storage.SessionEvent, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := writer.WriteComment("ping"); err != nil {
				return
			}
		case event, ok := <-events:
			if !ok {
				return
			}
			if err := writer.WriteSessionEvent(event); err != nil {
				return
			}
		}
	}
}

// wakeupStatsProvider is implemented by a runtime that owns scheduled
// wake-ups. It is an optional interface so the handler keeps working with any
// FlowRuntime, including the fakes used in tests.
type wakeupStatsProvider interface {
	WakeupStats(ctx context.Context) (pending int, sessions int)
}

// healthResponse is what an embedding client probes before it starts sending
// work: a process that is up but has no runtime is not ready.
type healthResponse struct {
	Ready          bool   `json:"ready"`
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	PendingWakeups int    `json:"pending_wakeups"`
	SessionsLoaded int    `json:"sessions_loaded"`
}

// HandleHealth reports readiness and how many wake-ups are waiting. The counts
// are what an operator needs to answer "did the engine remember my timers
// across the restart" without reading the event logs.
func (h *Handler) HandleHealth(w http.ResponseWriter, r *http.Request) {
	response := healthResponse{
		Ready:   h.runtime != nil,
		Version: h.build.Version,
		Commit:  h.build.Commit,
	}
	if provider, ok := h.runtime.(wakeupStatsProvider); ok {
		response.PendingWakeups, response.SessionsLoaded = provider.WakeupStats(r.Context())
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
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
		writeReadError(w, about("session", sessionID, err), "recovery status for session "+strconv.Quote(sessionID))
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
		writeDecodeError(w, err)
		return
	}
	result, err := recovery.Recover(r.Context(), sessionID, req)
	if err != nil {
		writeRuntimeError(w, about("session", sessionID, err))
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
			writeDecodeError(w, err)
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
			writeRuntimeError(w, about("session", sessionID, err))
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
		writeDecodeError(w, err)
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
		writeRuntimeError(w, about("session", sessionID, err))
		return
	}
	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// writeRuntimeError maps a runtime failure to the status code a client acts on.
// The distinction that matters is "the thing you named does not exist" versus
// "the request you sent is wrong": a 400 on an expired session teaches the
// client to fix its request, so it retries a session that is gone instead of
// starting a new one.
func writeRuntimeError(w http.ResponseWriter, err error) {
	if isNotFound(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// writeReadError answers a failed read.
//
// A read names its subject and asks for it; there is no request shape the caller
// can repair. So "not found" is the one answer it can act on and everything else
// is ours: reporting an unreadable state file as 404 tells the client the session
// is gone, and it drops work that a retry would have found. The 5xx body carries
// the reason so an operator can act on it, but never a stack.
func writeReadError(w http.ResponseWriter, err error, subject string) {
	if isNotFound(err) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	http.Error(w, "internal error reading "+subject+": "+err.Error(), http.StatusInternalServerError)
}

// writeDecodeError answers a body that could not be read as JSON.
//
// Almost every decode failure is the request's fault. The exception is the
// server's own cap: a body over the limit arrives here as *http.MaxBytesError,
// and answering 400 would tell the client its JSON was malformed when the real
// answer is "send less".
func writeDecodeError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		http.Error(w, fmt.Sprintf("request body exceeds the %d byte limit", tooLarge.Limit), http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, err.Error(), http.StatusBadRequest)
}

// resourceError names what a failure was about.
//
// "file not found" alone leaves a client with several ids in flight unable to
// tell which one to drop, and leaves a human reading the body unable to tell
// what the engine looked for. The id is quoted because it comes from the
// request: quoting escapes anything that would otherwise corrupt the body.
type resourceError struct {
	resource string
	id       string
	err      error
}

func (e *resourceError) Error() string {
	return e.resource + " " + strconv.Quote(e.id) + ": " + e.err.Error()
}

func (e *resourceError) Unwrap() error { return e.err }

// about names the resource a failure belongs to. A failure that already names
// one is left alone: wrapping it twice would print the id twice.
func about(resource, id string, err error) error {
	if err == nil {
		return nil
	}
	var named *resourceError
	if errors.As(err, &named) {
		return err
	}
	return &resourceError{resource: resource, id: id, err: err}
}

// isNotFound reports whether the failure means "no such session/task/approval".
// Storage reports it as storage.ErrNotFound; a few call sites wrap the
// workspace's own sentinel instead.
func isNotFound(err error) bool {
	return errors.Is(err, storage.ErrNotFound) ||
		errors.Is(err, workspace.ErrFileNotFound) ||
		errors.Is(err, agent.ErrToolTaskNotFound)
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
	// The agent reads its input text from this block, not from the `input` field
	// beside it. A request that carries only attachments has no text of its own,
	// so without this the words that came with the picture are dropped and the
	// turn runs on an empty message.
	if strings.TrimSpace(text) == "" {
		text = strings.TrimSpace(req.Input)
		if text == "" {
			text = strings.TrimSpace(req.Message)
		}
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
			writeRuntimeError(w, about("session", sessionID, err))
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
		writeReadError(w, about("task", taskID, err), "task "+strconv.Quote(taskID))
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
		// Cancelling a task nobody dispatched is the same class of answer as
		// asking for a session that does not exist: the id is unknown, not the
		// request malformed. A client that sees 400 here fixes its request; a 404
		// tells it the work is already gone.
		writeRuntimeError(w, about("task", taskID, err))
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
		writeReadError(w, about("task", taskID, err), "event stream for task "+strconv.Quote(taskID))
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
