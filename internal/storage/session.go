package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// EventLayer identifies which layer runtime produced an event, and therefore
// which per-layer jsonl file it is persisted to.
type EventLayer int

const (
	LayerFlow EventLayer = iota
	LayerTeam
	LayerAgent
)

// layerFile maps an EventLayer to its per-session jsonl file name.
func layerFile(layer EventLayer) string {
	switch layer {
	case LayerTeam:
		return "team.jsonl"
	case LayerAgent:
		return "agent.jsonl"
	default:
		return "flow.jsonl"
	}
}

// SessionEvent is the on-disk session event envelope. It is the union of the
// three layer event types (types.FlowEvent / types.TeamEvent / types.AgentEvent):
// the common types.EventHeader plus the agent-specific AgentTurnID/Round and the
// business Payload. A single type keeps Replay/Subscribe able to merge the three
// per-layer files into one globally-ordered timeline.
type SessionEvent struct {
	types.EventHeader
	AgentTurnID string         `json:"agent_turn_id,omitempty"`
	Round       int            `json:"round,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
}

// SessionReplay is the ordered event view reconstructed from the three layer
// files. The storage layer does not interpret business state; Runtime can fold
// Events into FlowSession/Turn state later.
type SessionReplay struct {
	SessionID string
	Events    []SessionEvent
	LastSeq   int64
}

// SessionWriter appends ordered session events and replays them. Append routes
// each event to the per-layer file derived from the EventLayer, but allocates
// sequence numbers from one global counter per session so the three files can
// always be re-merged into a single monotonic timeline.
type SessionWriter interface {
	Append(ctx context.Context, sessionID string, layer EventLayer, event SessionEvent) (SessionEvent, error)
	Replay(ctx context.Context, sessionID string) (*SessionReplay, error)
	Subscribe(ctx context.Context, sessionID string, afterSeq int64) (<-chan SessionEvent, error)
}

// JSONLSessionWriter is a single-writer implementation backed by the current
// FileStore. A mutex protects sequence allocation and append ordering even
// when Team calls finish in parallel.
type JSONLSessionWriter struct {
	fileStore FileStore
	mu        sync.Mutex
	nextSeq   map[string]int64
	subs      map[string]map[int]chan SessionEvent
	nextSubID int
}

func NewJSONLSessionWriter(fileStore FileStore) *JSONLSessionWriter {
	return &JSONLSessionWriter{
		fileStore: fileStore,
		nextSeq:   make(map[string]int64),
		subs:      make(map[string]map[int]chan SessionEvent),
	}
}

func (w *JSONLSessionWriter) Append(ctx context.Context, sessionID string, layer EventLayer, event SessionEvent) (SessionEvent, error) {
	if err := contextErr(ctx); err != nil {
		return SessionEvent{}, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return SessionEvent{}, errors.New("session id is required")
	}
	if strings.TrimSpace(event.Type) == "" {
		return SessionEvent{}, errors.New("event type is required")
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if _, ok := w.nextSeq[sessionID]; !ok {
		replay, err := w.replayLocked(sessionID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return SessionEvent{}, err
		}
		if replay != nil {
			w.nextSeq[sessionID] = replay.LastSeq + 1
		} else {
			w.nextSeq[sessionID] = 1
		}
	}

	event.Seq = w.nextSeq[sessionID]
	w.nextSeq[sessionID]++
	if strings.TrimSpace(event.EventID) == "" {
		event.EventID = fmt.Sprintf("%s-%d", sessionID, event.Seq)
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now()
	}

	data, err := json.Marshal(event)
	if err != nil {
		return SessionEvent{}, fmt.Errorf("marshal session event: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(".agents", "data", "sessions", sessionID, layerFile(layer))
	if err := w.fileStore.Append(path, data); err != nil {
		return SessionEvent{}, fmt.Errorf("append session event: %w", err)
	}
	w.publishLocked(sessionID, event)
	return event, nil
}

func (w *JSONLSessionWriter) Subscribe(ctx context.Context, sessionID string, afterSeq int64) (<-chan SessionEvent, error) {
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("session id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	w.mu.Lock()
	replay, err := w.replayLocked(sessionID)
	if errors.Is(err, ErrNotFound) {
		w.mu.Unlock()
		return nil, ErrNotFound
	}
	if err != nil {
		w.mu.Unlock()
		return nil, err
	}
	if replay == nil {
		w.mu.Unlock()
		return nil, ErrNotFound
	}
	ch := make(chan SessionEvent, len(replay.Events)+64)
	w.nextSubID++
	subID := w.nextSubID
	if w.subs[sessionID] == nil {
		w.subs[sessionID] = make(map[int]chan SessionEvent)
	}
	w.subs[sessionID][subID] = ch
	for _, event := range replay.Events {
		if event.Seq > afterSeq {
			ch <- event
		}
	}
	w.mu.Unlock()

	go func() {
		<-ctx.Done()
		w.mu.Lock()
		if subscribers := w.subs[sessionID]; subscribers != nil {
			if _, exists := subscribers[subID]; exists {
				delete(subscribers, subID)
				close(ch)
				if len(subscribers) == 0 {
					delete(w.subs, sessionID)
				}
			}
		}
		w.mu.Unlock()
	}()
	return ch, nil
}

func (w *JSONLSessionWriter) publishLocked(sessionID string, event SessionEvent) {
	for subID, ch := range w.subs[sessionID] {
		select {
		case ch <- event:
		default:
			// Disconnect slow subscribers. They can reconnect with
			// Last-Event-ID and receive the missed events from JSONL.
			delete(w.subs[sessionID], subID)
			close(ch)
		}
	}
	if len(w.subs[sessionID]) == 0 {
		delete(w.subs, sessionID)
	}
}

func (w *JSONLSessionWriter) Replay(ctx context.Context, sessionID string) (*SessionReplay, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, errors.New("session id is required")
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	return w.replayLocked(sessionID)
}

func (w *JSONLSessionWriter) replayLocked(sessionID string) (*SessionReplay, error) {
	replay := &SessionReplay{SessionID: sessionID}

	// Read every layer file and merge into one globally-ordered timeline. A
	// layer file that has not been written yet simply contributes no events.
	layers := []EventLayer{LayerFlow, LayerTeam, LayerAgent}
	found := false
	for _, layer := range layers {
		path := filepath.Join(".agents", "data", "sessions", sessionID, layerFile(layer))
		data, err := w.fileStore.Read(path)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found = true
		if err := decodeEventLines(data, replay); err != nil {
			return nil, err
		}
	}

	if !found || len(replay.Events) == 0 {
		return nil, ErrNotFound
	}
	sort.SliceStable(replay.Events, func(i, j int) bool {
		return replay.Events[i].Seq < replay.Events[j].Seq
	})
	return replay, nil
}

func decodeEventLines(data []byte, replay *SessionReplay) error {
	content := string(data)
	lines := strings.Split(content, "\n")
	if len(content) > 0 && !strings.HasSuffix(content, "\n") {
		// The last line may be a partial write left by a crashed process.
		lines = lines[:len(lines)-1]
	}

	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}

		var event SessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return fmt.Errorf("decode session event: %w", err)
		}
		replay.Events = append(replay.Events, event)
		if event.Seq > replay.LastSeq {
			replay.LastSeq = event.Seq
		}
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func now() time.Time {
	return time.Now().UTC()
}
