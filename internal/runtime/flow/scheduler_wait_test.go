package flow

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// waitingTeamRuntime dispatches "verify", which immediately parks waiting for a
// Tool task. This is the shape of an Agent that called an asynchronous Tool:
// the Team round is over, the work is not.
type waitingTeamRuntime struct {
	mu        sync.Mutex
	activated bool
}

func (w *waitingTeamRuntime) Run(_ context.Context, req types.TeamTurnRequest) (types.TeamTurnResult, error) {
	switch req.TeamTurn.TeamID {
	case "default":
		w.mu.Lock()
		activated := w.activated
		w.activated = true
		w.mu.Unlock()
		if activated {
			return types.TeamTurnResult{
				Turn:  req.TeamTurn,
				Reply: "coordination finished",
				Next:  &types.Route{Action: types.NextProceed},
			}, nil
		}
		return types.TeamTurnResult{
			Turn:  req.TeamTurn,
			Reply: "starting verification",
			Next:  &types.Route{Action: types.NextActivate, Teams: []string{"verify"}},
		}, nil
	case "verify":
		turn := req.TeamTurn
		turn.Status = types.TurnWaitingTool
		return types.TeamTurnResult{
			Turn:  turn,
			Reply: "waiting for the async Tool task",
			Next:  &types.Route{Action: types.NextWaitTool, Reason: "waiting for asynchronous Tool task"},
			PendingToolTasks: []types.PendingToolTask{{
				TaskID: "task-1", CallTurnID: req.TeamTurn.ID, Status: types.TurnWaitingTool,
			}},
		}, nil
	default:
		return types.TeamTurnResult{
			Turn: req.TeamTurn,
			Next: &types.Route{Action: types.NextFail, Reason: "unexpected team"},
		}, nil
	}
}

// TestDispatchedTeamWaitingForToolParksTheSession is the regression guard for a
// dispatched round that stops for a Tool task: postRoutes used to answer
// "unsupported next action" and nothing wrote the waiting status, so the
// session stayed waiting_input while its Tool completion callback only resumes
// waiting_tool sessions. The finished task's result was dropped after a minute
// and the session stopped answering.
func TestDispatchedTeamWaitingForToolParksTheSession(t *testing.T) {
	fileStore := storage.NewFileStore(t.TempDir())
	sessionWriter := storage.NewJSONLSessionWriter(fileStore)
	evidenceStore := storage.NewJSONLEvidenceStore(fileStore)
	runtime := newTestRuntime(testDefinitions(), &waitingTeamRuntime{}, sessionWriter, evidenceStore)
	runtime.SetAsyncDispatch(true)

	result, err := runtime.Start(context.Background(), types.StartFlowRequest{
		FlowID: "code-fix",
		Input:  "verify the change",
	})
	require.NoError(t, err)
	require.Equal(t, types.SessionWaitingInput, result.Session.Status, "the dispatching turn itself does not wait")

	require.Eventually(t, func() bool {
		session, loadErr := runtime.loadSession(context.Background(), result.Session.ID)
		return loadErr == nil && session.Status == types.SessionWaitingTool
	}, 5*time.Second, 20*time.Millisecond,
		"a dispatched round parked on a Tool task must leave the session in waiting_tool")

	// The work message must complete normally: parking is not a routing error.
	require.Eventually(t, func() bool {
		box, boxErr := runtime.loadMailbox(context.Background(), result.Session.ID)
		if boxErr != nil {
			return false
		}
		for _, msg := range box {
			if msg.Kind == mailboxKindWork && msg.To.TeamID == "verify" {
				return msg.Status == types.MailboxDelivered
			}
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the dispatched work message must be delivered, not marked dead")
}
