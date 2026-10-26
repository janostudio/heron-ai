package flow

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// TestDecodeMailboxValueAcceptsTypedAndMapForms covers the two shapes a
// mailbox message takes: the typed struct held in memory and the generic map
// that comes back from a JSONL round-trip. Both must decode identically.
func TestDecodeMailboxValueAcceptsTypedAndMapForms(t *testing.T) {
	typed := types.MailboxMessage{
		MessageID:       "mb_1",
		FlowSessionID:   "fs_1",
		To:              addr("verify", ""),
		Kind:            mailboxKindWork,
		CauseFlowTurnID: "ft_1",
		Status:          types.MailboxClaimed,
		Revision:        2,
		CreatedAt:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}

	fromTyped, err := decodeMailboxValue(typed)
	require.NoError(t, err)
	assert.Equal(t, typed.MessageID, fromTyped.MessageID)
	assert.Equal(t, types.MailboxClaimed, fromTyped.Status)
	assert.Equal(t, 2, fromTyped.Revision)

	data, err := json.Marshal(typed)
	require.NoError(t, err)
	var asMap map[string]any
	require.NoError(t, json.Unmarshal(data, &asMap))

	fromMap, err := decodeMailboxValue(asMap)
	require.NoError(t, err)
	assert.Equal(t, typed.MessageID, fromMap.MessageID)
	assert.Equal(t, typed.To, fromMap.To)
	assert.Equal(t, types.MailboxClaimed, fromMap.Status)
	assert.Equal(t, 2, fromMap.Revision)
	assert.Equal(t, "ft_1", fromMap.CauseFlowTurnID)
}

// TestDecodeMailboxValueDegradesOnMalformedInput proves every abnormal input
// degrades instead of panicking: JSON null decodes to the zero message, and
// anything that cannot be marshalled or unmarshalled is reported as an error
// that decodeMailboxMessage turns into "not a mailbox message".
func TestDecodeMailboxValueDegradesOnMalformedInput(t *testing.T) {
	cases := []struct {
		name    string
		raw     any
		wantErr bool
	}{
		{name: "nil decodes to zero value", raw: nil, wantErr: false},
		{name: "string", raw: "not-a-message", wantErr: true},
		{name: "number", raw: 42, wantErr: true},
		{name: "slice", raw: []any{"mb_1"}, wantErr: true},
		{name: "map with wrong field type", raw: map[string]any{"message_id": 42}, wantErr: true},
		{name: "map that cannot marshal", raw: map[string]any{"payload": func() {}}, wantErr: true},
		{name: "value that cannot marshal", raw: make(chan int), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var msg types.MailboxMessage
			var err error
			require.NotPanics(t, func() {
				msg, err = decodeMailboxValue(tc.raw)
			})
			if tc.wantErr {
				require.Error(t, err)
				assert.Empty(t, msg.MessageID)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, msg.MessageID, "an unusable value must not fabricate a message")
		})
	}
}

// TestDecodeMailboxMessageRejectsUnusablePayloads covers the guards in front of
// decodeMailboxValue: a nil payload, a payload without the message key, and a
// payload whose message value is undecodable all report "not a message".
func TestDecodeMailboxMessageRejectsUnusablePayloads(t *testing.T) {
	_, ok := decodeMailboxMessage(nil)
	require.False(t, ok)

	_, ok = decodeMailboxMessage(map[string]any{"other": "value"})
	require.False(t, ok)

	_, ok = decodeMailboxMessage(map[string]any{mailboxPayloadKey: "not-a-message"})
	require.False(t, ok)

	// A null message decodes without error (JSON null is a valid empty
	// object), so this reports ok with an empty MessageID; foldMailbox is
	// what drops it, via its own MessageID guard.
	nullMsg, ok := decodeMailboxMessage(map[string]any{mailboxPayloadKey: nil})
	require.True(t, ok)
	require.Empty(t, nullMsg.MessageID)

	msg, ok := decodeMailboxMessage(map[string]any{
		mailboxPayloadKey: map[string]any{"message_id": "mb_1", "status": "pending"},
	})
	require.True(t, ok)
	assert.Equal(t, "mb_1", msg.MessageID)
}

// TestFoldMailboxSkipsUndecodableEvents proves the fold degrades rather than
// failing: events that are not mailbox lifecycle events, or that carry an
// unusable message, are skipped instead of poisoning the folded state.
func TestFoldMailboxSkipsUndecodableEvents(t *testing.T) {
	event := func(seq int64, eventType string, payload map[string]any) storage.SessionEvent {
		return storage.SessionEvent{
			EventHeader: types.EventHeader{Seq: seq, Type: eventType, FlowSessionID: "fs_1"},
			Payload:     payload,
		}
	}

	good := event(1, types.EventMailboxMessageEnqueued, map[string]any{
		mailboxPayloadKey: map[string]any{"message_id": "mb_good", "status": "pending"},
	})

	box := foldMailbox([]storage.SessionEvent{
		event(2, types.EventFlowSessionCreated, map[string]any{"session": "x"}),            // wrong type
		event(3, types.EventMailboxMessageEnqueued, map[string]any{"other": "x"}),          // no message key
		event(4, types.EventMailboxMessageClaimed, map[string]any{mailboxPayloadKey: 42}),  // undecodable
		event(5, types.EventMailboxMessageEnqueued, nil),                                   // nil payload
		event(7, types.EventMailboxMessageClaimed, map[string]any{mailboxPayloadKey: nil}), // null message
		event(6, types.EventMailboxMessageEnqueued, map[string]any{
			mailboxPayloadKey: map[string]any{"status": "pending"}, // no message id
		}),
		good,
	})

	require.Len(t, box, 1)
	assert.Contains(t, box, "mb_good")
	assert.Equal(t, types.MailboxPending, box["mb_good"].Status)
}
