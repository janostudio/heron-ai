package flow

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// seedMailboxSession writes a flow session with the given status and returns
// it. It is the minimal setup for ResumeForMailbox tests: the resume path only
// reads the session and the mailbox, so no turn has to have run.
func seedMailboxSession(t *testing.T, runtime *Runtime, sessionID string, status types.SessionStatus) types.FlowSession {
	t.Helper()
	session := types.FlowSession{
		ID:        sessionID,
		FlowID:    "code-fix",
		Status:    status,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, runtime.appendSessionEvent(context.Background(), session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionCreated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": session},
	}))
	return session
}

// TestResumeForMailboxWakesAndDrains is the core Step 5 assertion: with a
// pending result message in the coordinator's inbox, ResumeForMailbox starts a
// new FlowTurn, the returned FlowTurnResult is non-zero, the coordinator is
// activated, and the inbox is drained (no message is left pending).
func TestResumeForMailboxWakesAndDrains(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)

	// Post a result message into the coordinator ("default") inbox, mirroring
	// what the background dispatcher does.
	_, err := runtime.enqueueResultMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
		types.MailboxAddress{TeamID: "default"},
		map[string]any{"team_result": types.TeamTurnResult{Reply: "verification passed"}},
	)
	require.NoError(t, err)

	result, err := runtime.ResumeForMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.NotEmpty(t, result.Turn.ID, "a real turn must have run")
	require.Equal(t, types.SessionWaitingInput, result.Session.Status)
	require.NotEmpty(t, result.TeamResults, "the coordinator must have been activated")

	// The inbox is drained: nothing is left pending.
	inbox, err := runtime.listInbox(ctx, session.ID, types.MailboxAddress{TeamID: "default"})
	require.NoError(t, err)
	require.Empty(t, inbox, "inbox must be drained by the resume")

	// The message is now delivered, not pending.
	box, err := runtime.loadMailbox(ctx, session.ID)
	require.NoError(t, err)
	for _, msg := range box {
		if msg.Kind == mailboxKindResult {
			require.Equal(t, types.MailboxDelivered, msg.Status)
		}
	}
}

// TestResumeForMailboxSilentWhenEmpty proves the empty-inbox contract: with
// nothing to summarize, ResumeForMailbox neither errors nor starts a turn.
func TestResumeForMailboxSilentWhenEmpty(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)

	result, err := runtime.ResumeForMailbox(ctx, session.ID)
	require.NoError(t, err, "an empty inbox is not an error")
	require.Empty(t, result.Turn.ID, "no turn should be started for an empty inbox")

	status, err := runtime.Status(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, status.Status, "status must be untouched")
}

// TestResumeForMailboxSilentWhenRunning pins the running guard: a wake that
// races a coordinator that is already running is dropped without error and
// without advancing the session; the result message stays in the inbox.
func TestResumeForMailboxSilentWhenRunning(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	// Seed as created, then mark running, so the result message can be posted
	// without a turn having to run.
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionCreated)
	_, err := runtime.enqueueResultMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
		types.MailboxAddress{TeamID: "default"},
		map[string]any{"team_result": types.TeamTurnResult{Reply: "done"}},
	)
	require.NoError(t, err)

	running := session
	running.Status = types.SessionRunning
	require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
		EventHeader: types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: session.ID},
		Payload:     map[string]any{"session": running},
	}))

	result, err := runtime.ResumeForMailbox(ctx, session.ID)
	require.NoError(t, err, "a running session must not be an error")
	require.Empty(t, result.Turn.ID, "a running session must not be advanced")

	// The result message is still pending for the next turn to drain.
	inbox, err := runtime.listInbox(ctx, session.ID, types.MailboxAddress{TeamID: "default"})
	require.NoError(t, err)
	require.Len(t, inbox, 1, "the result must remain pending while running")
}

