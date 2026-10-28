package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// JSON-RPC error codes reserved by the JSON-RPC 2.0 specification.
const (
	jsonRPCParseError     = -32700
	jsonRPCInvalidRequest = -32600
	jsonRPCMethodNotFound = -32601
	jsonRPCInvalidParams  = -32602

	// Application errors use the implementation-reserved range.
	// See docs/CONNECT-CLI-JSONRPC.md §8.
	jsonRPCFlowTurnFailed = -32001 // FlowTurn 执行失败
	// -32002 also covers "session state does not allow this turn", e.g. a
	// session parked at an approval gate.
	jsonRPCSessionFailed    = -32002 // Session 不存在或状态不允许
	jsonRPCRuntimeFailed    = -32003 // Runtime 配置或初始化失败
	jsonRPCRequestCancelled = -32004 // 请求被取消
)

// errTurnBlockedByApproval is returned when a plain turn targets a session that
// is parked at an approval gate. The caller must answer the pending approval
// first; routing the input through HandleInput would silently bypass the
// human-in-the-loop gate (docs/CONNECT-CLI-JSONRPC.md §6).
var errTurnBlockedByApproval = errors.New("session is waiting for approval: respond to the pending approval before sending a new turn")

// errTurnBlockedByTool is returned when a plain turn targets a session parked
// on async Tool tasks. FlowRuntime.HandleInput rejects that state too, but the
// transport maps it here so the caller gets -32002 ("state does not allow this
// turn") instead of a generic -32001, with the same shape as the approval case.
var errTurnBlockedByTool = errors.New("session is waiting for a Tool: resume the pending Tool task before sending a new turn")

// errApprovalNotConfigured is returned when the wired FlowRuntime cannot resume
// approvals at all. Reporting it as a runtime failure rather than as a bad
// request keeps "caller mistake" distinguishable from "this engine build has no
// approval support".
var errApprovalNotConfigured = errors.New("approval resume is not configured")

// jsonRPCRequest is deliberately small. The transport is JSON-RPC 2.0, while
// the "turn" method and its params are Heron-specific.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCTurnParams struct {
	SessionID     string `json:"session_id,omitempty"`
	FlowSessionID string `json:"flow_session_id,omitempty"`
	Input         string `json:"input,omitempty"`
	Message       string `json:"message,omitempty"`
	// FollowAsync keeps the progress subscription open after this turn
	// returns, until the work it dispatched into the background finishes.
	// A dispatch turn answers "handed off, more to come" and the real answer
	// arrives in a later FlowTurn that no other caller is subscribed to, so
	// without this flag that answer has nowhere to go.
	FollowAsync bool `json:"follow_async,omitempty"`
}

type jsonRPCTurnResult struct {
	SessionID  string              `json:"session_id"`
	FlowTurnID string              `json:"flow_turn_id,omitempty"`
	Status     types.SessionStatus `json:"status"`
	Reply      string              `json:"reply,omitempty"`
	// Team is the Team that wrote Reply. Callers that attribute a message to
	// its producer need it: an unattended turn can carry several Teams' results
	// and the reply is only the last hop's text.
	Team string `json:"team,omitempty"`
	// Intermediate marks a turn that is still waiting for work it dispatched,
	// so its Reply is a progress line rather than an answer. A caller must not
	// render an empty Reply from such a turn as "the turn produced nothing":
	// the conclusion arrives on a later turn.
	Intermediate bool                   `json:"intermediate,omitempty"`
	Records      []jsonRPCRecordSummary `json:"records,omitempty"`
	Usage        types.TokenUsage       `json:"usage,omitempty"`
	Error        string                 `json:"error,omitempty"`
	// PendingApprovals is how a caller learns the approval_id it must pass to
	// "approve". Without it, a JSON-RPC-only bridge parked at an approval
	// gate has no way to advance the session.
	PendingApprovals []jsonRPCPendingApproval `json:"pending_approvals,omitempty"`
}

type jsonRPCPendingApproval struct {
	RequestID   string `json:"request_id"`
	CallID      string `json:"call_id,omitempty"`
	ToolCallID  string `json:"tool_call_id,omitempty"`
	ToolName    string `json:"tool_name,omitempty"`
	Reason      string `json:"reason,omitempty"`
	RequestedAt string `json:"requested_at,omitempty"`
}

