package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/logging"
	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// ErrTranscriptNotFound is returned when an Agent has no conversation recorded
// for this Flow session yet — the first turn of every session.
var ErrTranscriptNotFound = errors.New("agent transcript not found")

// transcriptVersion guards the on-disk shape. A transcript written by an older
// engine is ignored rather than misinterpreted: continuing a conversation whose
// messages were rendered under different rules is worse than starting fresh.
const transcriptVersion = 1

// FileTranscriptStore keeps one conversation per Agent per Flow session.
type FileTranscriptStore struct {
	files storage.FileStore
	mu    sync.Mutex
}

func NewFileTranscriptStore(files storage.FileStore) *FileTranscriptStore {
	return &FileTranscriptStore{files: files}
}

func (s *FileTranscriptStore) Save(ctx context.Context, transcript types.AgentTranscript) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.files == nil {
		return errors.New("transcript file store is not configured")
	}
	if transcript.ID == "" {
		return errors.New("transcript id is required")
	}
	transcript.Version = transcriptVersion
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(transcript, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent transcript: %w", err)
	}
	return s.files.Write(transcriptPath(transcript.ID), data)
}

func (s *FileTranscriptStore) Load(ctx context.Context, id string) (*types.AgentTranscript, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.files == nil {
		return nil, errors.New("transcript file store is not configured")
	}
	if id == "" {
		return nil, errors.New("transcript id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := s.files.Read(transcriptPath(id))
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return nil, ErrTranscriptNotFound
		}
		return nil, err
	}
	var transcript types.AgentTranscript
	if err := json.Unmarshal(data, &transcript); err != nil {
		return nil, fmt.Errorf("decode agent transcript: %w", err)
	}
	if transcript.Version != transcriptVersion {
		return nil, ErrTranscriptNotFound
	}
	return &transcript, nil
}

func transcriptPath(id string) string {
	return filepath.Join(".agents", "data", "agent-transcripts", id+".json")
}

// transcriptID names one Agent's conversation. It is stable for the whole Flow
// session: the same Team call in a later turn resolves to the same id, which is
// what lets the next turn append instead of rebuilding.
//
// The Team instance leads the key: a resident instance's id is
// "<flowSession>:<team>", which reproduces the historical
// "<flowSession>:<team>:<call>" key exactly, while a per_task instance
// ("<flowSession>:<team>:<seq>") gets a conversation of its own.
func transcriptID(req types.AgentRequest) string {
	if req.FlowSessionID == "" {
		return ""
	}
	call := req.CallID
	if call == "" {
		call = req.AgentID
	}
	if call == "" {
		call = "agent"
	}
	if req.TeamSessionID != "" {
		return fmt.Sprintf("%s:%s", req.TeamSessionID, call)
	}
	team := req.TeamID
	if team == "" {
		team = "flow"
	}
	return fmt.Sprintf("%s:%s:%s", req.FlowSessionID, team, call)
}

// SetTranscriptStore wires the per-session conversation store. Optional: a loop
// without it rebuilds each turn from events, which is the previous behaviour.
func (t *TurnLoop) SetTranscriptStore(store types.AgentTranscriptStore) {
	if t == nil {
		return
	}
	t.transcripts = store
}

