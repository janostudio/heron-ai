package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
)

// A dispatched Team outlives the turn that started it: it runs on its own
// context, which is what lets it keep working after the FlowTurn returns — and
// what used to mean nothing could stop it. These cover the session-scoped
// handle Cancel uses for that half.
func TestCancelStopsRegisteredSessionWork(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), storage.NewJSONLEvidenceStore(fileStore))

	ctx, release := runtime.registerSessionWork("fs_cancel_work")
	defer release()
	require.NoError(t, ctx.Err())

	require.Equal(t, 1, runtime.cancelSessionWork("fs_cancel_work"))
	require.ErrorIs(t, ctx.Err(), context.Canceled)

	// A second cancel has nothing left to stop; it must not panic or report work.
	require.Equal(t, 0, runtime.cancelSessionWork("fs_cancel_work"))
}

// A session can have several Teams running at once, so cancelling must reach
// every one of them, not just the first.
func TestCancelStopsEveryExecutionOfASession(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), storage.NewJSONLEvidenceStore(fileStore))

	first, releaseFirst := runtime.registerSessionWork("fs_cancel_many")
	defer releaseFirst()
	second, releaseSecond := runtime.registerSessionWork("fs_cancel_many")
	defer releaseSecond()

	require.Equal(t, 2, runtime.cancelSessionWork("fs_cancel_many"))
	require.ErrorIs(t, first.Err(), context.Canceled)
	require.ErrorIs(t, second.Err(), context.Canceled)
}

// Cancelling one conversation must never stop another's work.
func TestCancelLeavesOtherSessionsRunning(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), storage.NewJSONLEvidenceStore(fileStore))

	stopped, releaseStopped := runtime.registerSessionWork("fs_cancel_a")
	defer releaseStopped()
	kept, releaseKept := runtime.registerSessionWork("fs_cancel_b")
	defer releaseKept()

	require.Equal(t, 1, runtime.cancelSessionWork("fs_cancel_a"))
	require.ErrorIs(t, stopped.Err(), context.Canceled)
	require.NoError(t, kept.Err())
}

// Releasing runs when the work ends on its own. It cancels the context (nobody
// is left to use it) and drops the registration, so a finished Team does not
// leave a cancel func behind for the rest of the process.
func TestReleasedWorkLeavesNothingBehind(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), storage.NewJSONLEvidenceStore(fileStore))

	ctx, release := runtime.registerSessionWork("fs_cancel_released")
	release()

	require.ErrorIs(t, ctx.Err(), context.Canceled)
	require.Equal(t, 0, runtime.cancelSessionWork("fs_cancel_released"),
		"a finished execution must not leave a cancel func behind")
}
