package flow

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// teamSessionPayloadKey is the payload key that carries a TeamSession in a
// session.team_session.created / session.team_session.updated event, matching the
// payload:{"record": record} convention used elsewhere in this package.
const teamSessionPayloadKey = "team_session"

// agentSessionPayloadKey is the payload key that carries an AgentSession in an
// session.agent_session.created / session.agent_session.updated event.
const agentSessionPayloadKey = "agent_session"

// loadTeamInstances folds the session's team_session events into every instance
// seen for each Team, in SessionSeq order.
//
// loadTeamSessions keeps one entry per Team because the scheduler only needs
// "is this Team free". A board needs the instances: a per_task Team can have
// several running at once, and collapsing them makes the last event win — a
// finished instance can hide a running one and report the Team as idle.
func (r *Runtime) loadTeamInstances(ctx context.Context, sessionID string) (map[string][]types.TeamSession, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]types.TeamSession)
	order := make([]string, 0, len(events))
	for _, event := range events {
		switch event.Type {
		case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
			session, ok := decodeTeamSession(event.Payload)
			if !ok || session.ID == "" {
				continue
			}
			if _, seen := byID[session.ID]; !seen {
				order = append(order, session.ID)
			}
			byID[session.ID] = session
		}
	}

	instances := make(map[string][]types.TeamSession, len(byID))
	for _, id := range order {
		session := byID[id]
		instances[session.TeamID] = append(instances[session.TeamID], session)
	}
	for teamID := range instances {
		list := instances[teamID]
		sort.SliceStable(list, func(i, j int) bool { return list[i].SessionSeq < list[j].SessionSeq })
		instances[teamID] = list
	}
	return instances, nil
}

// nextTeamSessionSeq returns the next per_task instance ordinal for a Team in
// one FlowSession.
//
// It derives the number from the event stream instead of keeping an in-memory
// counter: after a restart the next instance must not reuse an id that already
// owns transcripts and records. Resident sessions are not numbered (seq 0), so
// this only ever grows the per_task space.
func (r *Runtime) nextTeamSessionSeq(ctx context.Context, sessionID, teamID string) (int, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, event := range events {
		switch event.Type {
		case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
			session, ok := decodeTeamSession(event.Payload)
			if !ok || session.TeamID != teamID {
				continue
			}
			if session.SessionSeq > max {
				max = session.SessionSeq
			}
		}
	}
	return max + 1, nil
}

// loadAgentSessions folds the session's agent_session events into the latest
// AgentSession seen for each (Team, Call) pair, keyed by the AgentSession ID.
func (r *Runtime) loadAgentSessions(ctx context.Context, sessionID string) (map[string]types.AgentSession, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessions := make(map[string]types.AgentSession)
	for _, event := range events {
		switch event.Type {
		case types.EventAgentSessionCreated, types.EventAgentSessionUpdated:
			session, ok := decodeAgentSession(event.Payload)
			if !ok {
				continue
			}
			sessions[session.ID] = session
		}
	}
	return sessions, nil
}

// teamIdle reports whether a Team can take a new message. It encodes the one
// scheduling rule of the execution-state model: a Team with no instance
// executing is free. A Team never seen in the event stream has never run, so it
// is idle. per_task Teams can hold several instances, and any one of them
// running makes the Team busy.
func teamIdle(instances map[string][]types.TeamSession, teamID string) bool {
	for _, session := range instances[teamID] {
		if session.Status == types.SessionRunning {
			return false
		}
	}
	return true
}

// decodeTeamSession reads a TeamSession from an event payload. The value
// arrives either as the typed struct (in-memory) or as a generic map (after a
// JSONL round-trip), so a map is re-marshalled into the struct. It returns
// ok=false for a missing or malformed payload rather than failing the whole
// fold: one bad event must not hide every other Team's state.
func decodeTeamSession(payload map[string]any) (types.TeamSession, bool) {
	return decodeSessionValue[types.TeamSession](payload, teamSessionPayloadKey)
}

func decodeAgentSession(payload map[string]any) (types.AgentSession, bool) {
	return decodeSessionValue[types.AgentSession](payload, agentSessionPayloadKey)
}

func decodeSessionValue[T any](payload map[string]any, key string) (T, bool) {
	var zero T
	if payload == nil {
		return zero, false
	}
	raw, ok := payload[key]
	if !ok {
		return zero, false
	}
	switch v := raw.(type) {
	case T:
		return v, true
	case map[string]any:
		data, err := json.Marshal(v)
		if err != nil {
			return zero, false
		}
		var session T
		if err := json.Unmarshal(data, &session); err != nil {
			return zero, false
		}
		return session, true
	default:
		return zero, false
	}
}