// jsonRPCApproveParams answers a pending Agent Tool approval gate.
//
// Approved is a pointer because a HITL gate must not treat "field absent" as
// either answer: an omitted flag is a protocol error, not a denial.
type jsonRPCApproveParams struct {
	SessionID     string `json:"session_id,omitempty"`
	FlowSessionID string `json:"flow_session_id,omitempty"`
	ApprovalID    string `json:"approval_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	Approved      *bool  `json:"approved,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Approver      string `json:"approver,omitempty"`
	ApproverID    string `json:"approver_id,omitempty"`
	Channel       string `json:"channel,omitempty"`
}

type jsonRPCRecordSummary struct {
	RecordID string             `json:"record_id,omitempty"`
	Kind     string             `json:"kind,omitempty"`
	Name     string             `json:"name,omitempty"`
	Summary  string             `json:"summary,omitempty"`
	Status   types.RecordStatus `json:"status,omitempty"`
	Revision int                `json:"revision,omitempty"`
}

// jsonRPCServer handles one long-lived stdin/stdout JSONL connection.
//
// JSON-RPC provides the message envelope and request/response correlation.
// Newline-delimited JSON provides framing for the CLI pipes. This is not the
// same schema as the persistent session.jsonl event log.
type jsonRPCServer struct {
	runtime  types.FlowRuntime
	flowID   string
	writer   io.Writer
	sessions storage.SessionWriter

	// writeMu serialises everything written to the pipe. Responses come from
	// the serve loop while progress notifications come from a per-turn pump;
	// without this lock the two can interleave and corrupt a JSONL line.
	writeMu sync.Mutex

	// pumpMu guards pumps. A following pump outlives the turn that started
	// it, so a second turn on the same session must attach to that stream
	// instead of opening a second one that would deliver every event twice.
	pumpMu sync.Mutex
	pumps  map[string]*eventPump
}

type jsonRPCFlusher interface {
	Flush() error
}

func newJSONRPCServer(runtime types.FlowRuntime, flowID string, writer io.Writer) *jsonRPCServer {
	return &jsonRPCServer{
		runtime: runtime,
		flowID:  flowID,
		writer:  writer,
	}
}

// withSessions wires the session event store used to stream progress
// notifications. Without it the server keeps its previous single-response
// behaviour, which is what the existing tests rely on.
func (s *jsonRPCServer) withSessions(sessions storage.SessionWriter) *jsonRPCServer {
	s.sessions = sessions
	return s
}

// Serve reads one JSON-RPC request per line and writes one response per
// request. Requests are intentionally processed serially because one
// FlowSession must not execute two FlowTurns concurrently.
func (s *jsonRPCServer) Serve(ctx context.Context, reader io.Reader) error {
	if s.runtime == nil {
		return errors.New("JSON-RPC runtime is nil")
	}
	if reader == nil {
		return errors.New("JSON-RPC reader is nil")
	}
	if s.writer == nil {
		return errors.New("JSON-RPC writer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	scanner := bufio.NewScanner(reader)
	// Prompts and structured payloads can be large. Keep framing line-based,
	// but allow a reasonably large single request without unbounded memory.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var request jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			if writeErr := s.writeError(json.RawMessage("null"), jsonRPCParseError, "parse error", nil); writeErr != nil {
				return writeErr
			}
			continue
		}

		// JSON-RPC notifications have no id and do not receive a response.
		// The first version does not define notification methods, so ignore
		// them after validating the envelope instead of emitting an unsolicited
		// response that a caller cannot correlate.
		if len(request.ID) == 0 {
			if request.JSONRPC != "2.0" || request.Method == "" {
				if writeErr := s.writeError(json.RawMessage("null"), jsonRPCInvalidRequest, "invalid JSON-RPC request", nil); writeErr != nil {
					return writeErr
				}
			}
			continue
		}

		if err := s.handle(ctx, request); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read JSON-RPC stdin: %w", err)
	}
	return nil
}

func (s *jsonRPCServer) handle(ctx context.Context, request jsonRPCRequest) error {
	id := request.ID
	if len(id) == 0 {
		id = json.RawMessage("null")
	}

	if request.JSONRPC != "2.0" || bytes.Equal(bytes.TrimSpace(request.ID), []byte("null")) || request.Method == "" || !validJSONRPCID(request.ID) {
		return s.writeError(id, jsonRPCInvalidRequest, "invalid JSON-RPC request", nil)
	}

	switch request.Method {
	case "turn":
		return s.handleTurn(ctx, id, request.Params)
	case "approve":
		return s.handleApprove(ctx, id, request.Params)
	default:
		return s.writeError(id, jsonRPCMethodNotFound, "method not found", map[string]any{
			"method": request.Method,
		})
	}
}

