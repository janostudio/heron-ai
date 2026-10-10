package flow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// newMailboxTestRuntime builds a Runtime backed by a real JSONL session writer
// over a temp file store — the same wiring the existing flow tests use.
func newMailboxTestRuntime(t *testing.T) (*Runtime, storage.SessionWriter) {
	t.Helper()
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	rt := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)
	return rt, sessionWriter
}

func mailboxTestSession(t *testing.T) (types.FlowSession, types.FlowTurn) {
	t.Helper()
	session := types.FlowSession{
		ID:     newID("fs"),
		FlowID: "code-fix",
		Status: types.SessionWaitingInput,
	}
	turn := types.FlowTurn{ID: newID("ft"), FlowSessionID: session.ID}
	return session, turn
}

func addr(team, agent string) types.MailboxAddress {
	return types.MailboxAddress{TeamID: team, AgentID: agent}
}

// TestMailboxEnqueueThenLoadRoundTrips verifies that an enqueued work message
// reads back from the event stream as a pending message.
func TestMailboxEnqueueThenLoadRoundTrips(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("default", "worker"), map[string]any{"task": "fix"})
	require.NoError(t, err)
	require.Equal(t, types.MailboxPending, msg.Status)
	require.Equal(t, 0, msg.Revision)
	require.Equal(t, mailboxKindWork, msg.Kind)
	require.Equal(t, session.ID, msg.FlowSessionID)
	require.Equal(t, turn.ID, msg.CauseFlowTurnID)
	require.NotEmpty(t, msg.MessageID)

	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, box, 1)

	got, ok := box[msg.MessageID]
	require.True(t, ok)
	assert.Equal(t, types.MailboxPending, got.Status)
	assert.Equal(t, 0, got.Revision)
	assert.Equal(t, "default", got.To.TeamID)
	assert.Equal(t, "worker", got.To.AgentID)
	assert.Equal(t, "fix", got.Payload["task"])
}

// TestMailboxClaimDeliverTransitions verifies the Revision increments and the
// folded terminal state is delivered.
func TestMailboxClaimDeliverTransitions(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("default", "worker"), nil)
	require.NoError(t, err)

	claimed, err := rt.claimMessage(ctx, session, msg)
	require.NoError(t, err)
	require.Equal(t, types.MailboxClaimed, claimed.Status)
	require.Equal(t, 1, claimed.Revision)

	delivered, err := rt.deliverMessage(ctx, session, claimed, map[string]any{"ok": true})
	require.NoError(t, err)
	require.Equal(t, types.MailboxDelivered, delivered.Status)
	require.Equal(t, 2, delivered.Revision)

	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	got := box[msg.MessageID]
	assert.Equal(t, types.MailboxDelivered, got.Status)
	assert.Equal(t, 2, got.Revision)

	result, ok := got.Payload["result"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, true, result["ok"])
}

// TestMailboxDeadTransition verifies marking dead wins the fold.
func TestMailboxDeadTransition(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("default", "worker"), nil)
	require.NoError(t, err)

	dead, err := rt.markMessageDead(ctx, session, msg, "retry limit exceeded")
	require.NoError(t, err)
	require.Equal(t, types.MailboxDead, dead.Status)
	require.Equal(t, 1, dead.Revision)

	box, err := rt.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	got := box[msg.MessageID]
	assert.Equal(t, types.MailboxDead, got.Status)
	assert.Equal(t, "retry limit exceeded", got.Payload["dead_reason"])
}

// TestMailboxEventsWrittenWithExpectedTypes asserts the four lifecycle events
// land in flow.jsonl with the right Type and the message payload key.
func TestMailboxEventsWrittenWithExpectedTypes(t *testing.T) {
	rt, writer := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	msg, err := rt.enqueueWorkMessage(ctx, session, turn, addr("default", "worker"), nil)
	require.NoError(t, err)
	claimed, err := rt.claimMessage(ctx, session, msg)
	require.NoError(t, err)
	_, err = rt.deliverMessage(ctx, session, claimed, nil)
	require.NoError(t, err)

	// A second message exercises the dead event.
	msg2, err := rt.enqueueWorkMessage(ctx, session, turn, addr("default", "worker"), nil)
	require.NoError(t, err)
	_, err = rt.markMessageDead(ctx, session, msg2, "boom")
	require.NoError(t, err)

	replay, err := writer.Replay(ctx, session.ID)
	require.NoError(t, err)

	seen := map[string]int{}
	for _, event := range replay.Events {
		seen[event.Type]++
		require.NotNil(t, event.Payload["message"], "event %s missing message payload", event.Type)
		require.Equal(t, session.ID, event.FlowSessionID)
	}
	assert.Equal(t, 2, seen[types.EventMailboxMessageEnqueued])
	assert.Equal(t, 1, seen[types.EventMailboxMessageClaimed])
	assert.Equal(t, 1, seen[types.EventMailboxMessageDelivered])
	assert.Equal(t, 1, seen[types.EventMailboxMessageDead])

	// The enqueued events carry the cause turn.
	enqueuedFlowTurns := 0
	for _, event := range replay.Events {
		if event.Type == types.EventMailboxMessageEnqueued && event.FlowTurnID == turn.ID {
			enqueuedFlowTurns++
		}
	}
	assert.Equal(t, 2, enqueuedFlowTurns)
}

