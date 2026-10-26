package flow

import (
	"context"
	"encoding/json"
	"fmt"
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

	session, err := r.loadSession(ctx, sessionID)
	if err != nil {
		return err
	}
	switch session.Status {
	case types.SessionWaitingInput, types.SessionCreated:
	default:
		logging.Debug("resume for tool result skipped: session not idle", map[string]any{
			"flow_session_id": sessionID,
			"session_status":  string(session.Status),
		})
		return nil
	}

	transcriptID := callerTranscriptID(sessionID, signal.CallerTeam, signal.CallID)
	transcript, loadErr := r.transcripts.Load(ctx, transcriptID)
	if loadErr != nil || transcript == nil {
		logging.Debug("resume for tool result skipped: no transcript", map[string]any{
			"flow_session_id": sessionID,
			"transcript_id":   transcriptID,
		})
		return nil
	}

	// The result is appended in the protocol's own shape: a tool call for the
	// handle the model already holds, then its result.
	toolCallID := newID("call")
	payload := map[string]any{"status": signal.Status, "reply": signal.Reply}
	if len(signal.Records) > 0 {
		payload["records"] = signal.Records
	}
	if signal.Error != "" {
		payload["error"] = signal.Error
	}
	content, _ := json.Marshal(payload)
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
			Content:    string(content),
		},
	)
	transcript.UpdatedAt = time.Now().UTC()
	if saveErr := r.transcripts.Save(ctx, *transcript); saveErr != nil {
		return saveErr
	}

	activator := signal.CallerTeam
	if activator == "" {
		activator = transcript.TeamID
	}
	_, runErr := r.runTurnWithOrigin(ctx, r.definitions.Snapshot(), session, "", nil,
		[]activation{{teamID: activator}}, turnOriginTool, time.Now().UTC(), nil)
	if runErr != nil {
		return fmt.Errorf("run turn for tool result: %w", runErr)
	}
	return nil
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
