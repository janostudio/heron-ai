package main

import (
	"encoding/json"
	"fmt"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Outbound progress notification types.
//
// The values are deliberately the same words core.Event uses in
// heron-connect (assistant / tool_use / tool_result) so the connector maps
// fields without translating meaning. They are NOT a port of any one CLI's
// wire format: claudecode, codex exec, codex appserver and acp all differ,
// and heron-connect converges them onto core.Event with a separate adapter
// each. Heron only has to keep its own projection stable.
const (
	progressTypeFlowTurnStarted   = "flow_turn_started"
	progressTypeFlowTurnCompleted = "flow_turn_completed"
	progressTypeTeamTurnStarted   = "team_turn_started"
	progressTypeTeamTurnCompleted = "team_turn_completed"
	progressTypeCallStarted       = "call_started"
	progressTypeCallCompleted     = "call_completed"
	progressTypeAssistant         = "assistant"
	progressTypeToolUse           = "tool_use"
	progressTypeToolResult        = "tool_result"
	progressTypeRecordPublished   = "record_published"
	progressTypeStatus            = "status"
)

// Field size limits. They are display-oriented budgets, not transport
// limits: the goal is a card a chat platform can render.
const (
	progressMaxInputBytes       = 8 * 1024
	progressMaxResultBytes      = 32 * 1024
	progressMaxTextBytes        = 32 * 1024
	progressMaxNotificationSize = 64 * 1024
	progressTruncatedNotice     = "…[truncated]"
	progressPreviewNotice       = "[truncated]"
)

// progressProjection turns one internal SessionEvent into the flat params
// object of a "progress" notification. It returns nil for events that are
// internal bookkeeping and must not reach a caller.
//
// Fields are FLAT on params on purpose (no nested message/block container):
// heron's model response and tool_call.started are peer events, so merging
// them into an assistant message would mean re-ordering state in the
// projection layer and would force the connector into counter-intuitive
// rules such as "ignore tool_use blocks inside assistant".
func projectSessionEvent(event storage.SessionEvent) map[string]any {
	if event.FlowSessionID == "" || event.FlowTurnID == "" {
		return nil
	}
	base := map[string]any{
		"session_id":   event.FlowSessionID,
		"flow_turn_id": event.FlowTurnID,
		"seq":          event.Seq,
		"event_id":     event.EventID,
	}
	for key, value := range optionalHeaderFields(event.EventHeader) {
		base[key] = value
	}

	switch event.Type {
	case types.EventFlowTurnStarted:
		base["type"] = progressTypeFlowTurnStarted
		copyTurnAnnouncement(base, event.Payload)
		return base
	case types.EventFlowTurnCompleted:
		base["type"] = progressTypeFlowTurnCompleted
		base["status"] = payloadTurnStatus(event.Payload)
		base["error"] = payloadString(event.Payload, "error")
		copyTurnAnnouncement(base, event.Payload)
		return base
	case types.EventTeamTurnStarted:
		base["type"] = progressTypeTeamTurnStarted
		return base
	case types.EventTeamTurnCompleted:
		base["type"] = progressTypeTeamTurnCompleted
		base["status"] = payloadTurnStatus(event.Payload)
		copyTeamUsage(base, event.Payload)
		return base
	case types.EventAgentTurnStarted, types.EventCommandTurnStarted, types.EventWebhookTurnStarted:
		base["type"] = progressTypeCallStarted
		base["call_type"] = string(event.CallType)
		copyCallIdentity(base, event.Payload)
		return base
	case types.EventAgentTurnCompleted, types.EventCommandTurnCompleted, types.EventWebhookTurnCompleted:
		base["type"] = progressTypeCallCompleted
		base["call_type"] = string(event.CallType)
		copyCallIdentity(base, event.Payload)
		copyCallResultSummary(base, event.Payload)
		return base
	case types.EventAgentModelResponse:
		return projectModelResponse(base, event.Payload)
	case types.EventToolCallStarted:
		return projectToolStarted(base, event)
	case types.EventToolCallCompleted:
		return projectToolCompleted(base, event)
	case types.EventToolCallFailed:
		return projectToolFailed(base, event)
	case types.EventSharedRecordPublished:
		return projectRecordPublished(base, event.Payload)
	case types.EventFlowTurnWaitingInput, types.EventFlowTurnWaitingTool, types.EventFlowTurnWaitingApproval:
		base["type"] = progressTypeStatus
		base["status"] = waitingEventStatus(event.Type)
		// A finished turn also lands here when it ends waiting_input, which is
		// every normal turn. Without the reply it carries, a caller would only
		// ever see the turn boundary and never the answer of a turn it did not
		// start.
		copyTurnAnnouncement(base, event.Payload)
		return base
	default:
		return nil
	}
}

// copyTurnAnnouncement hoists the fields a caller needs to relay the answer of
// a turn it is not waiting on: the reply text, who started the turn, and
// whether that reply is an internal step rather than an answer.
//
// Keys are omitted when unset so an older caller decoding a newer engine (or
// the reverse) sees no change in shape for turns that have nothing to relay.
func copyTurnAnnouncement(base map[string]any, payload map[string]any) {
	if payload == nil {
		return
	}
	if reply, ok := payload["reply"].(string); ok && reply != "" {
		base["reply"] = truncateUTF8(reply, progressMaxTextBytes)
	}
	if origin := payloadString(payload, "origin"); origin != "" {
		base["origin"] = origin
	}
	if intermediate, ok := payload["intermediate"].(bool); ok && intermediate {
		base["intermediate"] = true
	}
	// Deliveries are mailbox results the user must receive verbatim. They are
	// hoisted like reply so a caller can relay them without decoding the turn.
	if deliveries, ok := payload["deliveries"].([]types.UserDelivery); ok && len(deliveries) > 0 {
		hoisted := make([]map[string]any, 0, len(deliveries))
		for _, delivery := range deliveries {
			item := map[string]any{"text": truncateUTF8(delivery.Text, progressMaxTextBytes)}
			if delivery.TeamID != "" {
				item["team_id"] = delivery.TeamID
			}
			if delivery.MessageID != "" {
				item["message_id"] = delivery.MessageID
			}
			hoisted = append(hoisted, item)
		}
		base["deliveries"] = hoisted
	}
}

// waitingEventStatus maps the flow_turn.waiting_* event to the session status
// it leaves the session in.
func waitingEventStatus(eventType string) string {
	switch eventType {
	case types.EventFlowTurnWaitingApproval:
		return string(types.SessionWaitingApproval)
	case types.EventFlowTurnWaitingTool:
		return string(types.SessionWaitingTool)
	case types.EventFlowTurnWaitingInput:
		return string(types.SessionWaitingInput)
	default:
		return ""
	}
}

func optionalHeaderFields(header types.EventHeader) map[string]any {
	fields := make(map[string]any, 5)
	if header.TeamID != "" {
		fields["team_id"] = header.TeamID
	}
	if header.TeamSessionID != "" {
		fields["team_session_id"] = header.TeamSessionID
	}
	if header.CallID != "" {
		fields["call_id"] = header.CallID
	}
	if header.CallTurnID != "" {
		fields["call_turn_id"] = header.CallTurnID
	}
	if header.Attempt > 0 {
		fields["attempt"] = header.Attempt
	}
	return fields
}

func projectModelResponse(base map[string]any, payload map[string]any) map[string]any {
	content := make([]any, 0, 2)
	// A structured Agent answers with a JSON envelope carrying its routing
	// fields (next, repo, task) next to the sentence meant for the user.
	// Forward the sentence, never the envelope: a caller renders this as
	// message text. When the envelope has no readable reply, the frame is
	// dropped rather than showing raw JSON.
	text := payloadString(payload, "text")
	if structured, ok := payload["structured"].(bool); ok && structured {
		text = payloadString(payload, "reply")
		base["structured"] = true
	}
	if text != "" {
		content = append(content, map[string]any{
			"type": "text",
			"text": truncateUTF8(text, progressMaxTextBytes),
		})
	}
	for _, call := range payloadToolCalls(payload) {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    call.ID,
			"name":  call.Name,
			"input": compactArguments(call.Arguments),
		})
	}
	// Reasoning is intentionally not forwarded: the projection carries the
	// per-round model output that is shown to a user, not chain of thought.
	if len(content) == 0 {
		return nil
	}

	base["type"] = progressTypeAssistant
	base["content"] = content
	if round, ok := payloadInt(payload, "round"); ok {
		base["round"] = round
	}
	if model := payloadString(payload, "model"); model != "" {
		base["model"] = model
	}
	if finish := payloadString(payload, "finish_reason"); finish != "" {
		base["finish_reason"] = finish
	}
	if !isEmptyUsage(payload["usage"]) {
		base["usage"] = payload["usage"]
	}
	return base
}

