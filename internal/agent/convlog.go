package agent

import (
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ReplayedCompaction is one compaction boundary found in the event log.
type ReplayedCompaction struct {
	Seq          int64
	Summary      string
	DroppedCount int
}

// ReplayedConversation is one agent's conversation rebuilt from the session
// event log.
//
// Two views come out of the same fold, and the difference between them is why
// the log is kept at all:
//
//   - Full is every message the agent ever exchanged. Nothing is dropped, so a
//     question about what happened earlier still has an answer after the model
//     has been compacted several times.
//   - Messages is what the model's context held after the last compaction
//     boundary: the summary, then the messages that survived it.
//
// ReplayConversation is the read side only. It does not touch the transcript
// cache, the context manager, or the event log.
type ReplayedConversation struct {
	Messages    []types.Message
	Full        []types.Message
	Compactions []ReplayedCompaction
}

// ReplayConversation folds the session event log into the conversation one
// agent ran. callTurnID identifies the agent instance (every agent event
// carries it); an empty value replays every agent in the session.
//
// The fold is deterministic: the same events always produce the same messages
// in the same order, which is what makes the log usable as the record rather
// than a debugging aid.
func ReplayConversation(events []storage.SessionEvent, callTurnID string) ReplayedConversation {
	ordered := make([]storage.SessionEvent, 0, len(events))
	for _, event := range events {
		if callTurnID != "" && event.CallTurnID != callTurnID {
			continue
		}
		ordered = append(ordered, event)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Seq < ordered[j].Seq })

	result := ReplayedConversation{
		Messages: make([]types.Message, 0, len(ordered)),
		Full:     make([]types.Message, 0, len(ordered)),
	}
	for _, event := range ordered {
		switch event.Type {
		case types.EventAgentTurnStarted:
			input := strings.TrimSpace(payloadString(event.Payload, "input"))
			if input == "" {
				// A wake-up turn carries no user message of its own.
				continue
			}
			result.append(types.Message{Role: "user", Content: input})

		case types.EventAgentModelResponse:
			message := modelResponseMessage(event.Payload)
			if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 {
				continue
			}
			result.append(message)

		case types.EventToolCallCompleted, types.EventToolCallFailed:
			result.append(toolResultMessage(event.Payload))

		case types.EventContextCompacted:
			result.applyCompaction(ReplayedCompaction{
				Seq:          event.Seq,
				Summary:      payloadString(event.Payload, "summary"),
				DroppedCount: payloadInt(event.Payload, "dropped_count"),
			})
		}
	}
	return result
}

// append records a message in both views.
func (c *ReplayedConversation) append(message types.Message) {
	c.Full = append(c.Full, message)
	c.Messages = append(c.Messages, message)
}

// applyCompaction rewrites the live view the way the manager did: the summary
// replaces everything before it and the last groups survive after it. The full
// view is untouched — that is the history the log exists to keep.
//
// dropped_count counts message groups, not messages, so the split uses the same
// grouping the manager used (an assistant message with tool calls absorbs the
// tool results that answer it).
func (c *ReplayedConversation) applyCompaction(boundary ReplayedCompaction) {
	c.Compactions = append(c.Compactions, boundary)

	messages := c.Messages
	if boundary.DroppedCount > 0 {
		groups := splitMessageGroups(messages)
		dropped := boundary.DroppedCount
		if dropped >= len(groups) {
			dropped = len(groups)
		}
		messages = nil
		for _, group := range groups[dropped:] {
			messages = append(messages, group...)
		}
	}

	next := make([]types.Message, 0, len(messages)+1)
	if strings.TrimSpace(boundary.Summary) != "" {
		next = append(next, types.Message{
			Role:    "user",
			Content: appendCompactionSummary("", boundary.Summary),
		})
	}
	next = append(next, messages...)
	c.Messages = next
}
