package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestProjectEventFieldMatrix pins the field set each progress notification
// exposes. The individual projectors have focused tests; this one answers the
// question a connector author actually asks: for this event type, which fields
// can I rely on? A field silently disappearing breaks the connector without
// any error on either side.
//
// Two entries are deliberately listed as absent: they are the known contract
// gaps D2 and D8. They are asserted here so that fixing them has to update this
// test consciously, instead of the fix going unnoticed.
func TestProjectEventFieldMatrix(t *testing.T) {
	header := func(eventType string) types.EventHeader {
		return types.EventHeader{
			Type:          eventType,
			FlowSessionID: "fs-1",
			FlowTurnID:    "ft-1",
			TeamID:        "owner",
			CallID:        "owner",
			Seq:           7,
			EventID:       "fs-1-7",
		}
	}

	cases := []struct {
		name    string
		event   storage.SessionEvent
		want    []string
		absent  []string
		whyText string
	}{
		{
			name: "flow_turn_started",
			event: storage.SessionEvent{
				EventHeader: header(types.EventFlowTurnStarted),
				Payload:     map[string]any{"turn": types.FlowTurn{ID: "ft-1", Origin: "user"}, "input": "hi"},
			},
			want:   []string{"type", "session_id", "flow_turn_id", "seq", "event_id"},
			absent: []string{"origin", "reply"},
			// D2: origin lives on the turn, not at the payload top level, and
			// the projector only reads the top level.
		},
		{
			name: "flow_turn_completed",
			event: storage.SessionEvent{
				EventHeader: header(types.EventFlowTurnCompleted),
				Payload:     map[string]any{"turn": types.FlowTurn{ID: "ft-1", Status: types.TurnCompleted}},
			},
			want: []string{"type", "session_id", "flow_turn_id", "status"},
		},
		{
			name: "team_turn_started",
			event: storage.SessionEvent{
				EventHeader: header(types.EventTeamTurnStarted),
				Payload:     map[string]any{"team_turn": types.TeamTurn{ID: "tt-1", TeamID: "owner"}},
			},
			want: []string{"type", "session_id", "flow_turn_id", "team_id"},
		},
		{
			name: "call_started",
			event: storage.SessionEvent{
				EventHeader: header(types.EventAgentTurnStarted),
				Payload:     map[string]any{"call": types.Call{ID: "owner", AgentID: "owner"}},
			},
			want: []string{"type", "session_id", "flow_turn_id", "call_type", "call_id"},
		},
		{
			name: "record_published",
			event: storage.SessionEvent{
				EventHeader: header(types.EventSharedRecordPublished),
				Payload: map[string]any{"record": types.SharedRecord{
					RecordID: "r-1", Name: "Answer", Kind: "agent_result",
					Status: types.RecordActive, Revision: 1, Summary: "x",
					Producer: types.ProducerRef{TeamID: "worker", CallID: "do_work"},
				}},
			},
			want: []string{"type", "record_name", "kind", "status", "revision", "producer"},
		},
		{
			name: "record_consumed",
			event: storage.SessionEvent{
				EventHeader: header(types.EventSharedRecordConsumed),
				Payload: map[string]any{
					"record":        types.SharedRecord{RecordID: "r-1", Name: "WorkerReport"},
					"consumer_team": "main",
				},
			},
			want: []string{"type", "record_name", "consumer_team"},
		},
		{
			name: "status_waiting_input",
			event: storage.SessionEvent{
				EventHeader: header(types.EventFlowTurnWaitingInput),
				Payload: map[string]any{
					"turn":   types.FlowTurn{ID: "ft-1", Status: types.TurnWaitingInput, Origin: "tool"},
					"origin": "tool",
					"reply":  "done",
				},
			},
			want: []string{"type", "status", "origin", "reply", "session_id", "flow_turn_id"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := projectSessionEvent(tc.event)
			require.NotNil(t, params, "%s must reach the connector", tc.name)
			for _, field := range tc.want {
				require.Contains(t, params, field, "%s must expose %q", tc.name, field)
			}
			for _, field := range tc.absent {
				require.NotContains(t, params, field,
					"%s must not expose %q yet — if this changed, update D2/D8 in the test plan", tc.name, field)
			}
		})
	}
}

// TestProjectEventDropsBookkeeping makes the silence explicit: events the
// caller must never see return nil rather than an empty notification. A
// connector that received an empty frame would render an empty message.
func TestProjectEventDropsBookkeeping(t *testing.T) {
	event := storage.SessionEvent{
		EventHeader: types.EventHeader{
			Type:          types.EventMailboxMessageClaimed,
			FlowSessionID: "fs-1",
			FlowTurnID:    "ft-1",
		},
		Payload: map[string]any{},
	}
	require.Nil(t, projectSessionEvent(event),
		"mailbox bookkeeping must not become a user-visible notification")
}