func projectToolStarted(base map[string]any, event storage.SessionEvent) map[string]any {
	payload := event.Payload
	base["type"] = progressTypeToolUse
	base["tool_use_id"] = toolUseID(event.EventHeader, payloadString(payload, "tool_call_id"))
	base["name"] = payloadString(payload, "tool_name")
	base["input"] = compactArguments(payloadArguments(payload))
	if round, ok := payloadInt(payload, "round"); ok {
		base["round"] = round
	}
	return base
}

func projectToolCompleted(base map[string]any, event storage.SessionEvent) map[string]any {
	payload := event.Payload
	base["type"] = progressTypeToolResult
	base["tool_use_id"] = toolUseID(event.EventHeader, payloadString(payload, "tool_call_id"))
	base["name"] = payloadString(payload, "tool_name")
	base["content"] = truncateUTF8(payloadString(payload, "content"), progressMaxResultBytes)
	base["is_error"] = false
	base["status"] = "completed"
	if exitCode, ok := payloadInt(payload, "exit_code"); ok {
		base["exit_code"] = exitCode
	}
	return base
}

func projectToolFailed(base map[string]any, event storage.SessionEvent) map[string]any {
	payload := event.Payload
	base["type"] = progressTypeToolResult
	base["tool_use_id"] = toolUseID(event.EventHeader, payloadString(payload, "tool_call_id"))
	base["name"] = payloadString(payload, "tool_name")
	base["content"] = truncateUTF8(payloadString(payload, "error"), progressMaxResultBytes)
	base["is_error"] = true
	base["status"] = "failed"
	return base
}

