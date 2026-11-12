package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// twoInstanceDefinitions is a Flow whose only non-entry Team is per_task, so it
// can hold several instances at once.
func twoInstanceDefinitions() *types.Definitions {
	return &types.Definitions{
		Flow: types.Flow{
			ID:          "demo",
			EntryTeamID: "desk",
			Teams: map[string]types.FlowTeamBinding{
				"desk": {ID: "desk", TeamID: "desk-team", CanActivate: []string{"work"}},
				"work": {ID: "work", TeamID: "work-team", Lifecycle: types.TeamLifecyclePerTask},
			},
		},
		Teams: map[string]types.Team{
			"desk-team": {ID: "desk-team"},
			"work-team": {ID: "work-team"},
		},
	}
}

func appendTeamSession(t *testing.T, rt *Runtime, sessionID, instanceID, teamID string, seq int, status types.SessionStatus) {
	t.Helper()
	err := rt.appendSessionEvent(context.Background(), sessionID, storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventTeamSessionCreated,
			FlowSessionID: sessionID,
		},
		Payload: map[string]any{"team_session": types.TeamSession{
			ID:            instanceID,
			FlowSessionID: sessionID,
			TeamID:        teamID,
			SessionSeq:    seq,
			Lifecycle:     types.TeamLifecyclePerTask,
			Status:        status,
		}},
	})
	require.NoError(t, err)
}

// A per_task Team can execute several tasks at once. Folding team sessions by
// Team id made the last event win, so a finished instance could hide a running
// one and the board would report the Team as idle; the board has to count
// instances instead.
func TestTopologyCountsConcurrentPerTaskInstances(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	rt := newTestRuntime(twoInstanceDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), nil)

	const sessionID = "fs_topo_concurrent"
	appendTeamSession(t, rt, sessionID, sessionID+":work:1", "work", 1, types.SessionRunning)
	appendTeamSession(t, rt, sessionID, sessionID+":work:2", "work", 2, types.SessionRunning)

	topology, err := rt.Topology(context.Background(), sessionID)
	require.NoError(t, err)

	byName := make(map[string]types.TopologyTeam, len(topology.Teams))
	for _, team := range topology.Teams {
		byName[team.Name] = team
	}
	work := byName["work"]
	require.Equal(t, topologyStatusRunning, work.Status)
	require.Equal(t, 2, work.Instances)
	require.Equal(t, 2, work.RunningInstances)
	require.Equal(t, topologyStatusNever, byName["desk"].Status, "a Team that never ran is still never")
}

// One instance finished, one still running: the Team is busy. This is the case
// the collapsed fold got wrong.
func TestTopologyReportsRunningWhenOneInstanceIsStillExecuting(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	rt := newTestRuntime(twoInstanceDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), nil)

	const sessionID = "fs_topo_mixed"
	appendTeamSession(t, rt, sessionID, sessionID+":work:1", "work", 1, types.SessionCompleted)
	appendTeamSession(t, rt, sessionID, sessionID+":work:2", "work", 2, types.SessionRunning)

	topology, err := rt.Topology(context.Background(), sessionID)
	require.NoError(t, err)

	for _, team := range topology.Teams {
		if team.Name != "work" {
			continue
		}
		require.Equal(t, topologyStatusRunning, team.Status)
		require.Equal(t, 2, team.Instances)
		require.Equal(t, 1, team.RunningInstances)
		return
	}
	t.Fatal("team work missing from topology")
}

// Nothing running, but the Team has instances: idle, not never.
func TestTopologyReportsIdleWhenNoInstanceExecutes(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	rt := newTestRuntime(twoInstanceDefinitions(), &fakeTeamRuntime{},
		storage.NewJSONLSessionWriter(fileStore), nil)

	const sessionID = "fs_topo_idle"
	appendTeamSession(t, rt, sessionID, sessionID+":work:1", "work", 1, types.SessionClosed)

	topology, err := rt.Topology(context.Background(), sessionID)
	require.NoError(t, err)

	for _, team := range topology.Teams {
		if team.Name != "work" {
			continue
		}
		require.Equal(t, topologyStatusIdle, team.Status)
		require.Equal(t, 1, team.Instances)
		require.Zero(t, team.RunningInstances)
		return
	}
	t.Fatal("team work missing from topology")
}
