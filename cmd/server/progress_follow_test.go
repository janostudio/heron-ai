package main

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// sessionHasBackgroundWork decides whether a following pump may close. It
// reads the same event log the caller sees: a turn that routes to activate,
// or mail that nobody consumed, means the answer is still coming.
func TestSessionHasBackgroundWork(t *testing.T) {
	terminal := func(action string) storage.SessionEvent {
		return storage.SessionEvent{
			EventHeader: types.EventHeader{Type: types.EventFlowTurnWaitingInput},
			Payload: map[string]any{
				"turn": map[string]any{"next": map[string]any{"action": action}},
			},
		}
	}
	mail := func(eventType, id string) storage.SessionEvent {
		return storage.SessionEvent{
			EventHeader: types.EventHeader{Type: eventType},
			Payload:     map[string]any{"message": map[string]any{"message_id": id}},
		}
	}

	cases := []struct {
		name   string
		events []storage.SessionEvent
		want   bool
	}{
		{name: "no events", events: nil, want: false},
		{name: "turn dispatched to another team", events: []storage.SessionEvent{terminal("activate")}, want: true},
		{name: "turn answered", events: []storage.SessionEvent{terminal("proceed")}, want: false},
		{name: "turn handed back for a decision", events: []storage.SessionEvent{terminal("coordinate")}, want: false},
		{
			name: "pending mail",
			events: []storage.SessionEvent{
				terminal("proceed"),
				mail(types.EventMailboxMessageEnqueued, "mb_1"),
			},
			want: true,
		},
		{
			name: "claimed mail is still outstanding",
			events: []storage.SessionEvent{
				mail(types.EventMailboxMessageEnqueued, "mb_1"),
				mail(types.EventMailboxMessageClaimed, "mb_1"),
			},
			want: true,
		},
		{
			name: "delivered mail is done",
			events: []storage.SessionEvent{
				mail(types.EventMailboxMessageEnqueued, "mb_1"),
				mail(types.EventMailboxMessageDelivered, "mb_1"),
				terminal("proceed"),
			},
			want: false,
		},
		{
			name: "dead mail must not hold the subscription",
			events: []storage.SessionEvent{
				mail(types.EventMailboxMessageEnqueued, "mb_1"),
				mail(types.EventMailboxMessageDead, "mb_1"),
				terminal("proceed"),
			},
			want: false,
		},
		{
			name: "two dispatches, one delivered",
			events: []storage.SessionEvent{
				mail(types.EventMailboxMessageEnqueued, "mb_1"),
				mail(types.EventMailboxMessageEnqueued, "mb_2"),
				mail(types.EventMailboxMessageDelivered, "mb_1"),
			},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, sessionHasBackgroundWork(tc.events))
		})
	}
}

// Live subscribers hand over typed payloads while a replay yields decoded
// maps; both shapes have to answer the same way or the follower would close
// the window on one path and not the other.
func TestSessionHasBackgroundWorkAcceptsTypedPayloads(t *testing.T) {
	typedTurn := storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowTurnWaitingInput},
		Payload: map[string]any{
			"turn": types.FlowTurn{Next: &types.Route{Action: types.NextActivate}},
		},
	}
	require.True(t, sessionHasBackgroundWork([]storage.SessionEvent{typedTurn}))

	typedMail := storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventMailboxMessageEnqueued},
		Payload:     map[string]any{"message": types.MailboxMessage{MessageID: "mb_typed"}},
	}
	require.True(t, sessionHasBackgroundWork([]storage.SessionEvent{typedMail}))
}

// A second turn on a session that is still streaming must attach to the
// running pump. Opening another would deliver every event twice.
func TestStartTurnPumpReusesExistingStream(t *testing.T) {
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	server := newJSONRPCServer(nil, "", io.Discard).withSessions(writer)

	existing := &eventPump{done: make(chan struct{})}
	close(existing.done)
	server.registerPump("fs_1", existing)

	require.Nil(t, server.startTurnPump(context.Background(), "fs_1"),
		"a second turn must attach to the running stream, not open another")
	require.Nil(t, server.startTurnPump(context.Background(), "  "),
		"an empty session id cannot host a subscription")
}

// releasePump must unregister before it waits for the drain, so a turn that
// arrives mid-drain opens a fresh stream instead of attaching to a closing one.
func TestReleasePumpUnregistersStream(t *testing.T) {
	server := newJSONRPCServer(nil, "", io.Discard)
	pump := &eventPump{done: make(chan struct{})}
	close(pump.done)
	server.registerPump("fs_1", pump)

	server.releasePump("fs_1", pump)

	server.pumpMu.Lock()
	_, registered := server.pumps["fs_1"]
	server.pumpMu.Unlock()
	require.False(t, registered, "a released pump must not stay attached to the session")
}
