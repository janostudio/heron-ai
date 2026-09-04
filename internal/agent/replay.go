package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// replayAgentContext replays the agent-internal events recorded in agent.jsonl
// for a single call, reconstructing the active message list from the previous
// execution. It is used so a normally-completed agent turn can be continued in
// a later session without losing its compacted context.
//
// initial is the freshly rendered system + user prompt. The replay appends the
// runtime increments (model responses, tool results, feedback) on top of it,
// and honors context.compacted events as fold points that replace the
// accumulated messages with the surviving snapshot.
func (t *TurnLoop) replayAgentContext(ctx context.Context, req types.AgentRequest, initial []types.Message) []types.Message {
	if t.sessionWriter == nil || strings.TrimSpace(req.FlowSessionID) == "" || req.CallID == "" {
		return initial
	}

	replay, err := t.sessionWriter.Replay(ctx, req.FlowSessionID)
	if err != nil || replay == nil {
		return initial
	}

	current := cloneMessages(initial)
	initialLen := len(current)
	for _, event := range replay.Events {
		if event.CallID != req.CallID {
			continue
		}
		switch event.Type {
		case types.EventAgentModelResponse:
			current = append(current, modelResponseMessage(event.Payload))
		case types.EventToolCallCompleted:
			current = append(current, toolResultMessage(event.Payload))
		case types.EventAgentFeedback:
			current = append(current, types.Message{Role: "user", Content: payloadString(event.Payload, "content")})
		case types.EventContextCompacted:
			current = foldCompacted(current, initialLen, payloadString(event.Payload, "summary"), payloadInt(event.Payload, "dropped_count"))
		}
	}
	return current
}

// foldCompacted replaces the oldest droppedCount message groups with the
// compaction summary, mirroring what compactLocked does in-memory. Only the
// replayed increments (beyond initialLen) are grouped and folded; the freshly
// rendered system + user prompt is preserved. A group is one assistant message
// (optionally followed by its tool results).
func foldCompacted(messages []types.Message, initialLen int, summary string, droppedCount int) []types.Message {
	if droppedCount <= 0 {
		return messages
	}
	groups := splitMessageGroups(messages[initialLen:])
	if droppedCount >= len(groups) {
		droppedCount = len(groups)
	}
	kept := groups[droppedCount:]
	result := make([]types.Message, 0, len(messages)+1)
	result = append(result, messages[:initialLen]...)
	if summary != "" {
		result = append(result, types.Message{Role: "user", Content: appendCompactionSummary("", summary)})
	}
	for _, group := range kept {
		result = append(result, group...)
	}
	return result
}

// splitMessageGroups partitions messages into groups, where an assistant
// message with tool calls absorbs the immediately following tool messages.
func splitMessageGroups(messages []types.Message) [][]types.Message {
	var groups [][]types.Message
	for i := 0; i < len(messages); {
		group := []types.Message{messages[i]}
		if messages[i].Role == "assistant" && len(messages[i].ToolCalls) > 0 {
			for i+1 < len(messages) && messages[i+1].Role == "tool" {
				group = append(group, messages[i+1])
				i++
			}
		}
		groups = append(groups, group)
		i++
	}
	return groups
}

func payloadInt(payload map[string]any, key string) int {
	if payload == nil {
		return 0
	}
	switch v := payload[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

// modelResponseMessage reconstructs the assistant message from an
// agent.model_response event payload.
func modelResponseMessage(payload map[string]any) types.Message {
	msg := types.Message{Role: "assistant", Content: payloadString(payload, "text")}
	if raw, ok := payload["tool_calls"]; ok {
		var toolCalls []types.ToolCall
		if err := decodeJSON(raw, &toolCalls); err == nil {
			msg.ToolCalls = toolCalls
		}
	}
	return msg
}

// toolResultMessage reconstructs the tool message from a tool_call.completed
// event payload.
func toolResultMessage(payload map[string]any) types.Message {
	return types.Message{
		Role:    "tool",
		Content: payloadString(payload, "content"),
		// ToolCallID is not currently recorded in the event; the role/content
		// pairing is sufficient to rebuild a usable context.
	}
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if v, ok := payload[key].(string); ok {
		return v
	}
	return ""
}

// decodeJSON round-trips a payload value (typically a map[string]any produced
// by the storage layer's JSON decode) back into a typed target.
func decodeJSON(value any, target any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
