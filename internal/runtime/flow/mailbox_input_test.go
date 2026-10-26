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
	mu      sync.Mutex
	inputs  []string
	records []string
	blocks  []types.ContextBlock
	next    *types.Route
}

func (r *inputRecordingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	r.mu.Lock()
	r.inputs = append(r.inputs, req.Input)
	r.blocks = append(r.blocks, req.ContextBlocks...)
	for _, record := range req.Records {
		r.records = append(r.records, record.Name)
	}
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

// recordedBlocks returns the context blocks the Team turn was given.
func (r *inputRecordingTeamRuntime) recordedBlocks() []types.ContextBlock {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.ContextBlock, len(r.blocks))
	copy(out, r.blocks)
	return out
}

// blockOfKind returns the first recorded block of the given kind, or nil.
func (r *inputRecordingTeamRuntime) blockOfKind(kind string) *types.ContextBlock {
	for _, block := range r.recordedBlocks() {
		if block.Kind == kind {
			found := block
			return &found
		}
	}
	return nil
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

// A Team woken by a result message must see what the upstream Team produced.
// Those records are published in team scope; a Team's declared flow_records are
// matched against the flow-scope slice, so they have to be promoted first.
func TestResultMessageExposesUpstreamRecords(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	defs := testDefinitions()
	defs.Flow.Teams["default"] = types.FlowTeamBinding{
		ID:     "default",
		TeamID: "default-team",
		Inputs: types.InputSpec{
			UserMessage: true,
			FlowRecords: []string{"OwnerReply"},
		},
	}
	rt := newTestRuntime(defs, recorder, writer, nil)

	session, turn := mailboxTestSession(t)
	msg, err := rt.enqueueResultMessage(context.Background(), session, turn,
		addr("default", ""),
		map[string]any{
			"input": "查看仓库 heron-connect，目前主分支是什么",
			"team_result": types.TeamTurnResult{
				Reply: "heron-connect 的主分支是 main",
				Records: []types.SharedRecord{{
					RecordID: "owner-1",
					Name:     "OwnerReply",
					Kind:     "agent_result",
					Scope:    types.RecordScopeTeam,
					Summary:  "heron-connect 的主分支是 main",
				}},
			},
		},
	)
	require.NoError(t, err)

	rt.runMessageTurn(context.Background(), session, msg)

	recorder.mu.Lock()
	seen := append([]string(nil), recorder.records...)
	recorder.mu.Unlock()
	require.Contains(t, seen, "OwnerReply",
		"the entry Team must receive the upstream result record")
}

// ResumeForMailbox starts the woken Team's turn at that Team, not at the flow
// entry, so the user's original input does not reach it by the usual path. The
// dispatched messages carry it; without it the Team can only answer from its
// own history.
func TestResumeForMailboxFeedsDispatchedTask(t *testing.T) {
	recorder := &inputRecordingTeamRuntime{}
	writer := storage.NewJSONLSessionWriter(storage.NewFileStore(t.TempDir()))
	defs := testDefinitions()
	defs.Flow.Teams["default"] = types.FlowTeamBinding{
		ID:     "default",
		TeamID: "default-team",
		Inputs: types.InputSpec{UserMessage: true},
	}
	rt := newTestRuntime(defs, recorder, writer, nil)

	session := seedMailboxSession(t, rt, "fs-task", types.SessionWaitingInput)
	turn := types.FlowTurn{ID: "ft-1", FlowSessionID: session.ID}
	const task = "查看仓库genie，目前主分支是什么"
	_, err := rt.enqueueResultMessage(context.Background(), session, turn,
		addr("default", ""),
		map[string]any{
			"input":       task,
			"team_result": types.TeamTurnResult{Reply: "上游结果"},
		},
	)
	require.NoError(t, err)

	_, err = rt.ResumeForMailbox(context.Background(), session.ID)
	require.NoError(t, err)

	// The task must still reach the woken Team — but as the statement the
	// continuation belongs to, not as this turn's input. A result message is a
	// report coming back, not a new assignment: rendering the old question as
	// the trailing user turn made the Team answer it from scratch and dispatch
	// the same job again (s17).
	for _, input := range recorder.recorded() {
		require.NotEqual(t, task, input,
			"a result message must not become the turn's input")
	}
	block := recorder.blockOfKind("user_task")
	require.NotNil(t, block, "the continued task statement must be carried")
	require.Equal(t, task, block.Text)
}
