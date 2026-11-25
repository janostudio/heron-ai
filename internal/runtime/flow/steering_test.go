package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestSubmitInputSteersRunningTurn is the case the feature exists for: a message
// that arrives while this process is running a turn is handed to that turn
// instead of waiting behind it.
func TestSubmitInputSteersRunningTurn(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-steer", types.SessionRunning)

	_, clear := rt.markTurnRunning(context.Background(), session.ID)
	defer clear()

	result, err := rt.SubmitInput(context.Background(), session.ID, "换个方向：先看 b.go", nil)
	require.NoError(t, err)
	require.True(t, result.Steered, "a running turn should take the message")
	require.Contains(t, result.Reply, "已插入当前轮次")

	source := rt.steeringSource(context.Background(), session, "ft-steer")
	require.NotNil(t, source)
	inputs := source()
	require.Len(t, inputs, 1, "the running turn reads exactly the queued message")
	require.Equal(t, "换个方向：先看 b.go", contextBlockTextOf(inputs[0].Blocks),
		"a text-only message still has to carry its text into the turn")
	require.Empty(t, source(), "the inbox is drained once, not replayed every round")

	delivered := false
	box, loadErr := rt.loadMailbox(context.Background(), session.ID)
	require.NoError(t, loadErr)
	for _, msg := range box {
		if msg.Kind != mailboxKindUser {
			continue
		}
		require.Equal(t, types.MailboxDelivered, msg.Status,
			"the stored copy is closed out, so the end-of-turn drain cannot run it twice")
		delivered = true
	}
	require.True(t, delivered)
}

// TestSubmitInputFallsBackToQueueingWithoutInbox covers the race: the turn ended
// between the busy check and the hand-off. The message must stay queued rather
// than be reported as steered and then never read.
func TestSubmitInputFallsBackToQueueingWithoutInbox(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-noinbox", types.SessionIdle)

	result, err := rt.SubmitInput(context.Background(), session.ID, "插话", nil)
	require.NoError(t, err)
	require.False(t, result.Steered, "no live turn, nothing to steer into")
	require.True(t, result.Queued, "it is still accepted and stored")
}

// TestCancelClearsTheSteeringInbox: after Cancel the turn is over, so a message
// must not be handed to an inbox nobody reads.
func TestCancelClearsTheSteeringInbox(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	rt := newTestRuntime(testDefinitions(), &inputRecordingTeamRuntime{}, writer, nil)
	session := seedMailboxSession(t, rt, "fs-cancel", types.SessionRunning)

	_, clear := rt.markTurnRunning(context.Background(), session.ID)
	require.NotNil(t, rt.steeringInbox(session.ID))
	clear()
	require.Nil(t, rt.steeringInbox(session.ID),
		"the inbox lives exactly as long as the turn that publishes it")
}
