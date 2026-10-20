package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// memorySessionStore is a minimal SessionWriter used to drive the progress
// pump without touching the filesystem.
type memorySessionStore struct {
	mu     sync.Mutex
	events []storage.SessionEvent
	next   int64
	subs   map[int]chan storage.SessionEvent
	nextID int
}

func newMemorySessionStore() *memorySessionStore {
	return &memorySessionStore{subs: make(map[int]chan storage.SessionEvent)}
}

func (s *memorySessionStore) Append(_ context.Context, sessionID string, _ storage.EventLayer, event storage.SessionEvent) (storage.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	event.Seq = s.next
	event.FlowSessionID = sessionID
	if event.EventID == "" {
		event.EventID = fmt.Sprintf("%s-%d", sessionID, event.Seq)
	}
	s.events = append(s.events, event)
	for id, channel := range s.subs {
		select {
		case channel <- event:
		default:
			delete(s.subs, id)
			close(channel)
		}
	}
	return event, nil
}

func (s *memorySessionStore) Replay(_ context.Context, sessionID string) (*storage.SessionReplay, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return nil, storage.ErrNotFound
	}
	replay := &storage.SessionReplay{SessionID: sessionID}
	for _, event := range s.events {
		if event.FlowSessionID != sessionID {
			continue
		}
		replay.Events = append(replay.Events, event)
	}
	if len(replay.Events) == 0 {
		return nil, storage.ErrNotFound
	}
	replay.LastSeq = replay.Events[len(replay.Events)-1].Seq
	return replay, nil
}

func (s *memorySessionStore) Subscribe(ctx context.Context, sessionID string, afterSeq int64) (<-chan storage.SessionEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	channel := make(chan storage.SessionEvent, 64)
	for _, event := range s.events {
		if event.FlowSessionID == sessionID && event.Seq > afterSeq {
			channel <- event
		}
	}
	s.nextID++
	id := s.nextID
	s.subs[id] = channel
	go func() {
		<-ctx.Done()
		s.mu.Lock()
		defer s.mu.Unlock()
		if existing, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(existing)
		}
	}()
	return channel, nil
}

func eventFor(seq int64, eventType string, payload map[string]any) storage.SessionEvent {
	return storage.SessionEvent{
		EventHeader: types.EventHeader{
			Seq:           seq,
			EventID:       fmt.Sprintf("fs-1-%d", seq),
			Type:          eventType,
			FlowSessionID: "fs-1",
			FlowTurnID:    "ft-1",
			TeamID:        "team-a",
			CallID:        "reviewer",
			CallTurnID:    "ct-1",
			CallType:      types.CallAgent,
			Attempt:       1,
		},
		Payload: payload,
	}
}

func TestProjectToolEventsShareOneToolUseID(t *testing.T) {
	started := projectSessionEvent(eventFor(1, types.EventToolCallStarted, map[string]any{
		"round":        0,
		"tool_name":    "Bash",
		"tool_call_id": "tc-abc",
		"arguments":    map[string]any{"command": "ls"},
	}))
	completed := projectSessionEvent(eventFor(2, types.EventToolCallCompleted, map[string]any{
		"round":        0,
		"tool_name":    "Bash",
		"tool_call_id": "tc-abc",
		"content":      "main.go",
		"exit_code":    0,
	}))
	failed := projectSessionEvent(eventFor(3, types.EventToolCallFailed, map[string]any{
		"round":        0,
		"tool_name":    "Bash",
		"tool_call_id": "tc-abc",
		"error":        "boom",
	}))

	require.Equal(t, progressTypeToolUse, started["type"])
	require.Equal(t, "ct-1/1/tc-abc", started["tool_use_id"])
	require.Equal(t, map[string]any{"command": "ls"}, started["input"])

	require.Equal(t, progressTypeToolResult, completed["type"])
	require.Equal(t, started["tool_use_id"], completed["tool_use_id"])
	require.Equal(t, false, completed["is_error"])
	require.Equal(t, "completed", completed["status"])
	require.Equal(t, 0, completed["exit_code"])

	require.Equal(t, progressTypeToolResult, failed["type"])
	require.Equal(t, started["tool_use_id"], failed["tool_use_id"])
	require.Equal(t, true, failed["is_error"])
	require.Equal(t, "failed", failed["status"])
	require.Equal(t, false, completed["is_error"])
}

