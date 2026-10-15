package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestCountTeamTurnsCountsStarts proves the session-level counter reads off the
// event stream, so a Team-turn budget no longer needs its own state.
func TestCountTeamTurnsCountsStarts(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	for _, team := range []string{"owner", "worktree", "owner"} {
		require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
			EventHeader: types.EventHeader{
				Type:          types.EventTeamTurnStarted,
				FlowSessionID: session.ID,
				TeamID:        team,
			},
		}))
	}

	n, err := rt.countTeamTurns(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, 3, n)
}

// TestRunMessageTurnStopsAtSessionTeamTurnBudget pins the backstop: once the
// session has started max_team_turns rounds, a further dispatched message is
// marked dead instead of running another round. It is the session total that
// bounds a dispatching chain, not any single turn.
func TestRunMessageTurnStopsAtSessionTeamTurnBudget(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)
	rt.SetLimits(types.RuntimeLimits{MaxTeamTurns: 1})

	// One round already spent.
	require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamTurnStarted,
			FlowSessionID: session.ID,
			TeamID:        "owner",
		},
	}))

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("worktree", ""), map[string]any{"caller_team": "owner"})
	require.NoError(t, err)

	rt.runMessageTurn(ctx, session, msg)

	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, types.MailboxDead, box[msg.MessageID].Status, "over-budget work must not run")
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
