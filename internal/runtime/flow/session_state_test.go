package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// appendTeamSessionEvent writes a team_session event for teamID with status,
// the same shape executeTeamTurn writes, so the fold reads back what the
// runtime actually produces.
func appendTeamSessionEvent(t *testing.T, rt *Runtime, sessionID, teamID string, status types.SessionStatus) {
	t.Helper()
	session := types.TeamSession{
		ID:            sessionID + ":" + teamID,
		FlowSessionID: sessionID,
		TeamID:        teamID,
		Status:        status,
	}
	require.NoError(t, rt.appendSessionEvent(context.Background(), sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionUpdated,
			FlowSessionID: sessionID,
			TeamSessionID: session.ID,
			TeamID:        teamID,
		},
		Payload: map[string]any{teamSessionPayloadKey: session},
	}))
}

// TestLoadTeamSessionsFoldsLatestPerTeam verifies the read path a
// state-driven scheduler needs: the last status written for a Team wins, so
// "is this Team executing?" is answerable from the event stream alone.
func TestLoadTeamSessionsFoldsLatestPerTeam(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendTeamSessionEvent(t, rt, session.ID, "owner", types.SessionRunning)
	appendTeamSessionEvent(t, rt, session.ID, "worktree", types.SessionRunning)
	appendTeamSessionEvent(t, rt, session.ID, "worktree", types.SessionCompleted)
	appendTeamSessionEvent(t, rt, session.ID, "owner", types.SessionWaitingInput)

	sessions, err := rt.loadTeamSessions(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, types.SessionWaitingInput, sessions["owner"].Status)
	require.Equal(t, types.SessionCompleted, sessions["worktree"].Status)
}

// TestTeamIdleIsTheSchedulingRule pins the one rule the model adds: a Team that
// is not executing can take a message, and a Team never seen has never run.
func TestTeamIdleIsTheSchedulingRule(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendTeamSessionEvent(t, rt, session.ID, "owner", types.SessionRunning)
	appendTeamSessionEvent(t, rt, session.ID, "worktree", types.SessionCompleted)

	sessions, err := rt.loadTeamSessions(ctx, session.ID)
	require.NoError(t, err)

	require.False(t, teamIdle(sessions, "owner"), "a running team is busy")
	require.True(t, teamIdle(sessions, "worktree"), "a completed team is free")
	require.True(t, teamIdle(sessions, "never-seen"), "a team that never ran is free")
}

// TestLoadTeamSessionsEmptySessionIsNotAnError keeps the read path usable on a
// fresh session: an empty stream folds to no sessions rather than failing.
func TestLoadTeamSessionsEmptySessionIsNotAnError(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	sessions, err := rt.loadTeamSessions(context.Background(), "fs_never_written")
	require.NoError(t, err)
	require.Empty(t, sessions)
}

// TestLoadTeamSessionsIgnoresMalformedPayload guards the fold against one bad
// event hiding every other Team's state.
func TestLoadTeamSessionsIgnoresMalformedPayload(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendTeamSessionEvent(t, rt, session.ID, "owner", types.SessionRunning)
	require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionUpdated,
			FlowSessionID: session.ID,
		},
		Payload: map[string]any{teamSessionPayloadKey: "not-a-session"},
	}))

	sessions, err := rt.loadTeamSessions(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, types.SessionRunning, sessions["owner"].Status)
}