func (s *jsonRPCServer) handleTurn(ctx context.Context, id json.RawMessage, rawParams json.RawMessage) error {
	if len(rawParams) == 0 || string(rawParams) == "null" {
		return s.writeError(id, jsonRPCInvalidParams, "params are required", nil)
	}

	var params jsonRPCTurnParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return s.writeError(id, jsonRPCInvalidParams, "params must be an object", nil)
	}
	if params.Input == "" {
		params.Input = params.Message
	}
	if strings.TrimSpace(params.Input) == "" {
		return s.writeError(id, jsonRPCInvalidParams, "input is required", nil)
	}
	if params.SessionID == "" {
		params.SessionID = params.FlowSessionID
	}

	pump := s.startTurnPump(ctx, params.SessionID)
	result, err := executeFlowTurn(ctx, s.runtime, s.flowID, params.SessionID, params.Input)
	if pump == nil && s.sessions != nil && params.SessionID == "" && result.Session.ID != "" {
		// A brand-new session has no id until Start returns, so there was no
		// subscription window to stream from: replay the turn's events as one
		// ordered snapshot instead.
		pump = newSnapshotPump(s.sessions, s.writeProgress, result.Turn.ID)
		pump.replayEventsSnapshot(ctx, result.Session.ID)
		// Following needs a live subscription, not a one-shot snapshot: the
		// background turns happen after this response is written.
		if params.FollowAsync {
			if replay, replayErr := s.sessions.Replay(ctx, result.Session.ID); replayErr == nil {
				pump = startEventPump(ctx, s.sessions, result.Session.ID, "", replay.LastSeq, s.writeProgress)
				s.registerPump(result.Session.ID, pump)
			}
		}
	}
	// Following happens after the response, so the caller sees this turn's
	// answer at turn pace instead of waiting for the background work. The
	// subscription stays alive in a goroutine; every write goes through
	// jsonRPCServer.write, which serialises notifications and responses.
	startFollow := func(sessionID string, pump *eventPump) {
		if pump == nil {
			return
		}
		if !params.FollowAsync {
			s.releasePump(sessionID, pump)
			return
		}
		// The pump folds flow events only, and a TeamRun tool task is not one
		// of them: it lives in the task store. Without this the follower sees
		// an empty event log while a dispatched Team is still working, closes
		// the subscription followAsyncHold later, and the answer written
		// afterwards is never forwarded (s24: subscription stopped 17:44, the
		// conclusion landed 17:49 and never reached the client).
		pump.busy = s.pendingTeamWork
		go func() {
			pump.waitForIdle(ctx, sessionID, followAsyncMaxWait)
			s.releasePump(sessionID, pump)
		}()
	}

	if err != nil {
		code := jsonRPCFlowTurnFailed
		switch {
		case errors.Is(err, context.Canceled):
			code = jsonRPCRequestCancelled
		case errors.Is(err, errTurnBlockedByApproval), errors.Is(err, errTurnBlockedByTool):
			code = jsonRPCSessionFailed
		case params.SessionID != "" && result.Session.ID == "":
			code = jsonRPCSessionFailed
		}
		turnSummaryFromResult(result, s.writeProgress)
		writeErr := s.writeError(id, code, err.Error(), turnErrorData(result, params.SessionID))
		startFollow(result.Session.ID, pump)
		return writeErr
	}

	turnSummaryFromResult(result, s.writeProgress)
	writeErr := s.writeResult(id, jsonRPCTurnResultFrom(result))
	startFollow(result.Session.ID, pump)
	return writeErr
}

// startTurnPump subscribes to the session's events for the duration of one
// turn. It returns nil when progress streaming is not configured, when the
// session is unknown (the caller then falls back to a post-turn snapshot), or
// when a pump is already streaming this session — that pump keeps forwarding,
// so a second one would duplicate every event.
func (s *jsonRPCServer) startTurnPump(ctx context.Context, sessionID string) *eventPump {
	if s.sessions == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	// Check and register under one lock: a second turn that saw a pump being
	// created but not yet recorded would open a competing stream.
	s.pumpMu.Lock()
	defer s.pumpMu.Unlock()
	if s.pumps == nil {
		s.pumps = make(map[string]*eventPump)
	}
	if _, streaming := s.pumps[sessionID]; streaming {
		return nil
	}
	replay, err := s.sessions.Replay(ctx, sessionID)
	if err != nil {
		return nil
	}
	pump := startEventPump(ctx, s.sessions, sessionID, "", replay.LastSeq, s.writeProgress)
	s.pumps[sessionID] = pump
	return pump
}