// TestResumeForMailboxSilentOnTerminalStatus proves terminal sessions are not
// woken: failed/cancelled/interrupted all return silently.
func TestResumeForMailboxSilentOnTerminalStatus(t *testing.T) {
	for _, status := range []types.SessionStatus{
		types.SessionFailed,
		types.SessionCancelled,
		types.SessionInterrupted,
	} {
		t.Run(string(status), func(t *testing.T) {
			fileStore := storage.NewFileStore(t.TempDir())
			sessionWriter := storage.NewJSONLSessionWriter(fileStore)
			runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

			ctx := context.Background()
			session := seedMailboxSession(t, runtime, newID("fs"), types.SessionCreated)
			_, err := runtime.enqueueResultMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
				types.MailboxAddress{TeamID: "default"},
				map[string]any{"team_result": types.TeamTurnResult{Reply: "done"}},
			)
			require.NoError(t, err)
			terminal := session
			terminal.Status = status
			require.NoError(t, runtime.appendSessionEvent(ctx, session.ID, storage.SessionEvent{
				EventHeader: types.EventHeader{Type: types.EventFlowSessionUpdated, FlowSessionID: session.ID},
				Payload:     map[string]any{"session": terminal},
			}))

			result, err := runtime.ResumeForMailbox(ctx, session.ID)
			require.NoError(t, err)
			require.Empty(t, result.Turn.ID)
		})
	}
}

// TestRenderMailboxInboxRendersReply covers the happy path: a message whose
// team_result carries a Reply renders that reply verbatim, headed by the
// message ID and the originating team.
func TestRenderMailboxInboxRendersReply(t *testing.T) {
	msg := types.MailboxMessage{
		MessageID: "mb_1",
		From:      types.MailboxAddress{TeamID: "verify"},
		Kind:      mailboxKindResult,
		Payload:   map[string]any{"team_result": types.TeamTurnResult{Reply: "verification passed"}},
	}
	out := renderMailboxInbox([]types.MailboxMessage{msg})
	require.Contains(t, out, "mb_1")
	require.Contains(t, out, "verify")
	require.Contains(t, out, "verification passed")
}

// TestRenderMailboxInboxRendersMapForm proves the JSONL-round-tripped shape (a
// generic map instead of the typed struct) renders identically.
func TestRenderMailboxInboxRendersMapForm(t *testing.T) {
	msg := types.MailboxMessage{
		MessageID: "mb_2",
		Kind:      mailboxKindResult,
		Payload: map[string]any{
			"team_result": map[string]any{"Reply": "from a map"},
		},
	}
	out := renderMailboxInbox([]types.MailboxMessage{msg})
	require.Contains(t, out, "from a map")
}

// TestRenderMailboxInboxDegradesGracefully proves a malformed payload does not
// panic and still surfaces the message ID with a human-readable fallback.
func TestRenderMailboxInboxDegradesGracefully(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"nil payload", nil},
		{"missing team_result", map[string]any{"other": 1}},
		{"team_result wrong type", map[string]any{"team_result": 42}},
		{"team_result without reply", map[string]any{"team_result": map[string]any{"Records": []any{}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.MailboxMessage{MessageID: "mb_bad", Kind: mailboxKindResult, Payload: tc.payload}
			require.NotPanics(t, func() {
				out := renderMailboxInbox([]types.MailboxMessage{msg})
				require.Contains(t, out, "mb_bad")
			})
		})
	}
}

// TestRenderMailboxInboxErrorFallback proves a payload carrying an error (the
// dispatch failure shape) falls back to that error text when there is no
// renderable Reply.
func TestRenderMailboxInboxErrorFallback(t *testing.T) {
	msg := types.MailboxMessage{
		MessageID: "mb_err",
		Kind:      mailboxKindResult,
		Payload:   map[string]any{"error": "team blew up"},
	}
	out := renderMailboxInbox([]types.MailboxMessage{msg})
	require.Contains(t, out, "mb_err")
	require.Contains(t, out, "team blew up")
}

