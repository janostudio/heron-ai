package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// A long conversation must never run out of budget. The removed session-wide
// check marked a new message dead once the session had started max_team_turns
// rounds in total — which in practice capped how many turns a *user* could
// have in one conversation. Only the per-FlowTurn limit bounds a turn now.
func TestRunMessageTurnIsNotBlockedBySessionTeamTurnTotal(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)
	rt.SetLimits(types.RuntimeLimits{MaxTeamTurns: 1})

	// Two rounds already spent — one more than the per-turn limit, which is
	// exactly the state that used to mark the next message dead.
	for _, team := range []string{"owner", "worktree"} {
		require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventTeamTurnStarted,
				FlowSessionID: session.ID,
				TeamID:        team,
			},
		}))
	}

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("worktree", ""), map[string]any{"caller_team": "owner"})
	require.NoError(t, err)

	rt.runMessageTurn(ctx, session, msg)

	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)

	// The message may end up dead for an unrelated reason (this fixture's
	// Team cannot execute the dispatched work). What must not happen is the
	// budget killing it: that is the check this test exists to keep deleted.
	if box[msg.MessageID].Status == types.MailboxDead {
		reason, _ := box[msg.MessageID].Payload["dead_reason"].(string)
		require.NotContains(t, reason, "max team turns",
			"a session total must not cap how many turns a conversation may have")
	}
}

// TestActivationBudgetStopsCrossTurnLoop pins the cross-turn bound: a Team that
// keeps re-activating the same target is stopped by max_activation_retries even
// though each activation happens in its own turn. Without a session-wide count
// the repetition would run until the much larger team-turn budget.
func TestActivationBudgetStopsCrossTurnLoop(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)
	rt.SetLimits(types.RuntimeLimits{MaxActivationRetries: 2})

	// Two prior dispatches from owner to worktree, each in its own turn.
	for i := 0; i < 2; i++ {
		_, err := rt.enqueueWorkMessage(ctx, session, turn, addr("worktree", ""), map[string]any{"caller_team": "owner"})
		require.NoError(t, err)
	}

	require.Error(t, rt.checkActivationBudget(ctx, session.ID, "owner", "worktree"),
		"the third activation from owner to worktree must be refused")
	require.NoError(t, rt.checkActivationBudget(ctx, session.ID, "owner", "fix"),
		"a different target is unaffected")
	require.NoError(t, rt.checkActivationBudget(ctx, session.ID, "other", "worktree"),
		"a different source is unaffected")
}
