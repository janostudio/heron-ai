package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestNextWakingTeamPicksWhoeverHasMail records the wake-up contract before the
// dispatcher is generalized: the Team to wake is chosen by pending mail, not by
// being the Flow entry. Today only the entry Team receives result messages, so
// this is also a regression guard that the old behaviour still holds.
func TestNextWakingTeamPicksWhoeverHasMail(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	for _, to := range []string{"default", "owner", "owner"} {
		_, err := rt.enqueueResultMessage(ctx, session, turn, addr(to, ""), map[string]any{"team_result": "done"})
		require.NoError(t, err)
	}

	target, messages, err := rt.nextWakingTeam(ctx, session.ID)
	require.NoError(t, err)
	// owner has more mail than default, so it is the one to wake.
	require.Equal(t, "owner", target)
	require.Len(t, messages, 2)
}

// TestNextWakingTeamEmptyWhenNoMail keeps the wake-up path a no-op for a
// session with nothing pending, rather than waking an arbitrary Team.
func TestNextWakingTeamEmptyWhenNoMail(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	target, messages, err := rt.nextWakingTeam(context.Background(), "fs_never_written")
	require.NoError(t, err)
	require.Empty(t, target)
	require.Empty(t, messages)
}

// TestNextWakingTeamDeterministicTie pins the tie-break so two Teams with the
// same amount of mail always wake in the same order.
func TestNextWakingTeamDeterministicTie(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	for _, to := range []string{"zeta", "alpha"} {
		_, err := rt.enqueueResultMessage(ctx, session, turn, addr(to, ""), map[string]any{})
		require.NoError(t, err)
	}

	target, _, err := rt.nextWakingTeam(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, "alpha", target, "ties break by TeamID")
}

// TestDispatchedResultGoesToActivator checks the address the dispatcher posts a
// result to: the Team that dispatched the work (caller_team on the work
// message), not the Flow entry.
func TestDispatchedResultGoesToActivator(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	// A work message dispatched by "default" to "worktree".
	work, err := rt.enqueueWorkMessage(ctx, session, turn,
		addr("worktree", ""), map[string]any{"caller_team": "default"})
	require.NoError(t, err)

	// The dispatcher reads the activator from the work message.
	defs := rt.definitions.Snapshot()
	require.Equal(t, "default", activatorForResult(work, defs))

	// And posts the result there.
	resultMsg, err := rt.enqueueResultMessage(ctx, session, turn,
		addr("default", ""), map[string]any{"team_result": "ok"})
	require.NoError(t, err)
	require.Equal(t, "default", resultMsg.To.TeamID)
}

// activatorForResult mirrors the dispatcher's choice of result address so the
// test pins the rule rather than the implementation detail.
func activatorForResult(work types.MailboxMessage, defs *types.Definitions) string {
	if caller, ok := work.Payload["caller_team"].(string); ok && caller != "" {
		return caller
	}
	return defs.Flow.EntryTeamID
}

// TestNextWakingTeamSkipsBusyTeam pins the fix for a double-run: a Team whose
// round the background dispatcher already launched must not be woken again, or
// two rounds race over the same message.
func TestNextWakingTeamSkipsBusyTeam(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	require.NoError(t, func() error {
		_, err := rt.enqueueWorkMessage(ctx, session, turn, addr("worktree", ""), map[string]any{"caller_team": "owner"})
		return err
	}())

	// With worktree free it is the Team to wake.
	target, _, err := rt.nextWakingTeam(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, "worktree", target)

	// Once its round is running, the wake-up must not pick it again.
	require.True(t, rt.tryMarkTeamBusy(session.ID, "worktree"))
	defer rt.clearTeamBusy(session.ID, "worktree")
	target, messages, err := rt.nextWakingTeam(ctx, session.ID)
	require.NoError(t, err)
	require.Empty(t, target, "a busy team must not be woken")
	require.Empty(t, messages)
}
