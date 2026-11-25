package flow

import (
	"context"
	"strings"
	"sync"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// steeringInbox holds the user messages a running turn has not taken yet.
//
// It lives for the length of one turn and only in memory, and that is not a
// durability hole: SubmitInput stores every message in the mailbox first, so a
// crash still leaves the text, and the end-of-turn drain runs anything the inbox
// never delivered. The inbox only decides whether the message is read now or
// after the turn — not whether it survives.
type steeringInbox struct {
	mu    sync.Mutex
	items []steeringItem
}

type steeringItem struct {
	message types.MailboxMessage
	blocks  []types.ContextBlock
}

func (i *steeringInbox) push(item steeringItem) {
	i.mu.Lock()
	i.items = append(i.items, item)
	i.mu.Unlock()
}

// take removes everything queued so far. The caller is the Agent loop, at a
// round boundary: taking the whole batch means two messages sent while one slow
// tool ran are read together instead of across two rounds.
func (i *steeringInbox) take() []steeringItem {
	i.mu.Lock()
	defer i.mu.Unlock()
	items := i.items
	i.items = nil
	return items
}

// registerSteeringInbox publishes the inbox for the turn that is starting and
// returns the cleanup for when it ends.
func (r *Runtime) registerSteeringInbox(sessionID string) func() {
	if sessionID == "" {
		return func() {}
	}
	inbox := &steeringInbox{}
	r.steerMu.Lock()
	if r.steering == nil {
		r.steering = make(map[string]*steeringInbox)
	}
	r.steering[sessionID] = inbox
	r.steerMu.Unlock()
	return func() {
		r.steerMu.Lock()
		// Only remove our own inbox: a resumed or replacement turn may have
		// published a newer one, and deleting that would strand its messages.
		if r.steering[sessionID] == inbox {
			delete(r.steering, sessionID)
		}
		r.steerMu.Unlock()
	}
}

func (r *Runtime) steeringInbox(sessionID string) *steeringInbox {
	r.steerMu.Lock()
	defer r.steerMu.Unlock()
	return r.steering[sessionID]
}

// steerUserInput hands a just-stored message to the turn that is running.
//
// It reports whether a live turn took it. False means no inbox is published —
// the turn ended between the busy check and here, or another process owns it —
// and the message stays queued, which is the previous behaviour and still
// correct: the end-of-turn drain runs it as its own turn.
func (r *Runtime) steerUserInput(sessionID string, message types.MailboxMessage, blocks []types.ContextBlock) bool {
	inbox := r.steeringInbox(sessionID)
	if inbox == nil {
		return false
	}
	inbox.push(steeringItem{message: message, blocks: blocks})
	return true
}

// steeringSource builds the poll the Agent loop calls before each model round.
//
// Two things happen per message here, and both matter: the mailbox copy is
// marked delivered (so the end-of-turn drain does not run the same text a second
// time as its own turn), and one event records that the input went into a
// running turn. Without the event the session would show a message that vanished
// and an answer that silently accounted for it.
func (r *Runtime) steeringSource(ctx context.Context, session types.FlowSession, flowTurnID string) types.SteeringSource {
	inbox := r.steeringInbox(session.ID)
	if inbox == nil {
		return nil
	}
	return func() []types.SteeringInput {
		items := inbox.take()
		if len(items) == 0 {
			return nil
		}
		inputs := make([]types.SteeringInput, 0, len(items))
		for _, item := range items {
			if _, err := r.deliverMessage(ctx, session, item.message, nil); err != nil {
				// The turn still gets the text: a failed status write is a
				// bookkeeping problem, and dropping the correction because of it
				// would be the bug this feature exists to fix.
				logging.Error("mark steered message delivered", map[string]any{
					"flow_session_id": session.ID,
					"message_id":      item.message.MessageID,
					"error":           err.Error(),
				})
			}
			text := contextBlockTextOf(item.blocks)
			if err := r.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
				EventHeader: types.EventHeader{
					Type:          types.EventUserInputInjected,
					FlowSessionID: session.ID,
					FlowTurnID:    flowTurnID,
				},
				Payload: map[string]any{
					"message_id": item.message.MessageID,
					"input":      text,
				},
			}); err != nil {
				logging.Error("record injected user input", map[string]any{
					"flow_session_id": session.ID,
					"message_id":      item.message.MessageID,
					"error":           err.Error(),
				})
			}
			inputs = append(inputs, types.SteeringInput{
				MessageID: item.message.MessageID,
				Blocks:    item.blocks,
			})
		}
		return inputs
	}
}

// withInputBlock makes sure the text the user typed is in the blocks that travel
// with the message. A caller that sends plain text and no attachments passes no
// blocks; steering that carries none would inject nothing, which reads as "the
// message was accepted and ignored".
func withInputBlock(input string, blocks []types.ContextBlock) []types.ContextBlock {
	if strings.TrimSpace(input) == "" {
		return blocks
	}
	for _, block := range blocks {
		if block.Kind == "input" && strings.TrimSpace(block.Text) != "" {
			return blocks
		}
	}
	out := make([]types.ContextBlock, 0, len(blocks)+1)
	out = append(out, types.ContextBlock{
		Kind: "input", Text: input, Source: "user",
		Stability: "dynamic", Priority: 80, Compressible: true,
	})
	return append(out, blocks...)
}

// contextBlockTextOf joins the input blocks of one message. It mirrors the Agent
// loop's own join so the recorded text and the injected text are the same bytes.
func contextBlockTextOf(blocks []types.ContextBlock) string {
	var text string
	for _, block := range blocks {
		if block.Kind != "input" {
			continue
		}
		if block.Text == "" {
			continue
		}
		if text != "" {
			text += "\n\n"
		}
		text += block.Text
	}
	return text
}