func projectRecordPublished(base map[string]any, payload map[string]any) map[string]any {
	name, kind, status, summary, revision := payloadRecord(payload)
	if name == "" {
		return nil
	}
	base["type"] = progressTypeRecordPublished
	base["record_name"] = name
	base["kind"] = kind
	base["status"] = status
	base["revision"] = revision
	base["summary"] = truncateUTF8(summary, progressMaxTextBytes)
	return base
}

// toolUseID is the connector-facing pairing key. It has to combine the call
// turn and the attempt because a model tool call id is only unique inside one
// attempt, and two retries of the same call can produce the same model ids.
func toolUseID(header types.EventHeader, modelToolCallID string) string {
	return fmt.Sprintf("%s/%d/%s", header.CallTurnID, header.Attempt, modelToolCallID)
}

func copyCallIdentity(base map[string]any, payload map[string]any) {
	if call, ok := payload["call"].(types.Call); ok {
		if call.ID != "" {
			base["call_id"] = call.ID
		}
		if call.AgentID != "" {
			base["agent_id"] = call.AgentID
		}
		return
	}
	if call, ok := payload["call"].(map[string]any); ok {
		if id, ok := call["id"].(string); ok && id != "" {
			base["call_id"] = id
		}
		if agentID, ok := call["agent_id"].(string); ok && agentID != "" {
			base["agent_id"] = agentID
		}
	}
}

func copyCallResultSummary(base map[string]any, payload map[string]any) {
	if result, ok := payload["call_result"].(types.CallResult); ok {
		base["status"] = string(result.Status)
		if result.Error != "" {
			base["error"] = truncateUTF8(result.Error, progressMaxResultBytes)
		}
		base["tool_calls"] = result.ToolCalls
		if !isEmptyUsage(result.Usage) {
			base["usage"] = result.Usage
		}
		return
	}
	result, ok := payload["call_result"].(map[string]any)
	if !ok {
		return
	}
	// Replay payloads use the struct's Go field names (CallResult has no
	// snake_case json tags on its exported fields).
	if status, ok := result["Status"].(string); ok && status != "" {
		base["status"] = status
	}
	if errText, ok := result["Error"].(string); ok && errText != "" {
		base["error"] = truncateUTF8(errText, progressMaxResultBytes)
	}
	if calls, ok := result["ToolCalls"]; ok {
		base["tool_calls"] = calls
	}
	if usage, ok := result["Usage"]; ok && !isEmptyUsage(usage) {
		base["usage"] = usage
	}
}

