package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestMailboxShadowOnCoordinatorActivate verifies the shadow-mode mailbox hook:
// when the coordinator activates a team, the runtime additionally records a
// work message to that team's mailbox, while the existing synchronous enqueue
// path (and therefore the turn's outcome) is unchanged.
//
// It reuses the exact flow of TestRuntimeStartsActivatesAndEndsResumable,
// where the coordinator ("default") activates "verify".
func TestMailboxShadowOnCoordinatorActivate(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, evidenceStore)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)

	// Execution is unchanged: the same aggregate outcome as before the hook.
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Equal(t, types.TurnWaitingInput, result.Turn.Status)
	require.Contains(t, result.Reply, "verification passed")
	require.NotEmpty(t, result.Reply)
	require.Len(t, result.TeamResults, 3)

	// A mailbox.message.enqueued event was written to flow.jsonl.
	replay, err := sessionWriter.Replay(context.Background(), result.Session.ID)
	require.NoError(t, err)
	enqueued := 0
	for _, event := range replay.Events {
		if event.Type == types.EventMailboxMessageEnqueued {
			enqueued++
		}
	}
	require.GreaterOrEqual(t, enqueued, 1)

	// loadMailbox folds that event into a pending work message addressed to
	// the activated team, caused by this turn.
	box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
	require.NoError(t, err)

	var shadow *types.MailboxMessage
	for i := range box {
		msg := box[i]
		if msg.Kind == mailboxKindWork && msg.To.TeamID == "verify" {
			shadow = &msg
			break
		}
	}
	require.NotNil(t, shadow, "expected a work message for the activated team")
	require.Equal(t, types.MailboxPending, shadow.Status)
	require.Equal(t, mailboxKindWork, shadow.Kind)
	require.Equal(t, "verify", shadow.To.TeamID)
	require.Equal(t, result.Turn.ID, shadow.CauseFlowTurnID)
	require.Equal(t, "default", shadow.Payload["caller_team"])
}
