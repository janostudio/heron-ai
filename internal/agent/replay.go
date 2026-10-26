package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
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
	// The task boundary is the most recent user turn, not the current flow
	// turn. Filtering by the current turn alone drops everything the agent did
	// earlier in the same task: a Team woken by a result cannot see that it
	// already dispatched this task and answers the same way again (s17: three
	// identical dispatch replies in one minute).
	chain := currentTaskChain(replay.Events)
	// pendingCallIDs tracks the ids of the most recent assistant tool_calls, in
	// order, so a tool result whose event predates the tool_call_id field can be
	// paired back to its call. Without this, replayed tool messages carry no id
	// and a strict provider rejects the request with "tool calls and tool
	// results do not match" (ISSUE-replay-missing-tool-call-failed).
	var pendingCallIDs []string
	for _, event := range replay.Events {
		if event.CallID != req.CallID {
			continue
		}
		// 只重放当前任务链内的事件。上一轮问题的结论不能进来——模型看到自己
		// 上次的答案，会把新问题当成旧任务继续答（s13：问 cls 日志，回了上一轮
		// genie-agent 的主分支）。但同一条任务链内的历史必须留下：被结果唤醒的
		// Team 需要看到自己上一轮做过什么，否则会重复同一个动作。
		if req.FlowTurnID != "" && event.FlowTurnID != "" {
			inChain := chain != nil && chain[event.FlowTurnID]
			if chain == nil {
				inChain = event.FlowTurnID == req.FlowTurnID
			}
			if !inChain {
				continue
			}
		}
		switch event.Type {
		case types.EventAgentModelResponse:
			msg := modelResponseMessage(event.Payload)
			current = append(current, msg)
			pendingCallIDs = callIDsOf(msg)
		case types.EventToolCallCompleted, types.EventToolCallFailed:
			// A failed tool call also produced a tool-role message in the live
			// context, so replay must restore it too: skipping it left an
			// assistant tool_call with no matching tool result, which a strict
			// provider rejects with "tool calls and tool results do not match".
			msg := toolResultMessage(event.Payload)
			if msg.ToolCallID == "" && len(pendingCallIDs) > 0 {
				// The event predates tool_call_id recording; take the next
				// unclaimed call id from the assistant message before it.
				msg.ToolCallID = pendingCallIDs[0]
			}
			pendingCallIDs = dropFirst(pendingCallIDs, msg.ToolCallID)
			current = append(current, msg)
		case types.EventAgentFeedback:
			// A feedback event may carry the model's raw output (raw_text) when
			// the retry branch it accompanies did not emit a model_response —
			// e.g. a structured-output validation failure. Restore the
			// assistant message first so the rebuilt context matches the
			// in-memory context the retry actually used.
			if raw := payloadString(event.Payload, "raw_text"); raw != "" {
				current = append(current, types.Message{Role: "assistant", Content: raw})
			}
			current = append(current, types.Message{Role: "user", Content: payloadString(event.Payload, "content")})
		case types.EventContextCompacted:
			current = foldCompacted(current, initialLen, payloadString(event.Payload, "summary"), payloadInt(event.Payload, "dropped_count"))
		}
	}
	return current
}

// currentTaskChain collects the flow turns that belong to the task being worked
// on: the most recent user-originated turn and every turn after it.
//
// Turn ids are opaque, so membership is decided by walking the event log
// backwards to the newest user turn rather than by comparing ids.
func currentTaskChain(events []storage.SessionEvent) map[string]bool {
	chain := make(map[string]bool, 4)
	found := false
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.FlowTurnID != "" {
			chain[event.FlowTurnID] = true
		}
		if replayedTurnOrigin(event.Payload) == "user" {
			found = true
			break
		}
	}
	if !found {
		// No user turn in the log (older recordings, or events that never
		// carried the turn record). Fall back to the previous behaviour —
		// replay this turn only — instead of replaying everything.
		return nil
	}
	return chain
}

// replayedTurnOrigin reads the turn origin out of a recorded event payload.
func replayedTurnOrigin(payload map[string]any) string {
	switch turn := payload["turn"].(type) {
	case map[string]any:
		origin, _ := turn["origin"].(string)
		return origin
	case types.FlowTurn:
		return turn.Origin
	}
	return ""
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

// callIDsOf returns the tool_call ids of an assistant message, in order.
func callIDsOf(msg types.Message) []string {
	if len(msg.ToolCalls) == 0 {
		return nil
	}
	ids := make([]string, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		ids = append(ids, tc.ID)
	}
	return ids
}

// dropFirst removes the first occurrence of id from ids, so a tool result that
// answers a specific call does not also claim the next one.
func dropFirst(ids []string, id string) []string {
	for i, v := range ids {
		if v == id {
			return append(ids[:i:i], ids[i+1:]...)
		}
	}
	return ids
}

// toolResultMessage reconstructs the tool message from a tool_call.completed /
// tool_call.failed event payload.
//
// A failed call's payload carries `error` where a completed one carries
// `content`; fall back to the error so the tool message is not empty. The
// tool_call_id is restored when the event recorded it (older events predate the
// field), so the assistant tool_call and its result pair up by id.
func toolResultMessage(payload map[string]any) types.Message {
	content := payloadString(payload, "content")
	if strings.TrimSpace(content) == "" {
		content = payloadString(payload, "error")
	}
	return types.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: payloadString(payload, "tool_call_id"),
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