// pendingTeamWork reports whether the session still has an outstanding TeamRun
// tool task. The follower reads the flow event log, which holds no record of
// those tasks: a dispatch turn therefore ends waiting_input with an empty event
// log while the Team it handed work to is still running. Treating that as idle
// is what dropped s24's conclusion.
func (s *jsonRPCServer) pendingTeamWork(ctx context.Context, sessionID string) bool {
	if s.runtime == nil || strings.TrimSpace(sessionID) == "" {
		return false
	}
	pending, ok := s.runtime.(types.PendingWorkRuntime)
	if !ok {
		return false
	}
	return pending.HasPendingTeamWork(ctx, sessionID)
}

// registerPump records the session's streaming pump so later turns attach to
// it instead of opening a competing stream.
func (s *jsonRPCServer) registerPump(sessionID string, pump *eventPump) {
	if pump == nil || strings.TrimSpace(sessionID) == "" {
		return
	}
	s.pumpMu.Lock()
	if s.pumps == nil {
		s.pumps = make(map[string]*eventPump)
	}
	s.pumps[sessionID] = pump
	s.pumpMu.Unlock()
}

// releasePump unregisters one pump and then stops it. The order matters: a
// new turn arriving while the old pump drains must not find it registered —
// it would attach to a stream that is already closing and miss its events.
func (s *jsonRPCServer) releasePump(sessionID string, pump *eventPump) {
	if pump == nil {
		return
	}
	s.pumpMu.Lock()
	if s.pumps[sessionID] == pump {
		delete(s.pumps, sessionID)
	}
	s.pumpMu.Unlock()
	pump.Stop()
}

// handleApprove answers the pending approval of a session parked at
// waiting_approval. It is the only JSON-RPC way out of that state: "turn" is
// rejected there on purpose, so a transport that only speaks JSON-RPC would
// otherwise have no method able to advance the session.
func (s *jsonRPCServer) handleApprove(ctx context.Context, id json.RawMessage, rawParams json.RawMessage) error {
	if len(rawParams) == 0 || string(rawParams) == "null" {
		return s.writeError(id, jsonRPCInvalidParams, "params are required", nil)
	}

	var params jsonRPCApproveParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return s.writeError(id, jsonRPCInvalidParams, "params must be an object", nil)
	}
	if params.SessionID == "" {
		params.SessionID = params.FlowSessionID
	}
	if strings.TrimSpace(params.SessionID) == "" {
		return s.writeError(id, jsonRPCInvalidParams, "session_id is required", nil)
	}
	if params.ApprovalID == "" {
		params.ApprovalID = params.RequestID
	}
	if strings.TrimSpace(params.ApprovalID) == "" {
		return s.writeError(id, jsonRPCInvalidParams, "approval_id is required", nil)
	}
	if params.Approved == nil {
		// A HITL decision must be explicit. Defaulting an absent flag to
		// false would silently deny, and to true would silently approve.
		return s.writeError(id, jsonRPCInvalidParams, "approved is required", nil)
	}

	result, err := executeApproval(ctx, s.runtime, params)
	if err != nil {
		code := jsonRPCFlowTurnFailed
		switch {
		case errors.Is(err, context.Canceled):
			code = jsonRPCRequestCancelled
		case errors.Is(err, errApprovalNotConfigured):
			code = jsonRPCRuntimeFailed
		case errors.Is(err, errApprovalSessionNotWaiting):
			code = jsonRPCSessionFailed
		}
		return s.writeError(id, code, err.Error(), turnErrorData(result, params.SessionID))
	}

	return s.writeResult(id, jsonRPCTurnResultFrom(result))
}

// errApprovalSessionNotWaiting marks "the session is not at an approval gate",
// which is a caller/state error (-32002) rather than a turn failure (-32001).
var errApprovalSessionNotWaiting = errors.New("session is not waiting for approval")

