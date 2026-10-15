package flow

import (
	"context"
	"encoding/json"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// teamSessionPayloadKey is the payload key that carries a TeamSession in a
// team_session.created / team_session.updated event, matching the
// payload:{"record": record} convention used elsewhere in this package.
const teamSessionPayloadKey = "team_session"

// agentSessionPayloadKey is the payload key that carries an AgentSession in an
// agent_session.created / agent_session.updated event.
const agentSessionPayloadKey = "agent_session"

// loadTeamSessions folds the session's team_session events into the latest
// TeamSession seen for each Team. It is the read path behind "is this Team
// executing?": the events were always written, but nothing ever read them
// back, so a scheduler that dispatches by execution state has no other way to
// know.
//
// The fold relies on replayFlowEvents returning events sorted by Seq (the
// session writer allocates Seq under a mutex and Replay sorts by it), so the
// last write for a Team wins regardless of the order the files were read.
func (r *Runtime) loadTeamSessions(ctx context.Context, sessionID string) (map[string]types.TeamSession, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	sessions := make(map[string]types.TeamSession)
	for _, event := range events {
		switch event.Type {
		case types.EventTeamSessionCreated, types.EventTeamSessionUpdated:
			session, ok := decodeTeamSession(event.Payload)
			if !ok {
				continue
			}
			sessions[session.TeamID] = session
		}
	}
	return sessions, nil
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

// countTeamTurns returns how many Team rounds the session has started. It is
// the session-level total a Team-turn budget is measured against: with several
// Teams running and each turn dispatching more, a per-turn count no longer
// bounds anything, but the event stream still records every round.
func (r *Runtime) countTeamTurns(ctx context.Context, sessionID string) (int, error) {
	events, err := r.replayFlowEvents(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, event := range events {
		if event.Type == types.EventTeamTurnStarted {
			n++
		}
	}
	return n, nil
}

// teamIdle reports whether a Team can take a new message. It encodes the one
// scheduling rule of the execution-state model: a Team that is not executing is
// free. A Team never seen in the event stream has never run, so it is idle.
func teamIdle(sessions map[string]types.TeamSession, teamID string) bool {
	session, ok := sessions[teamID]
	if !ok {
		return true
	}
	return session.Status != types.SessionRunning
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
