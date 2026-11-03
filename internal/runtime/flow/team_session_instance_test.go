package flow

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestTeamSessionIDKeepsResidentCompatibility pins the id contract: resident
// sessions keep the historical two-part id (so existing transcripts and state
// files still resolve) and per_task sessions append their ordinal.
func TestTeamSessionIDKeepsResidentCompatibility(t *testing.T) {
	require.Equal(t, "fs:dev", teamSessionID("fs", "dev", 0))
	require.Equal(t, "fs:dev:2", teamSessionID("fs", "dev", 2))
}

// TestNextTeamSessionSeqReadsTheEventStream verifies the ordinal is derived
// from what already happened, not from an in-memory counter: after a restart
// the next instance must not reuse an id that already owns transcripts.
func TestNextTeamSessionSeqReadsTheEventStream(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	appendTeamSessionWithSeq(t, rt, session.ID, "dev", 1)
	appendTeamSessionWithSeq(t, rt, session.ID, "dev", 3)
	appendTeamSessionWithSeq(t, rt, session.ID, "check", 9)

	next, err := rt.nextTeamSessionSeq(ctx, session.ID, "dev")
	require.NoError(t, err)
	require.Equal(t, 4, next)

	next, err = rt.nextTeamSessionSeq(ctx, session.ID, "check")
	require.NoError(t, err)
	require.Equal(t, 10, next)

	// A Team that never ran starts at 1, and an unknown session is not an error.
	next, err = rt.nextTeamSessionSeq(ctx, session.ID, "never-run")
	require.NoError(t, err)
	require.Equal(t, 1, next)
}

// TestPerTaskTeamRejectsRouteActivation guards the one way a per_task binding
// could silently run without a task: being reached by routing instead of being
// dispatched with a task_key. The error names the fix instead of creating an
// instance nobody owns.
func TestPerTaskTeamRejectsRouteActivation(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, _ := mailboxTestSession(t)

	defs := &types.Definitions{
		Flow: types.Flow{
			ID:          "f",
			EntryTeamID: "entry",
			Teams: map[string]types.FlowTeamBinding{
				"entry": {ID: "entry", TeamID: "entry_team"},
				"dev": {
					ID:        "dev",
					TeamID:    "dev_team",
					Lifecycle: types.TeamLifecyclePerTask,
					Inputs:    types.InputSpec{UserMessage: true},
				},
			},
		},
		Teams: map[string]types.Team{
			"entry_team": {ID: "entry_team"},
			"dev_team":   {ID: "dev_team"},
		},
	}
	flowTurn := types.FlowTurn{ID: "ft_test", FlowSessionID: session.ID, Status: types.TurnRunning}

	_, err := rt.executeTeamTurn(ctx, defs, session, flowTurn,
		activation{teamID: "dev"}, "task", nil, time.Now().UTC(), nil)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "task_key"),
		"error should tell the caller a task_key is required, got %v", err)
}

func appendTeamSessionWithSeq(t *testing.T, rt *Runtime, sessionID, teamID string, seq int) {
	t.Helper()
	err := rt.appendSessionEvent(context.Background(), sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionCreated,
			FlowSessionID: sessionID,
		},
		Payload: map[string]any{"team_session": types.TeamSession{
			ID:            teamSessionID(sessionID, teamID, seq),
			FlowSessionID: sessionID,
			TeamID:        teamID,
			SessionSeq:    seq,
			Lifecycle:     types.TeamLifecyclePerTask,
			Status:        types.SessionCompleted,
		}},
	})
	require.NoError(t, err)
}
