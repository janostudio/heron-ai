package view

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// syncRecorder is a ResponseWriter safe to read while a handler is still
// writing. httptest.Recorder is not, and a heartbeat can only be observed
// mid-stream, so the assertion needs a writer the test may inspect live.
type syncRecorder struct {
	mu  sync.Mutex
	buf bytes.Buffer
	hdr http.Header
}

func newSyncRecorder() *syncRecorder { return &syncRecorder{hdr: http.Header{}} }

func (r *syncRecorder) Header() http.Header { return r.hdr }

func (r *syncRecorder) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.Write(data)
}

func (r *syncRecorder) WriteHeader(int) {}

func (r *syncRecorder) Flush() {}

func (r *syncRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf.String()
}

// A followed stream can idle for minutes between turns. Without a heartbeat an
// intermediary that treats an idle connection as dead closes it, and the
// wake-up this feature exists to deliver is lost.
func TestStreamFollowEmitsHeartbeat(t *testing.T) {
	handler, sessionID := newStreamFixture(t)
	handler.streamHeartbeat = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest("GET", "/api/sessions/stream?session_id="+sessionID+"&follow=1", nil).WithContext(ctx)
	recorder := newSyncRecorder()

	done := make(chan struct{})
	go func() {
		handler.HandleStream(recorder, request)
		close(done)
	}()

	deadline := time.After(3 * time.Second)
	for !strings.Contains(recorder.String(), ": ping") {
		select {
		case <-deadline:
			t.Fatal("no heartbeat frame on a followed stream")
		case <-time.After(5 * time.Millisecond):
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the stream did not stop after the client went away")
	}
}

// The heartbeat belongs to the followed stream only: a per-turn stream ends at
// the turn boundary, so it carries no keep-alive frame. Its opening retry
// directive comes from NewSSEWriter and is shared by every stream — it is a
// connection parameter, not an event, and compliant clients skip it.
func TestStreamWithoutFollowHasNoHeartbeat(t *testing.T) {
	handler, sessionID := newStreamFixture(t)
	handler.streamHeartbeat = time.Millisecond
	request := httptest.NewRequest("GET", "/api/sessions/stream?session_id="+sessionID, nil)
	recorder := httptest.NewRecorder()

	handler.HandleStream(recorder, request)

	require.NotContains(t, recorder.Body.String(), ": ping")
}
