package flow

import (
	"context"
	"encoding/json"
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

// appendAgentSessionEvent writes an agent_session event for agentID with
// status, the same shape the Team runtime writes (payload key "agent_session").
// The value travels as a generic map, the way it looks after a JSONL
// round-trip, so the fold is exercised on the form a resumed session holds.
func appendAgentSessionEvent(t *testing.T, rt *Runtime, sessionID, agentID string, status types.SessionStatus) {
	t.Helper()
	session := types.AgentSession{
		ID:      sessionID + ":" + agentID,
		AgentID: agentID,
		Status:  status,
	}
	data, err := json.Marshal(session)
	require.NoError(t, err)
	var asMap map[string]any
	require.NoError(t, json.Unmarshal(data, &asMap))

	require.NoError(t, rt.appendSessionEvent(context.Background(), sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventAgentSessionUpdated,
			FlowSessionID: sessionID,
			CallID:        agentID,
		},
		Payload: map[string]any{agentSessionPayloadKey: asMap},
	}))
}

// TestLoadAgentSessionsFoldsLatestPerAgent is the agent-level counterpart of
// the Team fold: the last status written for an AgentSession wins. Nothing in
// the scheduler reads it back yet — the events are written, so this pins the
// read path for the per-agent execution state a scheduler will need.
func TestLoadAgentSessionsFoldsLatestPerAgent(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendAgentSessionEvent(t, rt, session.ID, "writer", types.SessionRunning)
	appendAgentSessionEvent(t, rt, session.ID, "reviewer", types.SessionRunning)
	appendAgentSessionEvent(t, rt, session.ID, "reviewer", types.SessionCompleted)
	appendAgentSessionEvent(t, rt, session.ID, "writer", types.SessionWaitingInput)

	sessions, err := rt.loadAgentSessions(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, types.SessionWaitingInput, sessions[session.ID+":writer"].Status)
	require.Equal(t, types.SessionCompleted, sessions[session.ID+":reviewer"].Status)
	require.Equal(t, "writer", sessions[session.ID+":writer"].AgentID)
}

// TestLoadAgentSessionsEmptySessionIsNotAnError keeps the agent read path
// usable on a fresh session, like its Team counterpart.
func TestLoadAgentSessionsEmptySessionIsNotAnError(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	sessions, err := rt.loadAgentSessions(context.Background(), "fs_never_written")
	require.NoError(t, err)
	require.Empty(t, sessions)
}

// TestLoadAgentSessionsIgnoresMalformedPayload guards the agent fold against
// one bad event hiding every other Agent's state.
func TestLoadAgentSessionsIgnoresMalformedPayload(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendAgentSessionEvent(t, rt, session.ID, "writer", types.SessionRunning)
	require.NoError(t, rt.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventAgentSessionUpdated,
			FlowSessionID: session.ID,
		},
		Payload: map[string]any{agentSessionPayloadKey: "not-a-session"},
	}))

	sessions, err := rt.loadAgentSessions(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	require.Equal(t, types.SessionRunning, sessions[session.ID+":writer"].Status)
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