// continueTranscript returns the conversation to run this turn on.
//
// The rendered prompt is not the conversation: it is this turn's own input
// (system, reminder block, collaborator messages, the trailing request). When a
// transcript exists for the same Agent in this session, the rendered messages
// are appended to it — deduplicated by content, so a record that is handed over
// again on every turn is not repeated in history. A definition change replaces
// the system prompt and keeps the history; the messages are what happened, and
// a model that loses them repeats finished work.
//
// The second return value is nil when there is nothing to continue (first turn,
// unusable store with nothing to rebuild from); the caller then falls back to
// its previous reconstruction path.
func (t *TurnLoop) continueTranscript(ctx context.Context, req types.AgentRequest, rendered []types.Message) ([]types.Message, *types.AgentTranscript) {
	if t.transcripts == nil {
		return rendered, nil
	}
	id := transcriptID(req)
	if id == "" {
		return rendered, nil
	}
	previous, err := t.transcripts.Load(ctx, id)
	if err != nil || previous == nil {
		// The transcript store is a cache, not the record. When there is nothing
		// usable on disk, the session event log still knows the conversation:
		// rebuild instead of starting over.
		//
		// Rebuilding is only safe when the file is genuinely absent or from an
		// older version. A transient read error says nothing about the content,
		// and rebuilding from the log throws away the dedup table — a duplicate
		// record block costs more than waiting for the file to be readable.
		if err == nil || errors.Is(err, ErrTranscriptNotFound) {
			previous = t.rebuildTranscript(ctx, req, id)
		}
		if previous == nil {
			return rendered, nil
		}
	}
	hash := systemHashOf(rendered)
	// The Agent definition changed under this conversation. Only the prompt
	// changed: the messages above are still what happened, and dropping them
	// costs the task itself — a model that starts from an empty history
	// re-dispatches work it already finished. Swap the prompt, keep the record.
	systemChanged := previous.SystemHash != "" && hash != "" && previous.SystemHash != hash
	if systemChanged {
		logging.Info("agent transcript kept under new system prompt", map[string]any{
			"transcript_id": id,
			"team_id":       req.TeamID,
			"messages":      len(previous.Messages),
		})
	}
	if len(previous.Messages) == 0 {
		// An empty recording carries no conversation; rendering is safer than
		// handing the model a message list without a system prompt.
		return rendered, nil
	}

	out := cloneMessages(previous.Messages)
	if systemChanged {
		out = replaceSystemPrompt(out, rendered)
	}
	if previous.LastTurnID != "" && previous.LastTurnID == req.AgentTurnID {
		// A retry of the turn that last wrote this transcript: its input is
		// already in history.
		return out, previous
	}

	appended := make(map[string]bool, len(previous.Appended)+len(rendered))
	for key := range previous.Appended {
		appended[key] = true
	}
	// Only replace when this turn renders a reminder of its own. A turn that
	// carries none must not delete the one already in the conversation: that
	// copy is the only pointer the model has to knowledge and outstanding work.
	//
	// The dropped message's dedup key goes with it. The reminder text can repeat
	// word for word (the task list shows ages in whole minutes), and a surviving
	// key would make the append below skip the new copy — leaving the
	// conversation with no reminder at all.
	if hasRebuiltBlock(rendered) {
		kept := make([]types.Message, 0, len(out))
		for _, message := range out {
			if isRebuiltBlock(message) {
				delete(appended, messageKey(message))
				continue
			}
			kept = append(kept, message)
		}
		out = kept
	}
	// A conversation that ends on a tool result is waiting for the model to act
	// on it. The renderer always closes with a user turn so the model answers;
	// appending that placeholder here would replace the tool result as the last
	// message and turn a finished call back into a question.
	endsOnToolResult := len(previous.Messages) > 0 &&
		previous.Messages[len(previous.Messages)-1].Role == "tool"
	for index, msg := range rendered {
		if index == 0 && msg.Role == "system" {
			continue
		}
		if endsOnToolResult && msg.Role == "user" && strings.TrimSpace(msg.Content) == types.ContinuePrompt {
			continue
		}
		key := messageKey(msg)
		if appended[key] {
			continue
		}
		appended[key] = true
		out = append(out, msg)
	}
	previous.Appended = appended
	return out, previous
}

// saveTranscript writes the conversation as it stands at the end of a turn.
//
// base is the rendered prompt of this turn, used on the first turn to record
// which messages were already present so a later turn does not append them
// twice.
func (t *TurnLoop) saveTranscript(ctx context.Context, req types.AgentRequest, messages []types.Message, continued *types.AgentTranscript, base []types.Message) {
	if t.transcripts == nil {
		return
	}
	id := transcriptID(req)
	if id == "" {
		return
	}
	appended := map[string]bool{}
	if continued != nil && len(continued.Appended) > 0 {
		for key := range continued.Appended {
			appended[key] = true
		}
	} else {
		for index, msg := range base {
			if index == 0 && msg.Role == "system" {
				continue
			}
			appended[messageKey(msg)] = true
		}
	}
	transcript := types.AgentTranscript{
		ID:            id,
		FlowSessionID: req.FlowSessionID,
		TeamID:        req.TeamID,
		CallID:        req.CallID,
		AgentID:       req.AgentID,
		AgentTurnID:   req.AgentTurnID,
		SystemHash:    systemHashOf(messages),
		Messages:      messages,
		LastTurnID:    req.AgentTurnID,
		Appended:      appended,
		UpdatedAt:     time.Now().UTC(),
	}
	if err := t.transcripts.Save(ctx, transcript); err != nil {
		logging.Warn("agent transcript save failed", map[string]any{
			"transcript_id": id,
			"error":         err.Error(),
		})
	}
}

