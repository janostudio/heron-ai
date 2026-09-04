package storage

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

func TestJSONLSessionWriterAssignsMonotonicSequence(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()

	first, err := writer.Append(ctx, "flow-1", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated}})
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Seq)

	second, err := writer.Append(ctx, "flow-1", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowTurnStarted}})
	require.NoError(t, err)
	require.Equal(t, int64(2), second.Seq)

	replay, err := writer.Replay(ctx, "flow-1")
	require.NoError(t, err)
	require.Equal(t, int64(2), replay.LastSeq)
	require.Len(t, replay.Events, 2)
	require.Equal(t, types.EventFlowTurnStarted, replay.Events[1].Type)
}

func TestJSONLSessionWriterSupportsConcurrentAppends(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)

	const count = 30
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := writer.Append(context.Background(), "flow-1", LayerFlow, SessionEvent{
				EventHeader: types.EventHeader{Type: types.EventToolCallCompleted},
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	replay, err := writer.Replay(context.Background(), "flow-1")
	require.NoError(t, err)
	require.Len(t, replay.Events, count)
	require.Equal(t, int64(count), replay.LastSeq)
	for i, event := range replay.Events {
		require.Equal(t, int64(i+1), event.Seq)
	}
}

func TestJSONLSessionWriterIgnoresPartialFinalLine(t *testing.T) {
	root := t.TempDir()
	store := NewFileStore(root)
	path := filepath.Join(".agents", "data", "sessions", "flow-1", "flow.jsonl")
	require.NoError(t, store.Append(path, []byte(`{"seq":1,"event_id":"e1","type":"flow_session.created"}`+"\n")))
	require.NoError(t, store.Append(path, []byte(`{"seq":2,"event_id":"e2","type":"flow_turn.started"`)))

	replay, err := NewJSONLSessionWriter(store).Replay(context.Background(), "flow-1")
	require.NoError(t, err)
	require.Len(t, replay.Events, 1)
	require.Equal(t, int64(1), replay.LastSeq)
}

func TestJSONLSessionWriterRoutesByLayer(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx := context.Background()

	_, err := writer.Append(ctx, "flow-1", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: types.EventFlowTurnStarted}})
	require.NoError(t, err)
	_, err = writer.Append(ctx, "flow-1", LayerTeam, SessionEvent{EventHeader: types.EventHeader{Type: types.EventAgentTurnStarted}})
	require.NoError(t, err)
	_, err = writer.Append(ctx, "flow-1", LayerAgent, SessionEvent{EventHeader: types.EventHeader{Type: types.EventAgentModelResponse}})
	require.NoError(t, err)

	// Each layer lands in its own file with a globally monotonic seq.
	require.True(t, store.Exists(filepath.Join(".agents", "data", "sessions", "flow-1", "flow.jsonl")))
	require.True(t, store.Exists(filepath.Join(".agents", "data", "sessions", "flow-1", "team.jsonl")))
	require.True(t, store.Exists(filepath.Join(".agents", "data", "sessions", "flow-1", "agent.jsonl")))

	replay, err := writer.Replay(ctx, "flow-1")
	require.NoError(t, err)
	require.Len(t, replay.Events, 3)
	require.Equal(t, int64(3), replay.LastSeq)
	for i, event := range replay.Events {
		require.Equal(t, int64(i+1), event.Seq)
	}
}

func TestJSONLSessionWriterSubscribeReplaysAndPublishes(t *testing.T) {
	store := NewFileStore(t.TempDir())
	writer := NewJSONLSessionWriter(store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := writer.Append(context.Background(), "flow-1", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: "one"}})
	require.NoError(t, err)

	events, err := writer.Subscribe(ctx, "flow-1", 1)
	require.NoError(t, err)

	_, err = writer.Append(context.Background(), "flow-1", LayerFlow, SessionEvent{EventHeader: types.EventHeader{Type: "two"}})
	require.NoError(t, err)

	select {
	case event := <-events:
		require.Equal(t, int64(2), event.Seq)
		require.Equal(t, "two", event.Type)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live session event")
	}

	cancel()
	select {
	case _, ok := <-events:
		require.False(t, ok)
	case <-time.After(time.Second):
		t.Fatal("subscription did not close after context cancellation")
	}
}

func TestJSONLEvidenceStorePublishesAndReadsLatestRecord(t *testing.T) {
	store := NewFileStore(t.TempDir())
	evidence := NewJSONLEvidenceStore(store)
	ctx := context.Background()

	record := types.SharedRecord{
		RecordID: "diagnosis-1",
		Name:     "DiagnosisReport",
		Scope:    types.RecordScopeFlow,
		Summary:  "Root cause found",
		Data: map[string]any{
			"root_cause": "duplicate retry",
		},
	}
	require.NoError(t, evidence.Publish(ctx, "flow-1", record))

	record.Revision = 2
	record.Summary = "Root cause refined"
	record.Status = types.RecordSuperseded
	require.NoError(t, evidence.Publish(ctx, "flow-1", record))

	latest, err := evidence.Get(ctx, "flow-1", "diagnosis-1")
	require.NoError(t, err)
	require.Equal(t, "Root cause refined", latest.Summary)
	require.Equal(t, 2, latest.Revision)

	records, err := evidence.List(ctx, "flow-1", types.RecordScopeFlow)
	require.NoError(t, err)
	require.Len(t, records, 2)
}
