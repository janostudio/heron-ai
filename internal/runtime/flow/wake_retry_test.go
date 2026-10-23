package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func appendSessionStatus(t *testing.T, writer storage.SessionWriter, session types.FlowSession) {
	t.Helper()
	_, err := writer.Append(context.Background(), session.ID, storage.LayerFlow, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventFlowSessionUpdated,
			FlowSessionID: session.ID,
		},
		Payload: map[string]any{"session": session},
	})
	require.NoError(t, err)
}

// TestWakeRetriesWhileSessionStillRunning is the regression guard for the
// single-shot wake-up: a dispatched Team can finish while the dispatching turn
// is still writing its own status, so the wake-up sees "running" and used to
// give up. The result message then sat in the inbox until the next user
// message — for an unattended session, never.
func TestWakeRetriesWhileSessionStillRunning(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, storage.NewJSONLEvidenceStore(fileStore))

	session := types.FlowSession{
		ID:        "fs_wake_retry",
		FlowID:    "code-fix",
		Status:    types.SessionRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	appendSessionStatus(t, writer, session)

	woke := make(chan string, 4)
	runtime.SetWaker(func(sessionID string) { woke <- sessionID })

	previous := wakeRetryInterval
	wakeRetryInterval = 10 * time.Millisecond
	t.Cleanup(func() { wakeRetryInterval = previous })

	runtime.wakeIfIdle(context.Background(), session.ID)
	select {
	case id := <-woke:
		t.Fatalf("a running session must not be woken immediately, got %q", id)
	case <-time.After(50 * time.Millisecond):
	}

	// The dispatching turn finishes its write-out: the session is idle now, and
	// the pending retry must deliver the wake-up instead of having dropped it.
	session.Status = types.SessionWaitingInput
	appendSessionStatus(t, writer, session)

	select {
	case id := <-woke:
		require.Equal(t, session.ID, id)
	case <-time.After(3 * time.Second):
		t.Fatal("the wake-up was dropped while the session was running")
	}
}

// TestWakeRetryStopsAtAttemptLimit keeps the retry bounded: a session that never
// becomes idle must not leave a timer running forever.
func TestWakeRetryStopsAtAttemptLimit(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer, storage.NewJSONLEvidenceStore(fileStore))

	session := types.FlowSession{
		ID:        "fs_wake_bounded",
		FlowID:    "code-fix",
		Status:    types.SessionRunning,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	appendSessionStatus(t, writer, session)
	woke := make(chan string, 4)
	runtime.SetWaker(func(sessionID string) { woke <- sessionID })

	previousInterval, previousAttempts := wakeRetryInterval, wakeRetryAttempts
	wakeRetryInterval, wakeRetryAttempts = 5*time.Millisecond, 3
	t.Cleanup(func() {
		wakeRetryInterval, wakeRetryAttempts = previousInterval, previousAttempts
	})

	runtime.wakeIfIdle(context.Background(), session.ID)
	require.Never(t, func() bool {
		select {
		case <-woke:
			return true
		default:
			return false
		}
	}, 200*time.Millisecond, 10*time.Millisecond, "a session that never idles must not be woken")

	// The retry chain is gone: a fresh call is not blocked by a stale marker.
	require.Eventually(t, func() bool {
		runtime.dispatchMu.Lock()
		defer runtime.dispatchMu.Unlock()
		return len(runtime.pendingWakes) == 0
	}, time.Second, 5*time.Millisecond, "pending wake markers must be released when a retry returns")
}
