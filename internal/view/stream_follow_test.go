package view

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newStreamFixture builds a session whose only turn has already completed: the
// shape a per-turn stream closes on, and the shape a lingering wake-up stream
// must survive.
func newStreamFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	files := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(files)
	const sessionID = "fs_stream_follow"
	ctx := context.Background()
	for _, eventType := range []string{types.EventFlowTurnStarted, types.EventFlowTurnCompleted} {
		_, err := writer.Append(ctx, sessionID, storage.LayerFlow, storage.SessionEvent{
			EventHeader: types.EventHeader{Type: eventType, FlowSessionID: sessionID},
			Payload:     map[string]any{},
		})
		require.NoError(t, err)
	}
	return &Handler{sessions: writer}, sessionID
}

// Without follow the old contract holds: one turn's events, then close.
func TestStreamClosesAfterTurnWithoutFollow(t *testing.T) {
	handler, sessionID := newStreamFixture(t)
	request := httptest.NewRequest("GET", "/api/sessions/stream?session_id="+sessionID, nil)
	recorder := httptest.NewRecorder()

	handler.HandleStream(recorder, request)

	body := recorder.Body.String()
	require.Equal(t, 2, strings.Count(body, "data:"), "both events of the turn must be delivered")
}

// With follow the stream must outlive the turn: a scheduled wake-up has no
// request in flight, so closing here is what would lose the message the whole
// feature exists to deliver.
func TestStreamFollowSurvivesCompletedTurn(t *testing.T) {
	handler, sessionID := newStreamFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "/api/sessions/stream?session_id="+sessionID+"&follow=1", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		handler.HandleStream(recorder, request)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("follow=1 must not close the stream when a turn completes")
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not stop after the client went away")
	}
	require.Contains(t, recorder.Body.String(), "data:")
}

// A process with no runtime is up but not ready: a client that probes first
// must not start sending work.
func TestHealthReportsNotReadyWithoutRuntime(t *testing.T) {
	handler := &Handler{}
	recorder := httptest.NewRecorder()

	handler.HandleHealth(recorder, httptest.NewRequest("GET", "/api/health", nil))

	require.Equal(t, 200, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"ready":false`)
	require.Contains(t, recorder.Body.String(), `"pending_wakeups":0`)
}
