package types

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func sampleMailboxMessage() MailboxMessage {
	return MailboxMessage{
		MessageID:     "msg_1",
		FlowSessionID: "fs_1",
		From:          MailboxAddress{TeamID: "t_a", AgentID: "a1"},
		To:            MailboxAddress{TeamID: "t_b", AgentID: "a2"},
		Kind:          "work",
		Payload:       map[string]any{"task": "deploy"},
		CauseFlowTurnID: "ft_9",
		Refs: ProducerRef{
			FlowSessionID: "fs_1",
			FlowTurnID:    "ft_9",
			TeamID:        "t_a",
			TeamTurnID:    "tt_1",
			CallID:        "c_1",
			CallTurnID:    "ct_1",
		},
		Status:    MailboxPending,
		Revision:  3,
		CreatedAt: time.Date(2026, 10, 10, 1, 2, 3, 0, time.UTC),
		UpdatedAt: time.Date(2026, 10, 10, 4, 5, 6, 0, time.UTC),
	}
}

func TestMailboxMessageJSONRoundTrip(t *testing.T) {
	orig := sampleMailboxMessage()

	raw, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got MailboxMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.MessageID != orig.MessageID ||
		got.FlowSessionID != orig.FlowSessionID ||
		got.Kind != orig.Kind ||
		got.CauseFlowTurnID != orig.CauseFlowTurnID ||
		got.Status != orig.Status ||
		got.Revision != orig.Revision {
		t.Fatalf("scalar fields mismatch:\n got=%+v\nwant=%+v", got, orig)
	}
	if got.From != orig.From || got.To != orig.To {
		t.Fatalf("address mismatch: got %+v/%+v want %+v/%+v", got.From, got.To, orig.From, orig.To)
	}
	if got.Refs != orig.Refs {
		t.Fatalf("refs mismatch: got %+v want %+v", got.Refs, orig.Refs)
	}
	if !got.CreatedAt.Equal(orig.CreatedAt) || !got.UpdatedAt.Equal(orig.UpdatedAt) {
		t.Fatalf("time mismatch: got %v/%v want %v/%v", got.CreatedAt, got.UpdatedAt, orig.CreatedAt, orig.UpdatedAt)
	}
	if v, ok := got.Payload["task"]; !ok || v != "deploy" {
		t.Fatalf("payload mismatch: %+v", got.Payload)
	}
}

func TestMailboxMessageJSONKeysAreSnakeCase(t *testing.T) {
	raw, err := json.Marshal(sampleMailboxMessage())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(raw)

	for _, key := range []string{
		`"message_id"`,
		`"flow_session_id"`,
		`"cause_flow_turn_id"`,
		`"from"`,
		`"to"`,
		`"team_id"`,
		`"agent_id"`,
		`"created_at"`,
		`"updated_at"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("expected snake_case key %s in JSON: %s", key, s)
		}
	}

	// Guard against accidental Go-name (camel) tags.
	for _, banned := range []string{
		`"MessageID"`,
		`"FlowSessionID"`,
		`"CauseFlowTurnID"`,
		`"CreatedAt"`,
	} {
		if strings.Contains(s, banned) {
			t.Errorf("unexpected camelCase key %s in JSON: %s", banned, s)
		}
	}
}

func TestMailboxMessageStatusValues(t *testing.T) {
	cases := map[MailboxMessageStatus]string{
		MailboxPending:   "pending",
		MailboxClaimed:   "claimed",
		MailboxDelivered: "delivered",
		MailboxDead:      "dead",
	}
	for status, want := range cases {
		if string(status) != want {
			t.Errorf("status %v: want %q", status, want)
		}
	}
}

func TestMailboxEventConstants(t *testing.T) {
	cases := map[string]string{
		EventMailboxMessageEnqueued:  "mailbox.message.enqueued",
		EventMailboxMessageClaimed:   "mailbox.message.claimed",
		EventMailboxMessageDelivered: "mailbox.message.delivered",
		EventMailboxMessageDead:      "mailbox.message.dead",
		EventMailboxBatchDispatched:  "mailbox.batch.dispatched",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("event constant: got %q want %q", got, want)
		}
	}
}