func executeApproval(
	ctx context.Context,
	runtime types.FlowRuntime,
	params jsonRPCApproveParams,
) (types.FlowTurnResult, error) {
	// ResumeApproval re-reads the session and rejects the wrong state anyway;
	// this check only exists so a caller that mistimes the request gets
	// -32002 instead of a generic -32001.
	session, err := runtime.Status(ctx, params.SessionID)
	if err != nil {
		return types.FlowTurnResult{}, fmt.Errorf("%w: %v", errApprovalSessionNotWaiting, err)
	}
	if session.Status != types.SessionWaitingApproval {
		return types.FlowTurnResult{Session: session}, fmt.Errorf(
			"%w (session %s is %s)", errApprovalSessionNotWaiting, params.SessionID, session.Status,
		)
	}

	decision := types.HITLResponse{
		RequestID:  params.ApprovalID,
		Approved:   *params.Approved,
		Reason:     params.Reason,
		Approver:   params.Approver,
		ApproverID: params.ApproverID,
		Channel:    params.Channel,
		DecidedAt:  time.Now().UTC(),
	}
	if decision.Channel == "" {
		decision.Channel = "jsonrpc"
	}
	if auditable, ok := runtime.(types.AuditableApprovalFlowRuntime); ok {
		return auditable.ResumeApprovalWithResponse(ctx, params.SessionID, decision)
	}
	approval, ok := runtime.(types.ApprovalFlowRuntime)
	if !ok {
		return types.FlowTurnResult{}, errApprovalNotConfigured
	}
	return approval.ResumeApproval(ctx, params.SessionID, params.ApprovalID, *params.Approved, params.Reason)
}

func executeFlowTurn(
	ctx context.Context,
	runtime types.FlowRuntime,
	flowID string,
	sessionID string,
	input string,
) (types.FlowTurnResult, error) {
	if sessionID == "" {
		return runtime.Start(ctx, types.StartFlowRequest{
			FlowID: flowID,
			Input:  input,
		})
	}

	session, err := runtime.Status(ctx, sessionID)
	if err != nil {
		return types.FlowTurnResult{}, err
	}
	switch session.Status {
	case types.SessionWaitingInput:
		return runtime.Resume(ctx, sessionID, input)
	case types.SessionWaitingApproval:
		// Return the session so the error response can still carry
		// session_id/status for the caller to react to.
		return types.FlowTurnResult{Session: session}, fmt.Errorf("%w (session %s)", errTurnBlockedByApproval, sessionID)
	case types.SessionWaitingTool:
		// A new turn cannot collect the pending Tool results; the session has
		// to be resumed once those tasks are terminal.
		return types.FlowTurnResult{Session: session}, fmt.Errorf("%w (session %s)", errTurnBlockedByTool, sessionID)
	default:
		return runtime.HandleInput(ctx, sessionID, input)
	}
}

func validJSONRPCID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return false
	}

	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	switch value.(type) {
	case string, float64:
		return true
	default:
		return false
	}
}

func jsonRPCTurnResultFrom(result types.FlowTurnResult) jsonRPCTurnResult {
	records := make([]jsonRPCRecordSummary, 0, len(result.Records))
	for _, record := range result.Records {
		records = append(records, jsonRPCRecordSummary{
			RecordID: record.RecordID,
			Kind:     record.Kind,
			Name:     record.Name,
			Summary:  record.Summary,
			Status:   record.Status,
			Revision: record.Revision,
		})
	}

	pending := make([]jsonRPCPendingApproval, 0, len(result.PendingApprovals))
	for _, item := range result.PendingApprovals {
		summary := jsonRPCPendingApproval{
			RequestID:  item.RequestID,
			CallID:     item.CallID,
			ToolCallID: item.ToolCallID,
			ToolName:   item.ToolName,
			Reason:     item.Reason,
		}
		if !item.RequestedAt.IsZero() {
			summary.RequestedAt = item.RequestedAt.UTC().Format(time.RFC3339)
		}
		pending = append(pending, summary)
	}

	// Reply is what the caller shows the user. FinalReply is the hop that
	// ended the turn; Reply concatenates every hop, intermediate Teams'
	// raw output included.
	return jsonRPCTurnResult{
		SessionID:        result.Session.ID,
		FlowTurnID:       result.Turn.ID,
		Status:           result.Session.Status,
		Reply:            replyForCaller(result),
		Team:             finalReplyTeam(result),
		Intermediate:     result.Turn.Intermediate,
		Records:          records,
		Usage:            aggregateTeamUsage(result.TeamResults),
		Error:            result.Error,
		PendingApprovals: pending,
	}
}

