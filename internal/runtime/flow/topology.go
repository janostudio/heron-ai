package flow

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Topology reports the Flow's configuration graph, plus the execution state of
// one session when sessionID is non-empty.
//
// The graph comes from the definitions in force right now, not from the event
// stream: a Team that has never run still belongs on the board. Execution state
// is folded from the same session events the scheduler reads, so a restarted
// process answers the same way it did before the restart.
func (r *Runtime) Topology(ctx context.Context, sessionID string) (types.FlowTopology, error) {
	if r.definitions == nil {
		return types.FlowTopology{}, errors.New("definitions are nil")
	}
	defs := r.definitions.Snapshot()
	if defs == nil {
		return types.FlowTopology{}, errors.New("definitions are not loaded")
	}

	teamInstances := map[string][]types.TeamSession{}
	agentSessions := map[string]types.AgentSession{}
	if sessionID != "" {
		var err error
		teamInstances, err = r.loadTeamInstances(ctx, sessionID)
		if err != nil {
			return types.FlowTopology{}, err
		}
		agentSessions, err = r.loadAgentSessions(ctx, sessionID)
		if err != nil {
			return types.FlowTopology{}, err
		}
	}

	topology := types.FlowTopology{
		FlowID: defs.Flow.ID,
		Entry:  defs.Flow.EntryTeamID,
	}

	names := make([]string, 0, len(defs.Flow.Teams))
	for name := range defs.Flow.Teams {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		binding := defs.Flow.Teams[name]
		definition := defs.Teams[binding.TeamID]

		team := types.TopologyTeam{
			Name:        name,
			TeamID:      binding.TeamID,
			Goal:        definition.Goal,
			Entry:       name == defs.Flow.EntryTeamID,
			Lifecycle:   string(binding.EffectiveLifecycle()),
			CanActivate: binding.CanActivate,
			Calls:       topologyCalls(definition),
			Status:      topologyStatusNever,
			CallStatus:  map[string]string{},
		}

		// per_task Teams can have several instances at once: the Team is running
		// when any of them is, and a Call is running when any instance runs it.
		instances := teamInstances[name]
		if len(instances) > 0 {
			team.Instances = len(instances)
			team.Status = topologyStatusIdle
		}
		for _, instance := range instances {
			if instance.Status != types.SessionRunning {
				continue
			}
			team.RunningInstances++
			team.Status = topologyStatusRunning
		}
		for _, agentSession := range agentSessions {
			if teamBindingOf(agentSession.TeamSessionID) != name || agentSession.CallID == "" {
				continue
			}
			status := topologyStatusOf(agentSession.Status)
			// running wins over idle: with two instances of the same Call, one
			// finished and one executing, the Team is still busy.
			if team.CallStatus[agentSession.CallID] == topologyStatusRunning {
				continue
			}
			team.CallStatus[agentSession.CallID] = status
		}
		if len(team.CallStatus) == 0 {
			team.CallStatus = nil
		}

		topology.Teams = append(topology.Teams, team)
	}

	return topology, nil
}

const (
	topologyStatusRunning = "running"
	topologyStatusIdle    = "idle"
	topologyStatusNever   = "never"
)

// topologyStatusOf maps a session status onto the three states the board shows.
// Anything that is not currently running reads as idle: a failed or cancelled
// turn is over, and calling it "running" would keep the board lying.
func topologyStatusOf(status types.SessionStatus) string {
	if status == types.SessionRunning {
		return topologyStatusRunning
	}
	return topologyStatusIdle
}

// teamBindingOf reads the Flow-local Team name out of a TeamSessionID, which
// reads "<flow session>:<team name>".
func teamBindingOf(teamSessionID string) string {
	if index := strings.LastIndex(teamSessionID, ":"); index >= 0 {
		return teamSessionID[index+1:]
	}
	return teamSessionID
}

func topologyCalls(team types.Team) []types.TopologyCall {
	names := make([]string, 0, len(team.Calls))
	for name := range team.Calls {
		names = append(names, name)
	}
	sort.Strings(names)

	calls := make([]types.TopologyCall, 0, len(names))
	for _, name := range names {
		call := team.Calls[name]
		calls = append(calls, types.TopologyCall{
			Name:  name,
			Type:  string(call.Type),
			Agent: call.AgentID,
		})
	}
	return calls
}