func TestProjectAssistantCarriesTextBlocksAndUsage(t *testing.T) {
	params := projectSessionEvent(eventFor(4, types.EventAgentModelResponse, map[string]any{
		"round":         1,
		"text":          "checking the tree",
		"finish_reason": "tool_calls",
		"model":         "test-model",
		"tool_calls": []types.ToolCall{
			{ID: "tc-1", Name: "Bash", Arguments: map[string]any{"command": "ls"}},
		},
		"usage": types.TokenUsage{PromptTokens: 3, CompletionTokens: 4, TotalTokens: 7},
	}))

	require.Equal(t, progressTypeAssistant, params["type"])
	blocks, ok := params["content"].([]any)
	require.True(t, ok)
	require.Len(t, blocks, 2)
	require.Equal(t, "text", blocks[0].(map[string]any)["type"])
	require.Equal(t, "checking the tree", blocks[0].(map[string]any)["text"])
	require.Equal(t, "tool_use", blocks[1].(map[string]any)["type"])
	require.Equal(t, "tc-1", blocks[1].(map[string]any)["id"])
	require.Equal(t, 1, params["round"])
	require.Equal(t, "test-model", params["model"])
}

func TestProjectAssistantSkipsEmptyRounds(t *testing.T) {
	params := projectSessionEvent(eventFor(5, types.EventAgentModelResponse, map[string]any{
		"round": 0,
		"text":  "",
	}))
	require.Nil(t, params, "a round with no text and no tool calls must not be emitted")
}

func TestProjectDropsInternalEvents(t *testing.T) {
	for _, eventType := range []string{
		types.EventFlowSessionCreated,
		types.EventTeamSessionCreated,
		types.EventAgentSessionCreated,
		types.EventContextCompacted,
		types.EventMailboxMessageEnqueued,
	} {
		require.Nil(t, projectSessionEvent(eventFor(1, eventType, nil)), eventType)
	}
}

func TestProjectTruncatesOversizedArguments(t *testing.T) {
	large := map[string]any{"body": strings.Repeat("x", progressMaxInputBytes*2)}
	params := projectSessionEvent(eventFor(6, types.EventToolCallStarted, map[string]any{
		"tool_name":    "Write",
		"tool_call_id": "tc-big",
		"arguments":    large,
	}))
	input, ok := params["input"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, input["_truncated"])
	preview, ok := input["preview"].(string)
	require.True(t, ok)
	require.LessOrEqual(t, len(preview), progressMaxInputBytes)
}

func TestEnforceNotificationSizeMarksTruncated(t *testing.T) {
	params := map[string]any{
		"type":    progressTypeToolResult,
		"content": strings.Repeat("y", progressMaxNotificationSize),
		"usage":   types.TokenUsage{TotalTokens: 1},
	}
	reduced := enforceNotificationSize(params)
	require.Equal(t, true, reduced["truncated"])
	require.LessOrEqual(t, encodedSize(reduced), progressMaxNotificationSize)
	_, hasUsage := reduced["usage"]
	require.False(t, hasUsage, "optional fields are dropped before text is shortened")
}

