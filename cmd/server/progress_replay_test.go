package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// countingSessionWriter counts full replays. follow_async used to do one per
// poll: at the default 500ms cadence and 15-minute cap that is ~1800 parses of
// the whole session file per turn, each holding the writer's global lock and
// blocking every event append in the process.
type countingSessionWriter struct {
	storage.SessionWriter
	replays atomic.Int64
}

func (c *countingSessionWriter) Replay(ctx context.Context, sessionID string) (*storage.SessionReplay, error) {
	c.replays.Add(1)
	return c.SessionWriter.Replay(ctx, sessionID)
}

func TestFollowAsyncDoesNotReplayOnEveryPoll(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	counted := &countingSessionWriter{SessionWriter: writer}

	sessionID := "fs_follow_no_replay"
	_, err := writer.Append(context.Background(), sessionID, storage.LayerFlow, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: sessionID},
		Payload:     map[string]any{"session": types.FlowSession{ID: sessionID, FlowID: "f", Status: types.SessionWaitingInput}},
	})
	require.NoError(t, err)

	previousPoll, previousHold := followAsyncPoll, followAsyncHold
	followAsyncPoll, followAsyncHold = 2*time.Millisecond, 6*time.Millisecond
	t.Cleanup(func() { followAsyncPoll, followAsyncHold = previousPoll, previousHold })

	pump := &eventPump{sessions: counted, done: make(chan struct{})}
	started := time.Now()
	pump.waitForIdle(context.Background(), sessionID, time.Second)
	require.GreaterOrEqual(t, time.Since(started), followAsyncHold,
		"the follower must still observe its quiet window")

	require.LessOrEqual(t, counted.replays.Load(), int64(1),
		"the idle check must fold the log once, not once per poll")
}

// TestBackgroundWorkTracksIncrementally keeps the incremental state honest: it
// has to answer exactly like folding the whole log after every event.
func TestBackgroundWorkTracksIncrementally(t *testing.T) {
	events := []storage.SessionEvent{
		{EventHeader: types.EventHeader{Type: types.EventMailboxMessageEnqueued}, Payload: map[string]any{"message": types.MailboxMessage{MessageID: "mb_1"}}},
		{EventHeader: types.EventHeader{Type: types.EventMailboxMessageEnqueued}, Payload: map[string]any{"message": types.MailboxMessage{MessageID: "mb_2"}}},
		{EventHeader: types.EventHeader{Type: types.EventMailboxMessageDelivered}, Payload: map[string]any{"message": types.MailboxMessage{MessageID: "mb_1"}}},
	}

	state := newBackgroundWork()
	for index, event := range events {
		state.apply(event)
		require.Equal(t, backgroundWorkFromEvents(events[:index+1]).pending(), state.pending(),
			"incremental state diverged after event %d", index)
	}
	require.True(t, state.pending(), "mb_2 is still open")

	state.apply(storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventMailboxMessageDead},
		Payload:     map[string]any{"message": types.MailboxMessage{MessageID: "mb_2"}},
	})
	require.False(t, state.pending())
}
