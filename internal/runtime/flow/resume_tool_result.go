package flow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// SetTranscriptStore wires the per-Agent conversation store. Optional: without
// it a finished Team call cannot reach the conversation that made it, and the
// caller has to poll its task instead.
func (r *Runtime) SetTranscriptStore(store types.AgentTranscriptStore) {
	if r == nil {
		return
	}
	r.transcripts = store
}

// TeamResultSignal is what a finished Team call hands back to the conversation
// that made it.
type TeamResultSignal struct {
	TaskID     string
	TeamID     string
	CallerTeam string
	CallID     string
	Status     string
	Reply      string
	Records    []string
	Error      string
}

// ResumeForToolResult appends a finished Team call's answer to the caller's
// conversation and runs the loop again.
//
// The answer enters as a tool result, not as a user message: a call coming back
// is exactly what `tool` means, so the model continues from it instead of
// re-reading the original question and answering it a second time. The pair
// (Collect call, tool result) is written by the harness because the model
// cannot know when the callee finishes; both messages are marked injected.
//
// Nothing happens when the session is busy or finished: the answer stays on the
// task record and the next turn reads it, rather than interrupting a running
// loop.
func (r *Runtime) ResumeForToolResult(ctx context.Context, sessionID string, signal TeamResultSignal) error {
	if r == nil || r.transcripts == nil || sessionID == "" {
		return nil
	}
	r.resumeMu.Lock()
	defer r.resumeMu.Unlock()

	if _, err := r.loadSession(ctx, sessionID); err != nil {
		return err
	}

	transcriptID := callerTranscriptID(sessionID, signal.CallerTeam, signal.CallID)
	transcript, loadErr := r.transcripts.Load(ctx, transcriptID)
	if loadErr != nil || transcript == nil {
		logging.Info("resume for tool result skipped: no transcript", map[string]any{
			"flow_session_id": sessionID,
			"transcript_id":   transcriptID,
		})
		return nil
	}

	// A completion callback can fire more than once for the same task: the
	// process re-scans terminal tasks on startup so a result finished just
	// before a restart is not lost. Delivering it again would append a second
	// Collect pair and run the turn twice, so the transcript is the queue's
	// ledger — one entry per task id.
	if transcriptHasToolResult(transcript, signal.TaskID) {
		return nil
	}

	// Write the answer first, whatever the session is doing. A busy session
	// cannot run the turn now, but dropping the answer would lose it for good:
	// the completion callback fires once. Persisting it means the next turn —
	// the current one finishing, or the user's next message — reads it from the
	// conversation instead of waiting for an event that already happened.
	toolCallID := newID("call")
	// The tool result is the Team's answer as text, matching what the Team tool
	// itself returns. A status envelope here reads as "task updated" and invites
	// the model to keep dispatching for the real answer.
	content := strings.TrimSpace(signal.Reply)
	switch {
	case signal.Error != "":
		content = signal.Error
	case content == "":
		content = "The team finished without a written answer."
	}
	if len(signal.Records) > 0 {
		content += fmt.Sprintf("\n\n(records: %s)", strings.Join(signal.Records, ", "))
	}
	transcript.Messages = append(transcript.Messages,
		types.Message{
			Role: "assistant",
			ToolCalls: []types.ToolCall{{
				ID:        toolCallID,
				Name:      "Collect",
				Arguments: map[string]any{"handles": []any{signal.TaskID}},
			}},
		},
		types.Message{
			Role:       "tool",
			ToolCallID: toolCallID,
			Content:    content,
		},
	)
	transcript.UpdatedAt = time.Now().UTC()
	if saveErr := r.transcripts.Save(ctx, *transcript); saveErr != nil {
		return saveErr
	}

	// Wait for the session to become idle, up to the limit. A Team's answer
	// often lands while the caller's own wake-up turn is still running; giving
	// up there is what made the result disappear intermittently.
	if waitErr := r.waitForIdle(ctx, sessionID, toolResultWaitLimit); waitErr != nil {
		logging.Info("resume for tool result deferred", map[string]any{
			"flow_session_id": sessionID,
			"error":           waitErr.Error(),
		})
		return nil
	}
	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return err
	}

	activator := signal.CallerTeam
	if activator == "" {
		activator = transcript.TeamID
	}
	// The resumed turn answers the question that dispatched this call, so its
	// record window starts when the call was dispatched — not when the callee
	// finished. Anchoring at "now" excluded the very record that triggered the
	// resume (published a moment earlier), which left the caller's declared
	// flow_records empty and produced no shared_record.consumed edge for the
	// display layer to draw (A→B fixture, 2026-10-28).
	scopeSince := time.Now().UTC()
	if r.tasks != nil {
		if task, taskErr := r.tasks.Load(ctx, signal.TaskID); taskErr == nil && task != nil && !task.CreatedAt.IsZero() {
			scopeSince = task.CreatedAt
		}
	}
	_, runErr := r.runTurnWithOrigin(ctx, r.definitions.Snapshot(), session, "", nil,
		[]activation{{teamID: activator}}, turnOriginTool, scopeSince, nil)
	if runErr != nil {
		return fmt.Errorf("run turn for tool result: %w", runErr)
	}
	return nil
}

// transcriptHasToolResult reports whether this Task already has its Collect
// pair in the conversation, i.e. whether the answer was already delivered.
func transcriptHasToolResult(transcript *types.AgentTranscript, taskID string) bool {
	if transcript == nil || taskID == "" {
		return false
	}
	for _, message := range transcript.Messages {
		for _, call := range message.ToolCalls {
			if call.Name != "Collect" {
				continue
			}
			handles, _ := call.Arguments["handles"].([]any)
			for _, handle := range handles {
				if handle == taskID {
					return true
				}
			}
		}
	}
	return false
}

// callerTranscriptID mirrors the Agent side's transcript naming: one
// conversation per Agent per Flow session.
func callerTranscriptID(flowSessionID, teamID, callID string) string {
	team := teamID
	if team == "" {
		team = "flow"
	}
	call := callID
	if call == "" {
		call = "agent"
	}
	return fmt.Sprintf("%s:%s:%s", flowSessionID, team, call)
}

// toolResultWaitLimit bounds how long a finished Team call waits for the
// session to become idle. A caller's own wake-up turn is usually short; waiting
// past this means something else is running, and the answer stays in the
// transcript for the next turn to pick up.
const toolResultWaitLimit = 45 * time.Second

// waitForIdle polls until the session can accept a turn again.
func (r *Runtime) waitForIdle(ctx context.Context, sessionID string, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		session, err := r.loadSession(ctx, sessionID)
		if err != nil {
			return err
		}
		switch session.Status {
		case types.SessionWaitingInput, types.SessionCreated:
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("session still %s after %s", session.Status, limit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}
