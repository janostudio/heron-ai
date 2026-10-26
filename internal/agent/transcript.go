package agent

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
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
func transcriptID(req types.AgentRequest) string {
	if req.FlowSessionID == "" {
		return ""
	}
	team := req.TeamID
	if team == "" {
		team = "flow"
	}
	call := req.CallID
	if call == "" {
		call = req.AgentID
	}
	if call == "" {
		call = "agent"
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
// (system, knowledge, collaborator messages, the trailing request). When a
// transcript exists for the same Agent in this session, the rendered messages
// are appended to it — deduplicated by content, so a record that is handed over
// again on every turn is not repeated in history.
//
// The second return value is nil when there is nothing to continue (first turn,
// changed Agent definition, unreadable store); the caller then falls back to its
// previous reconstruction path.
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
		return rendered, nil
	}
	hash := systemHashOf(rendered)
	if previous.SystemHash != "" && hash != "" && previous.SystemHash != hash {
		// The Agent definition changed under this conversation. The messages
		// above were produced under rules that no longer apply; start over
		// rather than hand the model a history it will misread.
		logging.Info("agent transcript discarded: system prompt changed", map[string]any{
			"transcript_id": id,
			"team_id":       req.TeamID,
		})
		return rendered, nil
	}
	if len(previous.Messages) == 0 {
		// An empty recording carries no conversation; rendering is safer than
		// handing the model a message list without a system prompt.
		return rendered, nil
	}
	if previous.LastTurnID != "" && previous.LastTurnID == req.AgentTurnID {
		// A retry of the turn that last wrote this transcript: its input is
		// already in history.
		return cloneMessages(previous.Messages), previous
	}

	appended := make(map[string]bool, len(previous.Appended)+len(rendered))
	for key := range previous.Appended {
		appended[key] = true
	}
	out := cloneMessages(previous.Messages)
	for index, msg := range rendered {
		if index == 0 && msg.Role == "system" {
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
