package flow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// failingAppendWriter fails every Append, standing in for the case that used to
// turn one failed wake-up into a spin: the reschedule cannot be persisted, so
// the in-memory record stays at a FireAt that has already passed.
type failingAppendWriter struct {
	storage.SessionWriter
}

func (failingAppendWriter) Append(context.Context, string, storage.EventLayer, storage.SessionEvent) (storage.SessionEvent, error) {
	return storage.SessionEvent{}, errors.New("append failed")
}

func newPastWakeupRuntime(t *testing.T, writer storage.SessionWriter) (*Runtime, types.WakeupRecord) {
	t.Helper()
	fileStore := storage.NewFileStore(t.TempDir())
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, writer,
		storage.NewJSONLEvidenceStore(fileStore))
	record := types.WakeupRecord{
		ID:            "wk_due",
		FlowSessionID: "fs_wakeup_hold",
		TeamID:        "team-a",
		FireAt:        time.Now().UTC().Add(-time.Minute),
		Note:          "check the deploy",
		Status:        types.WakeupScheduled,
	}
	runtime.wakeupMu.Lock()
	runtime.wakeupIndex = map[string]*wakeupIndex{
		record.FlowSessionID: {records: []types.WakeupRecord{record}, loaded: true},
	}
	runtime.wakeupMu.Unlock()
	return runtime, record
}

// A wake-up that fails for a reason other than deferral or a terminal session
// must still leave the past. The scheduler takes the earliest scheduled FireAt,
// so leaving it there fires the same record again on the next iteration — the
// failure mode this guards is a loop at full CPU, not a missed reminder.
func TestFailedWakeupIsRescheduledOutOfThePast(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	writer := storage.NewJSONLSessionWriter(fileStore)
	runtime, record := newPastWakeupRuntime(t, writer)

	runtime.rescheduleWakeup(context.Background(), record, "wake-up failed: boom", "wake-up failed")

	runtime.wakeupMu.Lock()
	updated := runtime.wakeupIndex[record.FlowSessionID].records[0]
	runtime.wakeupMu.Unlock()

	require.True(t, updated.FireAt.After(time.Now()),
		"a rescheduled wake-up must be in the future, got %s", updated.FireAt)

	deadline, ok := runtime.nextWakeupDeadline()
	require.True(t, ok)
	require.True(t, deadline.After(time.Now()),
		"the scheduler must not see a past deadline after a reschedule")
}

// When even the reschedule cannot be written, the record is still pinned in
// memory. Without the hold the deadline stays in the past and the loop spins.
func TestUnwritableRescheduleHoldsTheRecord(t *testing.T) {
	failing := failingAppendWriter{SessionWriter: storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))}
	runtime, record := newPastWakeupRuntime(t, failing)

	runtime.rescheduleWakeup(context.Background(), record, "wake-up failed: boom", "wake-up failed")

	// The persisted record cannot have moved; the in-memory hold must stand in.
	require.True(t, runtime.wakeupHeld(record.ID, time.Now()),
		"an unpersisted reschedule must hold the record")

	deadline, ok := runtime.nextWakeupDeadline()
	require.True(t, ok)
	require.True(t, deadline.After(time.Now()),
		"a held record must report its hold expiry, not its past FireAt")

	// And the loop must not start it again while held.
	runtime.fireDueWakeups(context.Background())
	require.False(t, runtime.wakeupInflight[record.ID], "a held record must not be fired")
}

// The hold is released by the first reschedule that does reach the stream, so a
// transient write failure does not delay the reminder by a full interval.
func TestSuccessfulRescheduleClearsTheHold(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	runtime, record := newPastWakeupRuntime(t, storage.NewJSONLSessionWriter(fileStore))

	runtime.holdWakeup(record.ID, time.Hour)
	runtime.rescheduleWakeup(context.Background(), record, "wake-up failed: boom", "wake-up failed")

	require.False(t, runtime.wakeupHeld(record.ID, time.Now()))
}