func TestPumpForwardsOnlyCurrentTurnEventsInSeqOrder(t *testing.T) {
	store := newMemorySessionStore()
	store.mu.Lock()
	store.events = []storage.SessionEvent{
		eventFor(1, types.EventToolCallStarted, map[string]any{"tool_name": "Old", "tool_call_id": "a"}),
	}
	store.next = 1
	store.events[0].FlowTurnID = "ft-0"
	store.mu.Unlock()

	var mu sync.Mutex
	var seen []map[string]any
	pump := startEventPump(context.Background(), store, "fs-1", "", 1, func(params map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, params)
		return nil
	})
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent,
		eventFor(0, types.EventToolCallStarted, map[string]any{"tool_name": "Bash", "tool_call_id": "b"}))
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent,
		eventFor(0, types.EventToolCallCompleted, map[string]any{"tool_name": "Bash", "tool_call_id": "b", "content": "ok"}))
	pump.Stop()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 2)
	require.Equal(t, progressTypeToolUse, seen[0]["type"])
	require.Equal(t, progressTypeToolResult, seen[1]["type"])
	require.Less(t, seen[0]["seq"].(int64), seen[1]["seq"].(int64))
}

func TestPumpFiltersEventsFromOtherTurns(t *testing.T) {
	store := newMemorySessionStore()
	var mu sync.Mutex
	var seen []map[string]any
	pump := startEventPump(context.Background(), store, "fs-1", "ft-1", 0, func(params map[string]any) error {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, params)
		return nil
	})
	other := eventFor(0, types.EventToolCallStarted, map[string]any{"tool_name": "Other", "tool_call_id": "z"})
	other.FlowTurnID = "ft-2"
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent, other)
	mine := eventFor(0, types.EventToolCallStarted, map[string]any{"tool_name": "Mine", "tool_call_id": "y"})
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent, mine)
	pump.Stop()

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, seen, 1)
	require.Equal(t, "Mine", seen[0]["name"])
}

func TestSnapshotReplayBackfillsFirstTurn(t *testing.T) {
	store := newMemorySessionStore()
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerFlow, eventFor(0, types.EventFlowTurnStarted, nil))
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerTeam, eventFor(0, types.EventAgentTurnStarted, map[string]any{
		"call": types.Call{ID: "reviewer", Type: types.CallAgent, AgentID: "reviewer-agent"},
	}))
	_, _ = store.Append(context.Background(), "fs-1", storage.LayerAgent, eventFor(0, types.EventToolCallCompleted, map[string]any{
		"tool_name": "Bash", "tool_call_id": "tc-1", "content": "ok",
	}))

	var mu sync.Mutex
	var typesSeen []string
	pump := &eventPump{
		sessions:   store,
		flowTurnID: "ft-1",
		emit: func(params map[string]any) error {
			mu.Lock()
			defer mu.Unlock()
			typesSeen = append(typesSeen, params["type"].(string))
			return nil
		},
		done: make(chan struct{}),
	}
	pump.replayEventsSnapshot(context.Background(), "fs-1")

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{progressTypeFlowTurnStarted, progressTypeCallStarted, progressTypeToolResult}, typesSeen)
}

// chunkWriter fails the test if two goroutines ever write at the same time:
// json.Encoder writes in several steps, so an unsynchronised writer corrupts
// lines instead of merely reordering them.
type chunkWriter struct {
	mu      sync.Mutex
	writing bool
	buffer  bytes.Buffer
}

func (w *chunkWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.writing {
		w.mu.Unlock()
		return 0, fmt.Errorf("concurrent write")
	}
	w.writing = true
	w.mu.Unlock()

	time.Sleep(time.Microsecond)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writing = false
	return w.buffer.Write(data)
}

func TestServerWriterSerialisesNotificationsAndResponses(t *testing.T) {
	writer := &chunkWriter{}
	server := newJSONRPCServer(&jsonRPCFlowRuntimeStub{}, "test-flow", writer)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(index int) {
			defer wg.Done()
			require.NoError(t, server.writeProgress(map[string]any{"type": "status", "seq": index}))
		}(i)
		go func(index int) {
			defer wg.Done()
			require.NoError(t, server.write(jsonRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(fmt.Sprintf("%d", index)), Result: map[string]any{"ok": true}}))
		}(i)
	}
	wg.Wait()

	lines := strings.Split(strings.TrimSpace(writer.buffer.String()), "\n")
	require.Len(t, lines, 100)
	for _, line := range lines {
		var decoded map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &decoded), "corrupted line: %q", line)
	}
}