// TestRenderMailboxInboxRendersDeliveredResult pins the shape the async
// dispatcher actually writes: deliverMessage stores the output under
// payload.result, so the team_result is one level down. Reading only the top
// level left every dispatched result rendering as "raw result unavailable".
func TestRenderMailboxInboxRendersDeliveredResult(t *testing.T) {
	msg := types.MailboxMessage{
		MessageID: "mb_delivered",
		Kind:      mailboxKindWork,
		Payload: map[string]any{
			"caller_team": "default",
			"result": map[string]any{
				"team_result": map[string]any{"Reply": "the branch is master"},
			},
		},
	}
	out := renderMailboxInbox([]types.MailboxMessage{msg})
	require.Contains(t, out, "the branch is master")
	// The reply must be rendered as text, not as the JSON container the
	// degraded summary path would produce: both contain the reply string, so
	// asserting only Contains cannot tell a real read from a fallback.
	require.NotContains(t, out, `"team_result"`)
	require.NotContains(t, out, `"Reply"`)
	require.NotContains(t, out, "raw result unavailable")
}

// TestRenderMailboxInboxWorkMessageIsNotAResult pins the waking-turn case: the
// message that wakes a Team is a work instruction still waiting in its inbox,
// so it must not be described as a result that failed to arrive.
func TestRenderMailboxInboxWorkMessageIsNotAResult(t *testing.T) {
	msg := types.MailboxMessage{
		MessageID: "mb_work",
		Kind:      mailboxKindWork,
		To:        types.MailboxAddress{TeamID: "repo-info"},
		Payload:   map[string]any{"caller_team": "owner"},
	}
	out := renderMailboxInbox([]types.MailboxMessage{msg})
	require.Contains(t, out, "dispatched to repo-info")
	require.NotContains(t, out, "raw result unavailable")
}

// TestRenderMailboxInboxAfterDeliverMessage closes the loop between writer and
// reader: the async dispatcher marks a message delivered with its output under
// payload.result, and the wake-up renderer has to read that exact shape. A
// hand-built payload would keep passing even if the two drifted apart again.
func TestRenderMailboxInboxAfterDeliverMessage(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionRunning)

	msg, err := runtime.enqueueMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
		types.MailboxAddress{TeamID: "owner"}, mailboxKindWork,
		map[string]any{"caller_team": "default"},
	)
	require.NoError(t, err)

	delivered, err := runtime.deliverMessage(ctx, session, msg, map[string]any{
		"team_result": types.TeamTurnResult{Reply: "the branch is master"},
	})
	require.NoError(t, err)

	out := renderMailboxInbox([]types.MailboxMessage{delivered})
	require.Contains(t, out, "the branch is master")
	require.NotContains(t, out, `"team_result"`)
	require.NotContains(t, out, `"Reply"`)
	require.NotContains(t, out, "raw result unavailable")
}

// TestResumeForMailboxMarksTurnOrigin pins the origin/intermediate contract the
// JSON-RPC caller needs to relay a background answer: a wake-up that has a
// caller is an internal hop, one without a caller (the entry Team summarising
// its inbox) is the answer for the user.
func TestResumeForMailboxMarksTurnOrigin(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	runtime := newTestRuntime(testDefinitions(), &fakeTeamRuntime{}, sessionWriter, nil)

	ctx := context.Background()
	session := seedMailboxSession(t, runtime, newID("fs"), types.SessionWaitingInput)

	_, err := runtime.enqueueResultMessage(ctx, session, types.FlowTurn{ID: newID("ft")},
		types.MailboxAddress{TeamID: "default"},
		map[string]any{"team_result": types.TeamTurnResult{Reply: "verification passed"}},
	)
	require.NoError(t, err)

	result, err := runtime.ResumeForMailbox(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, turnOriginMailbox, result.Turn.Origin)
	require.False(t, result.Turn.Intermediate,
		"a wake-up with no caller is the final hop and must be relayable")
}

// TestCallerTeamOfIgnoresEmptyCallers covers the dispatch-triggered wake-up,
// where the drained mail names the Team that handed the work over.
func TestCallerTeamOfIgnoresEmptyCallers(t *testing.T) {
	require.Equal(t, "", callerTeamOf(nil))
	require.Equal(t, "", callerTeamOf([]activation{{teamID: "default"}}))
	require.Equal(t, "default", callerTeamOf([]activation{{teamID: "owner", callerTeam: "default"}}))
}