// systemHashOf fingerprints the system prompt, so a turn can tell whether the
// conversation above it was produced under the same Agent definition.
func systemHashOf(messages []types.Message) string {
	if len(messages) == 0 || messages[0].Role != "system" {
		return ""
	}
	sum := sha1.Sum([]byte(messages[0].Content))
	return hex.EncodeToString(sum[:])
}

// messageKey identifies one message by speaker and body, for deduplication
// across turns. Two turns that hand over the same record produce the same key.
func messageKey(msg types.Message) string {
	sum := sha1.Sum([]byte(msg.Role + "\x00" + msg.TeamID + "\x00" + msg.Content))
	return msg.Role + ":" + hex.EncodeToString(sum[:8])
}

// isRebuiltBlock reports whether the renderer owns this message, i.e. it is
// regenerated from records every turn and the previous copy is obsolete.
func isRebuiltBlock(message types.Message) bool {
	return message.BlockType == types.BlockTypeHarnessReminder
}

// hasRebuiltBlock reports whether this turn's render contains a block the
// renderer rebuilds.
func hasRebuiltBlock(messages []types.Message) bool {
	for _, message := range messages {
		if isRebuiltBlock(message) {
			return true
		}
	}
	return false
}

// replaceSystemPrompt puts this turn's system prompt at the head of a recorded
// conversation, leaving the rest of it alone.
//
// history is the conversation as recorded under the previous Agent definition.
// The definition is what changed, so the prompt is the only part that is
// stale; the messages below it are what this Agent actually did, and a
// continuation that forgets them makes the model repeat finished work.
func replaceSystemPrompt(history []types.Message, rendered []types.Message) []types.Message {
	var system types.Message
	for _, message := range rendered {
		if message.Role == "system" {
			system = message
			break
		}
	}
	if system.Role == "" {
		// Nothing to swap in: keep the recorded prompt rather than leave the
		// conversation without one.
		return history
	}
	for index, message := range history {
		if message.Role == "system" {
			history[index] = system
			return history
		}
	}
	return append([]types.Message{system}, history...)
}

// rebuildTranscript reconstructs one agent's conversation from the session
// event log.
//
// The log is append-only and holds every message the agent exchanged, so a lost
// or stale transcript is a cache miss, not a lost conversation. Rebuilding is
// deterministic: ReplayConversation folds the same events into the same
// messages, applying each compaction boundary it finds.
//
// Returns nil when there is nothing to rebuild from: no writer, no session id,
// or a log with no agent events yet.
func (t *TurnLoop) rebuildTranscript(ctx context.Context, req types.AgentRequest, id string) *types.AgentTranscript {
	if t.sessionWriter == nil || req.FlowSessionID == "" || req.CallTurnID == "" {
		return nil
	}
	replay, err := t.sessionWriter.Replay(ctx, req.FlowSessionID)
	if err != nil || replay == nil || len(replay.Events) == 0 {
		return nil
	}
	// The current turn already reaches the model through the rendered prompt.
	// Replaying its events on top would hand the model the same user message
	// twice, so the rebuild stops at the turn boundary.
	events := make([]storage.SessionEvent, 0, len(replay.Events))
	for _, event := range replay.Events {
		if req.FlowTurnID != "" && event.FlowTurnID == req.FlowTurnID {
			continue
		}
		events = append(events, event)
	}
	conversation := ReplayConversation(events, req.CallTurnID)
	if len(conversation.Messages) == 0 {
		return nil
	}
	logging.Info("agent transcript rebuilt from session events", map[string]any{
		"transcript_id": id,
		"messages":      len(conversation.Messages),
		"compactions":   len(conversation.Compactions),
	})
	// Seed the dedup table from what was replayed. Without it the next turn
	// re-appends context that is already in the conversation: the same record
	// block handed over again looks new because the table starts empty.
	appended := make(map[string]bool, len(conversation.Messages))
	for _, message := range conversation.Messages {
		appended[messageKey(message)] = true
	}
	return &types.AgentTranscript{
		ID:            id,
		FlowSessionID: req.FlowSessionID,
		TeamID:        req.TeamID,
		CallID:        req.CallID,
		AgentID:       req.AgentID,
		AgentTurnID:   req.AgentTurnID,
		Messages:      conversation.Messages,
		Appended:      appended,
	}
}
