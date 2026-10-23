package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestResumeForMailboxClaimsBeforeRunningAndDeliversAfter pins the order of the
// three facts. Delivering before the turn used to consume the mail up front: a
// turn that failed to start, or a process that died before it ran, left the
// messages delivered while nothing had consumed them — the answer the user was
// waiting for vanished, with no dead-letter entry and no retry.
func TestResumeForMailboxClaimsBeforeRunningAndDeliversAfter(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)

	_, err := runtime.enqueueResultMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
		types.MailboxAddress{TeamID: "default"},
		map[string]any{"team_result": types.TeamTurnResult{Reply: "verification passed", Turn: types.TeamTurn{TeamID: "owner"}}},
	)
	require.NoError(t, err)

	_, err = runtime.ResumeForMailbox(ctx, session.ID)
	require.NoError(t, err)

	replay, err := sessionWriter.Replay(ctx, session.ID)
	require.NoError(t, err)

	claimedAt, startedAt, deliveredAt := -1, -1, -1
	for index, event := range replay.Events {
		switch event.Type {
		case types.EventMailboxMessageClaimed:
			if claimedAt < 0 {
				claimedAt = index
			}
		case types.EventFlowTurnStarted:
			if startedAt < 0 {
				startedAt = index
			}
		case types.EventMailboxMessageDelivered:
			deliveredAt = index
		}
	}
	require.GreaterOrEqual(t, claimedAt, 0, "the mail must be claimed, not silently delivered")
	require.GreaterOrEqual(t, startedAt, 0, "the wake-up must have started a turn")
	require.GreaterOrEqual(t, deliveredAt, 0, "a consumed message must be delivered")
	require.Less(t, claimedAt, startedAt, "claim must happen before the turn that consumes the mail runs")
	require.Greater(t, deliveredAt, startedAt, "delivery is the outcome of the turn, not a precondition")
}
