package flow

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// inputRecordingTeamRuntime keeps every TeamTurnRequest.Input it is asked to
// run, and answers with a route the test controls.
type inputRecordingTeamRuntime struct {
	mu     sync.Mutex
	inputs []string
	next   *types.Route
}

func (r *inputRecordingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	r.mu.Lock()
	r.inputs = append(r.inputs, req.Input)
	next := r.next
	r.mu.Unlock()
	if next == nil {
		next = &types.Route{Action: types.NextProceed}
	}
	return types.TeamTurnResult{Turn: req.TeamTurn, Reply: "ok", Next: next}, nil
}

func (r *inputRecordingTeamRuntime) recorded() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.inputs))
	copy(out, r.inputs)
	return out
}

// A Team woken by a mailbox has no user input of its own. The task has to ride
// on the message; otherwise the Team can only consult its own history and
// repeat the previous verdict, which reaches the user as a stale answer.
func TestMessageTurnFeedsDispatchedTaskToTeam(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	defs := testDefinitions()
	defs.Flow.Teams["verify"] = types.FlowTeamBinding{
		ID:     "verify",
		TeamID: "verify-team",
		Inputs: types.InputSpec{UserMessage: true},
	}
	rt := newTestRuntime(defs, recorder, writer, nil)

	session, turn := mailboxTestSession(t)
	const task = "查看仓库 heron-connect，目前主分支是什么"
	msg, err := rt.enqueueWorkMessage(context.Background(), session, turn,
		addr("verify", ""),
		map[string]any{"caller_team": "default", "input": task},
	)
	require.NoError(t, err)

	rt.runMessageTurn(context.Background(), session, msg)

	require.Equal(t, []string{task}, recorder.recorded(),
		"the woken Team must receive the task it was dispatched for")
}

// A Team that dispatches further down the chain has no user input of its own
// either, so the task has to keep travelling with the messages.
func TestDispatchedTeamForwardsTaskToItsOwnDispatch(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{
		next: &types.Route{Action: types.NextActivate, Teams: []string{"default"}},
	}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	defs := testDefinitions()
	defs.Flow.Teams["verify"] = types.FlowTeamBinding{
		ID:          "verify",
		TeamID:      "verify-team",
		CanActivate: []string{"default"},
		Inputs:      types.InputSpec{UserMessage: true},
	}
	rt := newTestRuntime(defs, recorder, writer, nil)
	rt.SetAsyncDispatch(true)

	session, turn := mailboxTestSession(t)
	const task = "查看仓库 heron-connect，目前主分支是什么"
	msg, err := rt.enqueueWorkMessage(context.Background(), session, turn,
		addr("verify", ""),
		map[string]any{"caller_team": "default", "input": task},
	)
	require.NoError(t, err)

	rt.runMessageTurn(context.Background(), session, msg)

	box, err := rt.loadMailbox(context.Background(), session.ID)
	require.NoError(t, err)
	forwarded := 0
	for _, m := range box {
		if m.Kind == mailboxKindWork && m.To.TeamID == "default" {
			forwarded++
			require.Equal(t, task, m.Payload["input"],
				"a downstream dispatch must carry the original task")
		}
	}
	require.Equal(t, 1, forwarded, "verify must dispatch once to default")
}

// The entry Team is woken to answer the user, so the result message has to
// carry the question along with the result it is relaying.
func TestPostResultCarriesTaskInput(t *testing.T) {
	rt, _ := newMailboxTestRuntime(t)
	session, turn := mailboxTestSession(t)
	turn.Input = "查看仓库 heron-connect，目前主分支是什么"

	require.NoError(t, rt.postResult(context.Background(), session, turn, "default", teamExecution{
		result: types.TeamTurnResult{Reply: "heron-connect 的主分支是 main"},
	}))

	box, err := rt.loadMailbox(context.Background(), session.ID)
	require.NoError(t, err)
	require.Len(t, box, 1)
	for _, msg := range box {
		require.Equal(t, mailboxKindResult, msg.Kind)
		require.Equal(t, turn.Input, msg.Payload["input"])
	}
}

// A mailbox turn that starts with an activator can still end at the entry
// Team. That final hop answers the user, so the turn must not be marked
// intermediate — otherwise the only turn carrying the answer is suppressed.
func TestEntryHopClearsIntermediateOnMailboxTurn(t *testing.T) {
	rt, writer := newMailboxTestRuntime(t)
	session, turn := mailboxTestSession(t)
	turn.Origin = turnOriginMailbox
	turn.Intermediate = true

	result, err := rt.finishTurn(context.Background(), types.FlowTurnResult{
		Session:             session,
		Turn:                turn,
		FinalReply:          "仓库 heron-connect 的主分支是 main",
		FinalReplyFromEntry: true,
	}, types.SessionWaitingInput, nil)
	require.NoError(t, err)
	require.False(t, result.Turn.Intermediate)

	replay, err := writer.Replay(context.Background(), session.ID)
	require.NoError(t, err)
	var payload map[string]any
	for _, event := range replay.Events {
		if event.FlowTurnID == turn.ID {
			payload = event.Payload
		}
	}
	require.NotNil(t, payload)
	require.Equal(t, false, payload["intermediate"],
		"the relayed event must not suppress the answer")
}
