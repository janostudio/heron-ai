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
	"time"

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
}

type jsonRPCTurnResult struct {
	SessionID  string                 `json:"session_id"`
	FlowTurnID string                 `json:"flow_turn_id,omitempty"`
	Status     types.SessionStatus    `json:"status"`
	Reply      string                 `json:"reply,omitempty"`
	Records    []jsonRPCRecordSummary `json:"records,omitempty"`
	Usage      types.TokenUsage       `json:"usage,omitempty"`
	Error      string                 `json:"error,omitempty"`
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
	runtime types.FlowRuntime
	flowID  string
	writer  io.Writer
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

	result, err := executeFlowTurn(ctx, s.runtime, s.flowID, params.SessionID, params.Input)
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
		return s.writeError(id, code, err.Error(), turnErrorData(result, params.SessionID))
	}

	return s.writeResult(id, jsonRPCTurnResultFrom(result))
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

	return jsonRPCTurnResult{
		SessionID:        result.Session.ID,
		FlowTurnID:       result.Turn.ID,
		Status:           result.Session.Status,
		Reply:            result.Reply,
		Records:          records,
		Usage:            aggregateTeamUsage(result.TeamResults),
		Error:            result.Error,
		PendingApprovals: pending,
	}
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
	encoder := json.NewEncoder(s.writer)
	if err := encoder.Encode(response); err != nil {
		return fmt.Errorf("write JSON-RPC response: %w", err)
	}
	if flusher, ok := s.writer.(jsonRPCFlusher); ok {
		if err := flusher.Flush(); err != nil {
			return fmt.Errorf("flush JSON-RPC response: %w", err)
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
	server := newJSONRPCServer(bundle.Flow, bundle.Definitions.Flow.ID, writer)
	if err := server.Serve(ctx, os.Stdin); err != nil {
		fmt.Fprintf(os.Stderr, "JSON-RPC server error: %v\n", err)
		os.Exit(1)
	}
}