// TestFoldMailboxIsOrderIndependent feeds the same lifecycle events in several
// orders and asserts the fold produces identical terminal state, exercising the
// Revision/Seq tiebreak directly.
func TestFoldMailboxIsOrderIndependent(t *testing.T) {
	base := types.MailboxMessage{
		MessageID:     "mb_1",
		FlowSessionID: "fs_1",
		To:            addr("default", "worker"),
		Kind:          mailboxKindWork,
	}
	mk := func(rev int, status types.MailboxMessageStatus, seq int64) storage.SessionEvent {
		m := base
		m.Revision = rev
		m.Status = status
		return storage.SessionEvent{
			EventHeader: types.EventHeader{
				Seq:           seq,
				Type:          types.EventMailboxMessageEnqueued,
				FlowSessionID: "fs_1",
			},
			Payload: map[string]any{mailboxPayloadKey: m},
		}
	}

	enqueued := mk(0, types.MailboxPending, 1)
	claimed := mk(1, types.MailboxClaimed, 2)
	delivered := mk(2, types.MailboxDelivered, 3)

	orders := [][]storage.SessionEvent{
		{enqueued, claimed, delivered},
		{delivered, claimed, enqueued},
		{claimed, enqueued, delivered},
		{delivered, enqueued, claimed},
	}
	for _, events := range orders {
		box := foldMailbox(events)
		require.Len(t, box, 1)
		got := box["mb_1"]
		assert.Equal(t, types.MailboxDelivered, got.Status)
		assert.Equal(t, 2, got.Revision)
	}
}

// TestFoldMailboxRevisionTieBreaksOnSeq verifies that when two events share the
// same Revision, the larger Seq wins — independent of arrival order.
func TestFoldMailboxRevisionTieBreaksOnSeq(t *testing.T) {
	m := types.MailboxMessage{
		MessageID: "mb_1",
		To:        addr("default", "worker"),
		Revision:  1,
	}
	lowSeq := storage.SessionEvent{
		EventHeader: types.EventHeader{Seq: 10, Type: types.EventMailboxMessageClaimed},
		Payload:     map[string]any{mailboxPayloadKey: withStatus(m, types.MailboxClaimed)},
	}
	highSeq := storage.SessionEvent{
		EventHeader: types.EventHeader{Seq: 20, Type: types.EventMailboxMessageDead},
		Payload:     map[string]any{mailboxPayloadKey: withStatus(m, types.MailboxDead)},
	}

	for _, events := range [][]storage.SessionEvent{
		{lowSeq, highSeq},
		{highSeq, lowSeq},
	} {
		box := foldMailbox(events)
		require.Equal(t, types.MailboxDead, box["mb_1"].Status)
	}
}

// TestMailboxListInboxFiltersAndOrders verifies listInbox only returns pending
// messages for the target address, ordered by CreatedAt.
func TestMailboxListInboxFiltersAndOrders(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	ctx := context.Background()
	session, turn := mailboxTestSession(t)

	target := addr("default", "worker")
	other := addr("verify", "checker")

	first, err := rt.enqueueWorkMessage(ctx, session, turn, target, map[string]any{"n": 1})
	require.NoError(t, err)
	second, err := rt.enqueueWorkMessage(ctx, session, turn, target, map[string]any{"n": 2})
	require.NoError(t, err)
	_, err = rt.enqueueWorkMessage(ctx, session, turn, other, nil)
	require.NoError(t, err)

	// Claim the first so it leaves the pending inbox; enqueue a third pending.
	_, err = rt.claimMessage(ctx, session, first)
	require.NoError(t, err)
	third, err := rt.enqueueWorkMessage(ctx, session, turn, target, nil)
	require.NoError(t, err)

	inbox, err := rt.listInbox(ctx, session.ID, target)
	require.NoError(t, err)
	require.Len(t, inbox, 2)
	// second and third are pending for target; ordered by CreatedAt asc, falling
	// back to MessageID for identical timestamps.
	assert.Equal(t, second.MessageID, inbox[0].MessageID)
	assert.Equal(t, third.MessageID, inbox[1].MessageID)
	for _, m := range inbox {
		assert.Equal(t, types.MailboxPending, m.Status)
		assert.Equal(t, target, m.To)
	}
}

// TestMailboxListInboxEmptySession verifies a session with no events folds to an
// empty mailbox rather than erroring.
func TestMailboxListInboxEmptySession(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	inbox, err := rt.listInbox(context.Background(), "fs_missing", addr("default", "worker"))
	require.NoError(t, err)
	assert.Empty(t, inbox)
}

func withStatus(m types.MailboxMessage, status types.MailboxMessageStatus) types.MailboxMessage {
	m.Status = status
	return m
}
