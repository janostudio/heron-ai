package flow

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

func teamTurnStartedEvent(flowTurnID, teamTurnID, teamID string) storage.SessionEvent {
	return storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:        types.EventTeamTurnStarted,
			FlowTurnID:  flowTurnID,
			TeamTurnID:  teamTurnID,
			TeamID:      teamID,
		},
		Payload: map[string]any{"input": "go"},
	}
}

// TestInterruptedExecutionsSkipsLiveTeams pins the fix that keeps a running
// dispatched Team from being reported as interrupted. Started-with-no-
// completion looks the same for a Team still running and one a crash left
// behind; only the former is in liveTeams, and only the latter must be reported.
func TestInterruptedExecutionsSkipsLiveTeams(t *testing.T) {
	events := []storage.SessionEvent{
		teamTurnStartedEvent("ft_1", "tt_owner", "owner"),
		teamTurnStartedEvent("ft_1", "tt_leaf", "leaf"),
	}

	// Both look interrupted when nothing is known to be running.
	all := interruptedExecutions(events, nil)
	require.Len(t, all, 2, "started with no completion is interrupted by default")

	// The running Team is excluded; the other still looks interrupted.
	live := interruptedExecutions(events, map[string]bool{"owner": true})
	require.Len(t, live, 1)
	require.Equal(t, "leaf", live[0].TeamID, "only the Team not known to be running is reported")
}

// TestInterruptedExecutionsSkipsCallsUnderLiveTeams covers the call-level entry:
// a call whose Team is still running must not be reported either, since it is
// the same live round.
func TestInterruptedExecutionsSkipsCallsUnderLiveTeams(t *testing.T) {
	events := []storage.SessionEvent{
		{
			EventHeader: types.EventHeader{
				Type:        types.EventAgentTurnStarted,
				FlowTurnID:  "ft_1",
				TeamTurnID:  "tt_owner",
				TeamID:      "owner",
				CallTurnID:  "ct_1",
			},
			Payload: map[string]any{"input": "go"},
		},
	}

	require.Len(t, interruptedExecutions(events, nil), 1)
	require.Empty(t, interruptedExecutions(events, map[string]bool{"owner": true}))
}