// finalReplyTeam names the Team behind Reply.
func finalReplyTeam(result types.FlowTurnResult) string {
	if result.FinalReplyTeam != "" {
		return result.FinalReplyTeam
	}
	return ""
}

// replyForCaller picks the text a caller relays to the user: the hop that
// ended the turn when the runtime could identify it, otherwise the
// concatenation (a turn that failed before any route resolved).
func replyForCaller(result types.FlowTurnResult) string {
	if strings.TrimSpace(result.FinalReply) != "" {
		return result.FinalReply
	}
	return result.Reply
}

func turnErrorData(result types.FlowTurnResult, requestedSessionID string) map[string]any {
	sessionID := result.Session.ID
	if sessionID == "" {
		sessionID = requestedSessionID
	}

	data := map[string]any{}
	if sessionID != "" {
		data["session_id"] = sessionID
	}
	if result.Turn.ID != "" {
		data["flow_turn_id"] = result.Turn.ID
	}
	if result.Session.Status != "" {
		data["status"] = result.Session.Status
	}
	// Only present when the runtime handed back the pending gate together with
	// the rejection; otherwise the caller reads it from the turn result that
	// parked the session (docs/CONNECT-CLI-JSONRPC.md §6.3).
	if len(result.PendingApprovals) > 0 && result.PendingApprovals[0].RequestID != "" {
		data["approval_id"] = result.PendingApprovals[0].RequestID
	}
	return data
}

func (s *jsonRPCServer) writeResult(id json.RawMessage, result any) error {
	return s.write(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *jsonRPCServer) writeError(id json.RawMessage, code int, message string, data any) error {
	return s.write(jsonRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &jsonRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	})
}

func (s *jsonRPCServer) write(response jsonRPCResponse) error {
	if err := s.writeLocked(response); err != nil {
		return fmt.Errorf("write JSON-RPC response: %w", err)
	}
	return nil
}

// writeProgress sends one "progress" notification. It shares the writer lock
// with responses so a notification can never split a response line.
func (s *jsonRPCServer) writeProgress(params map[string]any) error {
	if params == nil {
		return nil
	}
	envelope := progressEnvelope{
		JSONRPC: "2.0",
		Method:  "progress",
		Params:  params,
	}
	if err := s.writeLocked(envelope); err != nil {
		return fmt.Errorf("write JSON-RPC progress: %w", err)
	}
	return nil
}

func (s *jsonRPCServer) writeLocked(value any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	encoder := json.NewEncoder(s.writer)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if flusher, ok := s.writer.(jsonRPCFlusher); ok {
		if err := flusher.Flush(); err != nil {
			return fmt.Errorf("flush: %w", err)
		}
	}
	return nil
}

func runJSONRPC(flowPath string, o cliOverrides) {
	ctx := context.Background()
	bundle, _, err := buildCurrentRuntime(ctx, flowPath, o)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building runtime: %v\n", err)
		os.Exit(1)
	}

	writer := bufio.NewWriter(os.Stdout)
	server := newJSONRPCServer(bundle.Flow, bundle.Definitions.Flow.ID, writer).withSessions(bundle.Sessions)
	if err := server.Serve(ctx, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "JSON-RPC server error: %v\n", err)
		shutdownAndExit(bundle, 1)
	}
	// A normal return is stdin EOF — the documented shutdown for a bridge
	// client. It has to close the runtime like every other exit path does
	// (main.go's shutdownAndExit contract): an MCP server started with
	// transport=stdio is a child process, and leaving it behind leaves an
	// orphan that keeps running with no owner.
	if err := closeJSONRPCBundle(bundle); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: error shutting down runtime: %v\n", err)
	}
}

// runtimeCloser is the part of the built runtime that has to be released on
// exit. Narrowed to an interface so the shutdown contract is testable without
// building a whole bundle.
type runtimeCloser interface {
	Close() error
}

// closeJSONRPCBundle is the normal-exit half of shutdownAndExit: same close,
// without the os.Exit that would skip the caller's own return.
func closeJSONRPCBundle(bundle runtimeCloser) error {
	if bundle == nil {
		return nil
	}
	return bundle.Close()
}
