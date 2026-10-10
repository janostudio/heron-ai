package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestMailboxDispatchOnCoordinatorActivate verifies the P0 mailbox dispatch
// hook: when async dispatch is enabled and the coordinator activates a team,
// the runtime records a work message to that team's mailbox and hands the team
// to the background dispatcher, ending the coordinator's turn without waiting.
//
// It reuses the exact flow of TestRuntimeStartsActivatesAndEndsResumable,
// where the coordinator ("default") activates "verify", and then waits for the
// background dispatch to land its result.
func TestMailboxDispatchOnCoordinatorActivate(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, evidenceStore)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)

	// The coordinator dispatched and ended without waiting for the activated
	// team, so only the coordinator's own turn is in this result.
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.Equal(t, types.TurnWaitingInput, result.Turn.Status)
	require.Len(t, result.TeamResults, 1)

	// Wait for the background "verify" dispatch to post its result.
	require.Eventually(t, func() bool {
		box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
		if err != nil {
			return false
		}
		for _, msg := range box {
			if msg.Kind == mailboxKindResult && msg.To.TeamID == "default" {
				return true
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond)

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

	// loadMailbox folds those events into a delivered work message addressed
	// to the activated team, caused by this turn.
	box, err := runtime.loadMailbox(context.Background(), result.Session.ID)
	require.NoError(t, err)

	var work *types.MailboxMessage
	for i := range box {
		msg := box[i]
		if msg.Kind == mailboxKindWork && msg.To.TeamID == "verify" {
			work = &msg
			break
		}
	}
	require.NotNil(t, work, "expected a work message for the activated team")
	require.Equal(t, types.MailboxDelivered, work.Status)
	require.Equal(t, mailboxKindWork, work.Kind)
	require.Equal(t, "verify", work.To.TeamID)
	require.Equal(t, result.Turn.ID, work.CauseFlowTurnID)
	require.Equal(t, "default", work.Payload["caller_team"])
}