func copyTeamUsage(base map[string]any, payload map[string]any) {
	if teamResult, ok := payload["team_result"].(types.TeamTurnResult); ok {
		if teamResult.Error != "" {
			base["error"] = truncateUTF8(teamResult.Error, progressMaxResultBytes)
		}
		if !isEmptyUsage(teamResult.Usage) {
			base["usage"] = teamResult.Usage
		}
		return
	}
	teamResult, ok := payload["team_result"].(map[string]any)
	if !ok {
		return
	}
	if errText, ok := teamResult["Error"].(string); ok && errText != "" {
		base["error"] = truncateUTF8(errText, progressMaxResultBytes)
	}
	if usage, ok := teamResult["Usage"]; ok && !isEmptyUsage(usage) {
		base["usage"] = usage
	}
}

// payloadTurnStatus digests the turn status out of an event payload. Flow
// events nest it as "turn"; team events nest the whole TeamTurnResult as
// "team_result", whose Turn carries the status.
//
// Both typed and map-shaped payloads have to be handled: live subscribers see
// the Go values a runtime appended, while a replay has been through JSON and
// yields map[string]any.
func payloadTurnStatus(payload map[string]any) string {
	if turn, ok := payload["turn"]; ok {
		switch typed := turn.(type) {
		case types.FlowTurn:
			return string(typed.Status)
		case types.TeamTurn:
			return string(typed.Status)
		case map[string]any:
			return payloadString(typed, "status")
		}
		return ""
	}
	if teamResult, ok := payload["team_result"].(types.TeamTurnResult); ok {
		return string(teamResult.Turn.Status)
	}
	if teamResult, ok := payload["team_result"].(map[string]any); ok {
		if turn, ok := teamResult["Turn"].(map[string]any); ok {
			return payloadString(turn, "status")
		}
	}
	return ""
}

func payloadString(payload map[string]any, key string) string {
	value, ok := payload[key].(string)
	if !ok {
		return ""
	}
	return value
}

func payloadInt(payload map[string]any, key string) (int, bool) {
	switch typed := payload[key].(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	default:
		return 0, false
	}
}

func payloadArguments(payload map[string]any) map[string]any {
	arguments, ok := payload["arguments"].(map[string]any)
	if !ok {
		return nil
	}
	return arguments
}

// payloadRecord digests a shared record out of an event payload, accepting
// both the live struct and its replayed JSON form.
func payloadRecord(payload map[string]any) (name, kind, status, summary string, revision int) {
	if record, ok := payload["record"].(types.SharedRecord); ok {
		return record.Name, record.Kind, string(record.Status), record.Summary, record.Revision
	}
	record, ok := payload["record"].(map[string]any)
	if !ok {
		return "", "", "", "", 0
	}
	name, _ = record["name"].(string)
	kind, _ = record["kind"].(string)
	status, _ = record["status"].(string)
	summary, _ = record["summary"].(string)
	revision, _ = payloadInt(record, "revision")
	return name, kind, status, summary, revision
}

func payloadToolCalls(payload map[string]any) []types.ToolCall {
	switch typed := payload["tool_calls"].(type) {
	case []types.ToolCall:
		return typed
	case []any:
		converted := make([]types.ToolCall, 0, len(typed))
		for _, item := range typed {
			switch call := item.(type) {
			case types.ToolCall:
				converted = append(converted, call)
			case map[string]any:
				// Replay payloads carry the struct as decoded JSON.
				entry := types.ToolCall{}
				entry.ID, _ = call["id"].(string)
				entry.Name, _ = call["name"].(string)
				entry.Arguments, _ = call["arguments"].(map[string]any)
				converted = append(converted, entry)
			}
		}
		return converted
	default:
		return nil
	}
}

func isEmptyUsage(value any) bool {
	switch usage := value.(type) {
	case types.TokenUsage:
		return usage.TotalTokens == 0 && usage.PromptTokens == 0 && usage.CompletionTokens == 0
	case map[string]any:
		for _, key := range []string{"total_tokens", "prompt_tokens", "completion_tokens"} {
			if tokens, ok := payloadInt(usage, key); ok && tokens != 0 {
				return false
			}
		}
		return true
	default:
		return true
	}
}

// compactArguments renders tool input for display. The size cap keeps a huge
// argument map (a whole file body, for instance) from dominating the line.
func compactArguments(arguments map[string]any) any {
	if arguments == nil {
		return map[string]any{}
	}
	encoded, err := json.Marshal(arguments)
	if err != nil {
		return map[string]any{"_error": "arguments are not serialisable"}
	}
	if len(encoded) <= progressMaxInputBytes {
		return arguments
	}
	preview := truncateUTF8(string(encoded), progressMaxInputBytes)
	return map[string]any{
		"_truncated": true,
		"preview":    preview,
	}
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune, and
// appends a notice when something was dropped.
func truncateUTF8(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	cut := limit - len(progressTruncatedNotice)
	cut = runeBoundary(s, cut)
	return s[:cut] + progressTruncatedNotice
}

func runeBoundary(s string, index int) int {
	if index >= len(s) {
		return len(s)
	}
	if index <= 0 {
		return 0
	}
	for index > 0 && (s[index]&0xC0) == 0x80 {
		index--
	}
	return index
}

// enforceNotificationSize drops optional fields, then shortens long text,
// until the encoded notification fits budget. params.truncated marks the
// result so a client can tell a full event from a reduced one.
func enforceNotificationSize(params map[string]any) map[string]any {
	if params == nil || encodedSize(params) <= progressMaxNotificationSize {
		return params
	}
	for _, key := range []string{"model", "finish_reason", "usage", "exit_code", "summary"} {
		delete(params, key)
	}
	if encodedSize(params) <= progressMaxNotificationSize {
		params["truncated"] = true
		return params
	}
	for _, key := range []string{"content", "error"} {
		if value, ok := params[key].(string); ok {
			params[key] = truncateUTF8(value, progressMaxResultBytes/4)
		}
	}
	if content, ok := params["content"].([]any); ok {
		params["content"] = shrinkContentBlocks(content)
	}
	if encodedSize(params) > progressMaxNotificationSize {
		params["content"] = []any{progressPreviewNotice}
		delete(params, "input")
	}
	params["truncated"] = true
	return params
}

func shrinkContentBlocks(blocks []any) []any {
	shrunk := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		reduced := make(map[string]any, len(block))
		for key, value := range block {
			reduced[key] = value
		}
		if text, ok := reduced["text"].(string); ok {
			reduced["text"] = truncateUTF8(text, progressMaxResultBytes/4)
		}
		if input, ok := reduced["input"].(map[string]any); ok && len(input) > 0 {
			reduced["input"] = map[string]any{"_truncated": true}
		}
		shrunk = append(shrunk, reduced)
	}
	return shrunk
}

func encodedSize(value any) int {
	encoded, err := json.Marshal(value)
	if err != nil {
		return 0
	}
	return len(encoded)
}

// progressEnvelope is the JSON-RPC notification written for one event.
type progressEnvelope struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params"`
}

// statusNotificationsFromResult covers the case where a turn ends parked in a
// waiting state: the flow_turn.waiting_* event may have been published after
// the subscription window closed, so the terminal result is the authoritative
// source for the pending state.
func statusNotificationFromResult(result types.FlowTurnResult) map[string]any {
	// waiting_input is the normal end of a turn that produced a reply: the
	// terminal response already says so, and emitting a notification for it
	// would hand every caller a second frame per turn. Only states that park
	// the session without a completed answer are worth an out-of-band event.
	switch result.Session.Status {
	case types.SessionWaitingApproval, types.SessionWaitingTool:
	default:
		return nil
	}
	pending := make([]map[string]any, 0, len(result.PendingApprovals))
	for _, approval := range result.PendingApprovals {
		entry := map[string]any{
			"request_id": approval.RequestID,
			"tool_name":  approval.ToolName,
		}
		if approval.CallID != "" {
			entry["call_id"] = approval.CallID
		}
		if approval.Reason != "" {
			entry["reason"] = truncateUTF8(approval.Reason, progressMaxResultBytes)
		}
		pending = append(pending, entry)
	}
	params := map[string]any{
		"session_id":   result.Session.ID,
		"flow_turn_id": result.Turn.ID,
		"status":       string(result.Session.Status),
		"type":         progressTypeStatus,
	}
	if len(pending) > 0 {
		params["pending_approvals"] = pending
	}
	return params
}
